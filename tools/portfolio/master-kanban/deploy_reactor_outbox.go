package main

// deploy_reactor_outbox.go — der Deploy-Reaktor als OUTBOX-KONSUMENT
// (Code-Fabrik Release-Pipeline WP5 · D7/D10-D20).
//
// EIN Trigger: MR_MERGED_GREEN. Der Merger schreibt die Tatsache als
// Transactional-Outbox-Zeile (status='pending', PRD D10) — dieser Reaktor
// KONSUMIERT sie, er erzeugt sie NIE. Wahrheitsquelle ist die Tabelle
// (SELECT … WHERE status='pending'), pg_notify ist nur Wakeup-Hint. Der
// GitHub-Webhook-Pfad in deploy_reactor.go ist damit geparkt (D7): EIN
// Deployer, EIN „was ist live".
//
// Eigentümerschaft (D13): dieser Reaktor besitzt die Ledger-Zeile während des
// Deploys via Lease (owned_by/owned_until). Der Reconciler (deployments_
// reconcile.go) überspringt geleaste Zeilen — kein Race auf status.
//
// Mechanik delegiert an ../deploy-gt.sh (SHA-gepinnt, D12); Smoke nutzt die
// Sonden aus deployments_reconcile.go wieder (probeRow, D18). Rollback,
// Circuit-Breaker (D15), Quarantäne und Eskalation (D20c) leben hier.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// ServiceRecipe sagt dem Reaktor, WIE ein Service gebaut und gesmoked wird.
// Die Outbox-Zeile trägt nur service/env/sha — das Rezept liefert den Rest.
type ServiceRecipe struct {
	Type      string `yaml:"type"`       // 'go' (Default) | 'node' — wählt deploy-gt.sh vs. deploy-node.sh (sa-deploy-stufen W4)
	Src       string `yaml:"src"`        // App-Relpfad im Repo (--src): go=Go-Paket, node=App-Dir
	Bin       string `yaml:"bin"`        // Ziel-PFAD auf der Box: go=Binary (--bin), node=Deploy-Wurzel (--dest)
	Unit      string `yaml:"unit"`       // systemd-Unit; leer = cli-Service ohne Restart
	Box       string `yaml:"box"`        // ssh-Ziel (--box); leer = lokaler Deploy (sa-deploy-stufen W2)
	BuildCmd  string `yaml:"build_cmd"`  // node: Build-Kommando (deploy-node.sh --build-cmd); leer = dessen Default
	ProbeKind string `yaml:"probe_kind"` // 'http' | 'cli' (D18)
	HealthURL string `yaml:"health_url"` // http: /version-URL · cli: absoluter Binary-/Wrapper-Pfad
	SmokeURL  string `yaml:"smoke_url"`  // http: eine echte Business-Route (2xx erwartet, D18)

	// JourneyShadow schaltet den Journey-Smoke im SHADOW-MODE zu (PRD ui-journey-
	// testing WP3/D14): report-only NEBEN der weiterhin ALLEIN entscheidenden
	// HTTP/CLI-Probe. Additiv — fehlt/false ⇒ unverändertes Verhalten (kein
	// Bestands-Service wird durch die Einführung berührt). Rollback-Arming kommt
	// erst NACH dem Kalibrierfenster (≥20 Deploys, 0 Falsch-Rot), nicht mit diesem
	// Flag.
	JourneyShadow bool `yaml:"journey_shadow"`
}

// recipeType normalisiert das Rezept-Feld ("" → "go", rückwärtskompatibel).
func recipeType(rec ServiceRecipe) string {
	if strings.ToLower(strings.TrimSpace(rec.Type)) == "node" {
		return "node"
	}
	return "go"
}

// deployMethod ist der Ledger-Stempel (deploy_method) je Rezept-Typ.
func deployMethod(rec ServiceRecipe) string {
	if recipeType(rec) == "node" {
		return "deploy-node"
	}
	return "deploy-gt"
}

type reactorManifest struct {
	Repo     string                   `yaml:"repo"`     // git-Wurzel (Default /opt/stack)
	Services map[string]ServiceRecipe `yaml:"services"` // service-name → Rezept
}

func loadReactorManifest(path string) (*reactorManifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var m reactorManifest
	if err := yaml.NewDecoder(f).Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest %s nicht lesbar: %w", path, err)
	}
	if m.Repo == "" {
		m.Repo = "/opt/stack"
	}
	return &m, nil
}

type outboxRow struct {
	ID          int64
	Service     string
	Environment string
	GitSha      string
	Version     string
	Status      string
}

// ── Pure Zustandsmaschine (D13-Reaktor-Übergangstabelle, WP6-testbar) ────────

type claimAction int

const (
	actClaim          claimAction = iota // pending, Breaker zu → Deploy starten
	actSkipBreaker                       // Breaker offen → nichts anfassen (D15)
	actSkipNotPending                    // Zeile nicht mehr pending (Doppel-Zustellung, D11)
)

// decideClaim: darf der Reaktor diese Outbox-Zeile übernehmen? Idempotenz (D11)
// entsteht strukturell — nur status='pending' wird selektiert; eine schon
// live/deploying/rolled_back-Zeile derselben SHA (UNIQUE service,env,sha) wird
// nie zweimal deployt.
func decideClaim(status string, breakerOpen bool) claimAction {
	if breakerOpen {
		return actSkipBreaker
	}
	if status != "pending" {
		return actSkipNotPending
	}
	return actClaim
}

type smokeVerdict int

const (
	smokeGreen    smokeVerdict = iota // erreicht + SHA stimmt → live
	smokeRetry                        // rot, aber Retries/Frist offen (Readiness-Backoff D18)
	smokeRollback                     // rot und erschöpft → Rollback (D12)
)

// decideSmoke: Smoke-Entscheid je Versuch. forceRed ist die deterministische
// Rot-Injektion (SMOKE_FORCE_RED / Game-Day) — sie erschöpft die Retries wie
// echtes Rot und mündet in Rollback. dueHit = Gesamt-Frist gerissen (D17).
func decideSmoke(reached, shaOK, forceRed bool, attempt, maxAttempts int, dueHit bool) smokeVerdict {
	if !forceRed && reached && shaOK {
		return smokeGreen
	}
	if attempt < maxAttempts && !dueHit {
		return smokeRetry
	}
	return smokeRollback
}

type rollbackAction int

const (
	rbDeployPrev    rollbackAction = iota // prev-SHA existiert → SHA-gepinnt zurückbauen
	rbErroredNoPrev                       // kein prev (Erst-Deploy) → errored + eskalieren, nicht so tun als ob
)

func decideRollback(prevSha string) rollbackAction {
	if strings.TrimSpace(prevSha) == "" {
		return rbErroredNoPrev
	}
	return rbDeployPrev
}

// breakerOpens: K rote Deploys in Folge öffnen den Riegel (D15) — getrennt von
// der WP7-Burn-Ratio. Danach kein Auto-Deploy bis manueller Reset.
func breakerOpens(consecutiveReds, k int) bool { return consecutiveReds >= k }

// journeyVerdict übersetzt den journey-run-Exit in ein Verdict-Wort (Wrapper-
// Kontrakt: 0 grün · 1 Journey rot · 3 Harness). Alles andere (Timeout-Kill,
// Startfehler, -1) zählt als Harness — im SHADOW-MODE (D14) NIE als „rot": ein
// Runner-Defekt darf später nie fälschlich einen guten Deploy zurückrollen.
// Pur/DB-frei, damit die Klassifikation als Zustandsmaschine testbar bleibt.
func journeyVerdict(exit int) string {
	switch exit {
	case 0:
		return "green"
	case 1:
		return "red"
	default: // 3 Harness und jeder unerwartete Code (kill/-1) → Harness, nie rot
		return "harness"
	}
}

// envEligible: darf ein Reaktor mit Stufen-Scope reactorEnv diese Outbox-Zeile
// (Stufe rowEnv) überhaupt anfassen? Leerer Scope = alle Stufen (rückwärts-
// kompatibel); sonst NUR die eigene (sa-deploy-stufen W2: prod-Reaktor zieht
// 'prod-mvp', sa-staging-drain 'staging'). Poka-yoke-Zwilling zum SQL-Filter
// ($1=” OR environment=$1) in runOnce/reclaimStranded: die SELECT-Query
// schließt Fremd-Stufen schon in der DB aus — dieser Guard in der Drain-Schleife
// ist der Gürtel dazu. Selbst wenn je eine Fremd-Zeile durchrutschte (manueller
// Insert, Query-Drift), fasst der prod-Reaktor keine staging-Zeile an und
// umgekehrt — genau der wiederkehrende Kollisions-Befund (W1/W2/W4), bei dem der
// ungescopte prod-Reaktor staging-Zeilen als "kein Rezept" errorte.
func envEligible(reactorEnv, rowEnv string) bool {
	return reactorEnv == "" || rowEnv == reactorEnv
}

// ── Reaktor (Orchestrierung; injizierbare Kanten für Tests) ──────────────────

type reactor struct {
	pool        *pgxpool.Pool
	man         *reactorManifest
	scriptPath  string // deploy-gt.sh (Go-Binary-Swap)
	nodeScript  string // deploy-node.sh (Bundle-Deploy, sa-deploy-stufen W4)
	owner       string
	environment string // "" = alle Stufen draina; sonst nur diese (sa-deploy-stufen W2)
	lease       time.Duration
	maxSmoke    int
	smokeSleep  time.Duration
	smokeDue    time.Duration
	probeTO     time.Duration
	breakerK    int
	stateDir    string // Breaker-Marker
	eventsDir   string // durable Eskalations-Artefakte (D20c)

	// Journey-Shadow (WP3/D14) — report-only, entscheidet NIE.
	journeyRun       string        // Pfad zum journey-run-Wrapper
	journeyShadowDir string        // <dir>/<service>.jsonl (Kalibrier-Rohdaten)
	journeyShadowTO  time.Duration // harter, deploy-unabhängiger Timeout je Lauf

	logf func(string, ...any)
}

func (r *reactor) breakerFile() string { return filepath.Join(r.stateDir, "DEPLOY_BREAKER_OPEN") }

func (r *reactor) breakerIsOpen() bool {
	_, err := os.Stat(r.breakerFile())
	return err == nil
}

func (r *reactor) openBreaker(reason string) {
	_ = os.MkdirAll(r.stateDir, 0755)
	_ = os.WriteFile(r.breakerFile(), []byte(reason+"\n"), 0644)
	r.escalate("GATE_BREAKER_OPEN", map[string]any{"reason": reason, "reset": "rm " + r.breakerFile()})
	r.logf("!! Circuit-Breaker OFFEN: %s — manueller Reset: rm %s", reason, r.breakerFile())
}

// escalate schreibt ein durables Artefakt (D20c). Poka-yoke: absoluter
// eventsDir; der Reaktor macht den Kanal nicht selbst zum Rätsel.
func (r *reactor) escalate(kind string, detail map[string]any) {
	if r.eventsDir == "" {
		return
	}
	_ = os.MkdirAll(r.eventsDir, 0755)
	detail["kind"] = kind
	detail["at"] = time.Now().Format(time.RFC3339)
	detail["actor"] = r.owner
	b, _ := json.MarshalIndent(detail, "", "  ")
	name := fmt.Sprintf("%s-%d.json", kind, time.Now().UnixNano())
	_ = os.WriteFile(filepath.Join(r.eventsDir, name), b, 0644)
}

// journeyShadow fährt den Journey-Smoke im SHADOW-MODE (PRD ui-journey-testing
// WP3/D14): report-only NEBEN der bereits gefällten HTTP/CLI-Entscheidung. Er
// gibt NICHTS zurück und wird NACH dem Terminal-Übergang aufgerufen — der
// Journey-Ausgang kann den Deploy-Fluss strukturell nicht mehr berühren. Jeder
// Journey-Fehler (auch exit 3 / Timeout / panic) wird NUR geloggt + als JSONL-
// Zeile persistiert. „Shadow heißt Shadow" (harte Regel des Auftrags).
//
// Ref-Pinning (D13): derselbe Ref wie das deployte Artefakt (o.GitSha). Beim
// MK-Deploy-Mirror ist das ein /opt/stack-SHA, der im Journey-SoT /opt/master-
// kanban NICHT existiert → journey-run meldet Harness (exit 3), die Zeile
// trägt verdict=harness. Das blockt nie und ist im Shadow-Fenster ehrlich.
// ponytail: echtes grün/rot-Signal im Fenster braucht die Outbox→SoT-SHA-
// Mapping-Regel (D13) — Upgrade-Pfad vor dem Rollback-Arming, nicht heute.
func (r *reactor) journeyShadow(o outboxRow, httpVerdict string) {
	defer func() {
		if p := recover(); p != nil {
			r.logf("~ %s: journey-shadow panic geschluckt (Deploy unberührt): %v", o.Service, p)
		}
	}()
	runner := r.journeyRun
	if runner == "" {
		runner = "/opt/solartown/bin/journey-run"
	}
	to := r.journeyShadowTO
	if to <= 0 {
		to = 180 * time.Second
	}
	tag := o.GitSha
	if len(tag) > 7 {
		tag = tag[:7]
	}
	// Eigener Timeout-Kontext (NICHT der Deploy-Kontext): der Shadow verlängert
	// den Deploy nicht und der Deploy kappt den Shadow nicht mitten im Lauf.
	jctx, cancel := context.WithTimeout(context.Background(), to)
	defer cancel()
	// CombinedOutput verworfen: der Wrapper schreibt sein eigenes Result-JSON +
	// Trace-Artefakt (D3/D8); für die Shadow-Zeile zählt nur der Exit-Code.
	_, err := exec.CommandContext(jctx, runner, o.Service, o.Environment,
		"--context", "smoke", "--ref", o.GitSha).CombinedOutput()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			exit = -1 // konnte nicht starten / Timeout-Kill → Harness, nie „rot"
		}
	}
	verdict := journeyVerdict(exit)
	r.appendShadowLine(o.Service, o.GitSha, httpVerdict, exit, verdict)
	r.logf("~ %s@%s: journey-shadow exit=%d verdict=%s (report-only; HTTP entschied %s)",
		o.Service, tag, exit, verdict, httpVerdict)
	if verdict == "red" && httpVerdict == "green" {
		// Genau die teuerste Falsch-Rot-Divergenz, deren Rate das Kalibrier-
		// fenster (D14) auf 0 messen muss, BEVOR scharfgeschaltet wird. Nur
		// hervorgehoben geloggt — kein Rollback, kein Reset (Shadow-Mode).
		r.logf("~ %s@%s: SHADOW-DIVERGENZ journey=red / http=green (Kalibrier-Signal, kein Rollback)", o.Service, tag)
	}
}

// appendShadowLine hängt eine maschinenlesbare Shadow-Zeile an
// <journeyShadowDir>/<service>.jsonl — die Rohdaten des Kalibrierfensters
// (D14): {ts, sha, http_verdict, journey_exit, journey_verdict}. Best-effort;
// ein Schreibfehler wird geloggt, darf den Drain aber nicht stören.
func (r *reactor) appendShadowLine(service, sha, httpVerdict string, exit int, verdict string) {
	dir := r.journeyShadowDir
	if dir == "" {
		dir = "/var/lib/journey-runner/shadow"
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		r.logf("~ %s: shadow-dir %s nicht anlegbar: %v", service, dir, err)
		return
	}
	line, _ := json.Marshal(map[string]any{
		"ts":              time.Now().Format(time.RFC3339),
		"sha":             sha,
		"http_verdict":    httpVerdict,
		"journey_exit":    exit,
		"journey_verdict": verdict,
	})
	f, err := os.OpenFile(filepath.Join(dir, service+".jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		r.logf("~ %s: shadow-jsonl nicht schreibbar: %v", service, err)
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(line, '\n'))
}

// runOnce: ein beaufsichtigter Drain (D8 single-shot). Gestrandete deploying-
// Zeilen zurückholen (D11), dann jede pending-Zeile abarbeiten.
func (r *reactor) runOnce(ctx context.Context) error {
	r.reclaimStranded(ctx)

	if r.breakerIsOpen() {
		r.logf("Circuit-Breaker offen — Drain pausiert (Reset: rm %s)", r.breakerFile())
		return nil
	}

	// environment-Filter (sa-deploy-stufen W2): leer = alle Stufen (rückwärts-
	// kompatibel, heutiges Verhalten); gesetzt = NUR diese Stufe. So teilen sich
	// der prod-Reaktor (environment='prod-mvp') und der sa-staging-drain
	// (environment='staging') EINE portfolio.deployments-Tabelle, ohne sich
	// gegenseitig fremde Zeilen wegzuschnappen (sonst errored jeder die Rezepte
	// des anderen — genau die Kollision aus dem W1-Befund).
	rows, err := r.pool.Query(ctx, `SELECT id, service, environment, git_sha, COALESCE(version,''), status
	     FROM portfolio.deployments
	     WHERE status='pending' AND ($1='' OR environment=$1) ORDER BY deployed_at`, r.environment)
	if err != nil {
		return fmt.Errorf("pending-Zeilen lesen: %w", err)
	}
	var pend []outboxRow
	for rows.Next() {
		var o outboxRow
		if err := rows.Scan(&o.ID, &o.Service, &o.Environment, &o.GitSha, &o.Version, &o.Status); err != nil {
			rows.Close()
			return err
		}
		pend = append(pend, o)
	}
	rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}

	consecReds := 0
	for _, o := range pend {
		// Stufen-Scope-Gürtel (sa-deploy-stufen W2): der SQL-WHERE oben schließt
		// Fremd-Stufen bereits aus; dieser Guard ist die Poka-yoke-Redundanz, damit
		// ein gescopter Reaktor NIE eine Zeile einer fremden Stufe anfasst (s.
		// envEligible — behebt den Kollisions-Befund strukturell, nicht nur per Query).
		if !envEligible(r.environment, o.Environment) {
			continue
		}
		switch decideClaim(o.Status, r.breakerIsOpen()) {
		case actSkipBreaker:
			r.logf("· %s@%s [%s] Breaker offen — übersprungen", o.Service, o.Environment, o.GitSha[:min(7, len(o.GitSha))])
			return nil
		case actSkipNotPending:
			continue
		}
		red := r.processOne(ctx, o)
		if red {
			consecReds++
			if breakerOpens(consecReds, r.breakerK) {
				r.openBreaker(fmt.Sprintf("%d rote Deploys in Folge", consecReds))
				return nil
			}
		} else {
			consecReds = 0
		}
	}
	return nil
}

// reclaimStranded holt deploying-Zeilen zurück, deren Lease abgelaufen ist
// (Reaktor-Instanz weg während deploying, D11-Unstick) → wieder pending.
func (r *reactor) reclaimStranded(ctx context.Context) {
	tag, err := r.pool.Exec(ctx, `UPDATE portfolio.deployments
	     SET status='pending', owned_by=NULL, owned_until=NULL
	     WHERE status='deploying' AND owned_until IS NOT NULL AND owned_until < now()
	       AND ($1='' OR environment=$1)`, r.environment)
	if err == nil && tag.RowsAffected() > 0 {
		r.logf("↺ %d gestrandete deploying-Zeile(n) zurück auf pending (Lease abgelaufen)", tag.RowsAffected())
	}
}

// processOne fährt eine pending-Zeile durch claim→deploy→smoke→(live|rollback).
// Rückgabe: true = rotes Ergebnis (Breaker-relevant).
func (r *reactor) processOne(ctx context.Context, o outboxRow) bool {
	tag := o.GitSha
	if len(tag) > 7 {
		tag = tag[:7]
	}
	rec, ok := r.man.Services[o.Service]
	if !ok {
		// Rezept-Miss vor Claim: keine Lease gesetzt, direkter Übergang.
		_, _ = r.pool.Exec(ctx, `UPDATE portfolio.deployments SET status='errored'
		     WHERE id=$1 AND status='pending'`, o.ID)
		r.escalate("MR_DEPLOY_NO_RECIPE", map[string]any{"service": o.Service, "git_sha": o.GitSha})
		r.logf("x %s: kein Rezept im Manifest — errored + eskaliert", o.Service)
		return true
	}

	prevSha, prevVer := r.prevLive(ctx, o.Service, o.Environment)

	// Claim (CAS pending→deploying + Lease + Deploy-Felder aus dem Rezept).
	until := time.Now().Add(r.lease)
	ct, err := r.pool.Exec(ctx, `UPDATE portfolio.deployments
	     SET status='deploying', owned_by=$2, owned_until=$3, version=$4,
	         probe_kind=$5, health_url=$6, deploy_method=$8,
	         deployed_by=$2, prev_version=NULLIF($7,'')
	     WHERE id=$1 AND status='pending'`,
		o.ID, r.owner, until, o.GitSha, rec.ProbeKind, rec.HealthURL, prevVer, deployMethod(rec))
	if err != nil || ct.RowsAffected() != 1 {
		r.logf("· %s@%s: Claim übersprungen (schon übernommen?) %v", o.Service, tag, err)
		return false
	}
	r.logf("→ %s@%s claim → deploying (prev=%s)", o.Service, tag, shortOr(prevSha, "—"))

	// Deploy (SHA-gepinnt via deploy-gt.sh). Nicht-Null = Mechanik-Miss → wie rot.
	if out, err := r.deploy(ctx, rec, o.Service, o.GitSha); err != nil {
		r.logf("x %s@%s: deploy-gt.sh missglückte: %v\n%s", o.Service, tag, err, out)
		return r.rollback(ctx, o, rec, prevSha, "deploy-gt.sh-Miss")
	}

	// Smoke (D18) mit Readiness-Backoff. Die HTTP/CLI-Probe entscheidet ALLEIN
	// über live|rollback — der Journey-Shadow unten ändert daran NICHTS (D14).
	green := r.smokeGreen(ctx, rec, o.GitSha)
	var red bool
	if green {
		if r.commit(ctx, o.ID, "live") {
			r.logf("ok %s@%s: Smoke grün → live", o.Service, tag)
		}
		red = false
	} else {
		r.logf("! %s@%s: Smoke rot → Rollback", o.Service, tag)
		red = r.rollback(ctx, o, rec, prevSha, "Smoke rot")
	}

	// Journey-Shadow (WP3/D14): report-only, NACH dem Terminal-Übergang. Strukturell
	// nicht mehr in der Lage, die schon gefällte HTTP-Entscheidung zu beeinflussen.
	if rec.JourneyShadow {
		httpVerdict := "green"
		if !green {
			httpVerdict = "rollback"
		}
		r.journeyShadow(o, httpVerdict)
	}
	return red
}

// rollback: SHA-gepinnt auf prev bauen, Zeile → rolled_back (quarantänisiert
// die Gift-SHA, D15), eskalieren. Ohne prev → errored, kein So-tun-als-ob.
func (r *reactor) rollback(ctx context.Context, o outboxRow, rec ServiceRecipe, prevSha, why string) bool {
	if decideRollback(prevSha) == rbErroredNoPrev {
		r.commit(ctx, o.ID, "errored")
		r.escalate("MR_DEPLOY_ERRORED", map[string]any{"service": o.Service, "git_sha": o.GitSha, "why": why, "note": "kein prev — Rollback nicht möglich"})
		r.logf("x %s: %s, kein prev → errored + eskaliert", o.Service, why)
		return true
	}
	if out, err := r.deploy(ctx, rec, o.Service, prevSha); err != nil {
		r.logf("x %s: Rollback-Build auf %s missglückte: %v\n%s", o.Service, shortOr(prevSha, "?"), err, out)
	}
	// rolled_back wird unbedingt geschrieben (quarantänisiert die Gift-SHA);
	// die prev-Smoke bestätigt best-effort und gatet den Übergang nicht.
	r.commit(ctx, o.ID, "rolled_back")
	r.escalate("MR_DEPLOY_ROLLED_BACK", map[string]any{
		"service": o.Service, "git_sha": o.GitSha, "rolled_back_to": prevSha, "why": why,
		"quarantined": true, "note": "Gift-SHA quarantänisiert — kein Auto-Re-Deploy bis Human-Clear",
	})
	r.logf("↩ %s: rolled_back auf %s, SHA quarantänisiert, eskaliert", o.Service, shortOr(prevSha, "?"))
	return true
}

// deployInvocation baut (script, args) für einen Deploy — pure Funktion, damit
// die Rezept-Typ-Weiche (go|node) testbar ist, ohne ein Skript auszuführen.
// go: deploy-gt.sh --bin <binary>. node: deploy-node.sh --dest <deploy-wurzel>
// (+ optional --build-cmd). --unit/--box werden für beide gleich angehängt.
func deployInvocation(rec ServiceRecipe, gtScript, nodeScript, repo, service, ref string) (string, []string) {
	var script string
	var args []string
	if recipeType(rec) == "node" {
		script = nodeScript
		args = []string{"--ref", ref, "--service", service, "--src", rec.Src,
			"--dest", rec.Bin, "--repo", repo, "--json"}
		if rec.BuildCmd != "" {
			args = append(args, "--build-cmd", rec.BuildCmd)
		}
	} else {
		script = gtScript
		args = []string{"--ref", ref, "--service", service, "--src", rec.Src,
			"--bin", rec.Bin, "--repo", repo, "--json"}
	}
	if rec.Unit != "" {
		args = append(args, "--unit", rec.Unit)
	}
	// Remote-Deploy (sa-deploy-stufen W2): baut lokal SHA-gepinnt und
	// shippt/swappt/restartet per ssh auf die Ziel-Box. Leer = lokal.
	if rec.Box != "" {
		args = append(args, "--box", rec.Box)
	}
	return script, args
}

// deploy ruft SHA-gepinnt die passenden „Hände": deploy-gt.sh (Go-Binary-Swap)
// oder deploy-node.sh (Bundle-Deploy, sa-deploy-stufen W4). --unit leer =
// cli-Service. Der Rollback-Pfad (rec + prevSha) landet ebenfalls hier — für
// node baut deploy-node.sh dann NICHT neu, sondern flippt das schon vorhandene
// prev-Release (Marker .deploy-ok), also derselbe SHA-gepinnte Rückweg.
func (r *reactor) deploy(ctx context.Context, rec ServiceRecipe, service, ref string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.smokeDue+2*time.Minute)
	defer cancel()
	script, args := deployInvocation(rec, r.scriptPath, r.nodeScript, r.man.Repo, service, ref)
	out, err := exec.CommandContext(ctx, script, args...).CombinedOutput()
	return string(out), err
}

// smokeGreen sondiert bis grün oder Frist/Retries erschöpft. wantSha ist die
// eben deployte SHA (Erwartung für shaMatch). SMOKE_FORCE_RED=1 injiziert
// deterministisch Rot (Testpfad/Game-Day).
func (r *reactor) smokeGreen(ctx context.Context, rec ServiceRecipe, wantSha string) bool {
	forceRed := os.Getenv("SMOKE_FORCE_RED") == "1"
	due := time.Now().Add(r.smokeDue)
	hurl := rec.HealthURL
	head := deploymentHead{ProbeKind: rec.ProbeKind, HealthURL: &hurl, GitSha: wantSha}
	for attempt := 1; ; attempt++ {
		var reached, shaOK bool
		if !forceRed {
			pr := probeRow(ctx, head, r.probeTO)
			reached = pr.Reached
			shaOK = pr.Reached && shaMatch(head, pr) && r.businessOK(ctx, rec)
		}
		switch decideSmoke(reached, shaOK, forceRed, attempt, r.maxSmoke, time.Now().After(due)) {
		case smokeGreen:
			return true
		case smokeRollback:
			return false
		}
		time.Sleep(r.smokeSleep) // Readiness-Backoff: Connection-Refused im Restart-Fenster ≠ rot
	}
}

// businessOK prüft eine echte Business-Route (D18) — nur http + gesetzte URL;
// cli-Services zertifiziert `version --json` bereits (probeRow).
func (r *reactor) businessOK(ctx context.Context, rec ServiceRecipe) bool {
	if rec.ProbeKind != "http" || rec.SmokeURL == "" {
		return true
	}
	return httpOK(ctx, rec.SmokeURL, r.probeTO)
}

// httpOK: GET, erwartet 2xx (Business-Route lebt, D18). Kein Vertrag-Parsing —
// die Route darf beliebiges JSON liefern; es zählt, dass sie 2xx antwortet.
func httpOK(ctx context.Context, url string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// commit schreibt einen geschützten Terminal-Übergang deploying→(to) und gibt
// die Lease frei. Owner-CAS: nur wenn WIR die Zeile noch halten — kein Race mit
// dem Reconciler (D13) oder einem anderen Reaktor.
func (r *reactor) commit(ctx context.Context, id int64, to string) bool {
	ct, err := r.pool.Exec(ctx, `UPDATE portfolio.deployments
	     SET status=$2, owned_by=NULL, owned_until=NULL
	     WHERE id=$1 AND status='deploying' AND owned_by=$3`,
		id, to, r.owner)
	if err != nil {
		r.logf("x Übergang deploying→%s (#%d): %v", to, id, err)
		return false
	}
	return ct.RowsAffected() == 1
}

func (r *reactor) prevLive(ctx context.Context, service, env string) (sha, ver string) {
	_ = r.pool.QueryRow(ctx, `SELECT git_sha, COALESCE(version,'') FROM portfolio.deployments
	     WHERE service=$1 AND environment=$2 AND status='live'
	     ORDER BY deployed_at DESC LIMIT 1`, service, env).Scan(&sha, &ver)
	return sha, ver
}

func shortOr(s, def string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// defaultScriptPath sucht deploy-gt.sh neben dem Binary bzw. im Repo.
func defaultScriptPath() string {
	if p := envOr("DEPLOY_GT_SH", ""); p != "" {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "deploy-gt.sh")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return "/opt/stack/tools/portfolio/deploy-gt.sh"
}

// defaultNodeScriptPath sucht deploy-node.sh neben dem Binary bzw. im Repo
// (sa-deploy-stufen W4, analog zu defaultScriptPath).
func defaultNodeScriptPath() string {
	if p := envOr("DEPLOY_NODE_SH", ""); p != "" {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "deploy-node.sh")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return "/opt/stack/tools/portfolio/deploy-node.sh"
}

// cmdDeployReactorOutbox — der Outbox-konsumierende Deploy-Reaktor (WP5).
// --once ist der beaufsichtigte Einzel-Drain (D8, MVP-Beweispfad). --loop ist
// das kontinuierliche Re-Arm (WP7) — die zugehörige Unit bleibt disabled, bis
// HAUPT sie hinter dem Test-Gate scharfschaltet.
func cmdDeployReactorOutbox() *cobra.Command {
	var manifestPath, scriptPath, nodeScript, stateDir, eventsDir, environment string
	var journeyRun, journeyShadowDir string
	var once, loop bool
	var interval, lease, smokeDue, probeTO, smokeSleep, journeyShadowTO time.Duration
	var maxSmoke, breakerK int

	c := &cobra.Command{
		Use:   "deploy-reactor-outbox",
		Short: "Konsumiert MR_MERGED_GREEN (Outbox) und deployt SHA-gepinnt in-place (WP5)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if once == loop { // beide false ODER beide true
				return fmt.Errorf("wähle genau eines: --once (ein Drain, D8) oder --loop (WP7)")
			}
			man, err := loadReactorManifest(manifestPath)
			if err != nil {
				return err
			}
			host, _ := os.Hostname()
			owner := "deploy-reactor@" + host
			if environment != "" {
				// Eigener owned_by je Stufe → im Ledger sofort sichtbar, welcher
				// Drain die Lease hält, und keine CAS-Verwechslung zwischen den
				// beiden Reaktoren.
				owner = "deploy-reactor-" + environment + "@" + host
			}
			r := &reactor{
				pool: connect(), man: man,
				scriptPath: scriptPath, nodeScript: nodeScript, owner: owner, environment: environment,
				lease: lease, maxSmoke: maxSmoke, smokeSleep: smokeSleep,
				smokeDue: smokeDue, probeTO: probeTO, breakerK: breakerK,
				stateDir: stateDir, eventsDir: eventsDir,
				journeyRun: journeyRun, journeyShadowDir: journeyShadowDir, journeyShadowTO: journeyShadowTO,
				logf: func(f string, a ...any) { fmt.Printf(f+"\n", a...) },
			}
			if loop {
				// WP7: Ticker-Drain (pg_notify wäre nur Wakeup-Hint, D10 — die
				// Tabelle ist Wahrheit). ponytail: LISTEN mr_merged_green für
				// sofortiges Aufwachen ist die WP7-Härtung; Ceiling: bis dahin
				// Latenz = interval. Unit bleibt bis dahin disabled.
				t := time.NewTicker(interval)
				defer t.Stop()
				for {
					if err := r.runOnce(cmd.Context()); err != nil {
						r.logf("x Drain-Runde: %v", err)
					}
					select {
					case <-cmd.Context().Done():
						return nil
					case <-t.C:
					}
				}
			}
			return r.runOnce(cmd.Context())
		},
	}
	c.Flags().StringVar(&manifestPath, "manifest", envOr("DEPLOY_REACTOR_MANIFEST", "/opt/stack/deploy-reactor-manifest.yaml"), "Service-Rezepte (YAML)")
	c.Flags().StringVar(&environment, "environment", envOr("DEPLOY_REACTOR_ENV", ""), "nur diese Ledger-Stufe draina (leer=alle; sa-deploy-stufen W2: 'staging' bzw. 'prod-mvp')")
	c.Flags().StringVar(&scriptPath, "script", defaultScriptPath(), "Pfad zu deploy-gt.sh (Go-Binary-Swap)")
	c.Flags().StringVar(&nodeScript, "node-script", defaultNodeScriptPath(), "Pfad zu deploy-node.sh (Bundle-Deploy, sa-deploy-stufen W4)")
	c.Flags().StringVar(&stateDir, "state-dir", envOr("DEPLOY_STATE_DIR", "/opt/stack/var/deploy-reactor"), "Circuit-Breaker-Marker")
	c.Flags().StringVar(&eventsDir, "events-dir", envOr("DEPLOY_EVENTS_DIR", "/opt/solartown/events/refinery"), "durable Eskalations-Artefakte (D20c)")
	c.Flags().BoolVar(&once, "once", false, "ein beaufsichtigter Drain, dann Ende (D8)")
	c.Flags().BoolVar(&loop, "loop", false, "kontinuierlich (WP7; Unit bleibt disabled)")
	c.Flags().DurationVar(&interval, "interval", 30*time.Second, "Drain-Takt im --loop")
	c.Flags().DurationVar(&lease, "lease", 10*time.Minute, "Lease-Dauer owned_until (> Smoke-Fenster, D13)")
	c.Flags().DurationVar(&smokeDue, "smoke-due", 90*time.Second, "Gesamt-Frist je Deploy bis Rollback (D17)")
	c.Flags().DurationVar(&probeTO, "probe-timeout", 5*time.Second, "Timeout je Smoke-Sonde (D17)")
	c.Flags().DurationVar(&smokeSleep, "smoke-sleep", time.Second, "Pause zwischen Smoke-Versuchen (Readiness-Backoff)")
	c.Flags().IntVar(&maxSmoke, "max-smoke", 15, "Smoke-Versuche vor Rollback")
	c.Flags().IntVar(&breakerK, "breaker-k", 3, "rote Deploys in Folge bis Circuit-Breaker öffnet (D15)")
	// Journey-Shadow (WP3/D14): report-only, entscheidet NIE über live|rollback.
	// Default-Pfade so gewählt, dass die bestehende .service-ExecStart (kein Unit-
	// Umbau) den Shadow ohne zusätzliche Flags mitnimmt, sobald ein Service im
	// Manifest journey_shadow: true trägt.
	c.Flags().StringVar(&journeyRun, "journey-run", envOr("JOURNEY_RUN", "/opt/solartown/bin/journey-run"), "Pfad zum journey-run-Wrapper (Journey-Shadow, D14)")
	c.Flags().StringVar(&journeyShadowDir, "journey-shadow-dir", envOr("JOURNEY_SHADOW_DIR", "/var/lib/journey-runner/shadow"), "Ablage der Shadow-JSONL je Service (D14)")
	c.Flags().DurationVar(&journeyShadowTO, "journey-shadow-timeout", 180*time.Second, "harter, deploy-unabhängiger Timeout je Journey-Shadow-Lauf (D14; report-only)")
	return c
}
