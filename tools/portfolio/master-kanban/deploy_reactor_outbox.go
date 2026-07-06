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
	Src       string `yaml:"src"`        // Go-Paket-Relpfad im Repo (deploy-gt.sh --src)
	Bin       string `yaml:"bin"`        // Ziel-Binary, absoluter Pfad (--bin)
	Unit      string `yaml:"unit"`       // systemd-Unit; leer = cli-Service ohne Restart
	ProbeKind string `yaml:"probe_kind"` // 'http' | 'cli' (D18)
	HealthURL string `yaml:"health_url"` // http: /version-URL · cli: absoluter Binary-Pfad
	SmokeURL  string `yaml:"smoke_url"`  // http: eine echte Business-Route (2xx erwartet, D18)
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
	actClaim         claimAction = iota // pending, Breaker zu → Deploy starten
	actSkipBreaker                      // Breaker offen → nichts anfassen (D15)
	actSkipNotPending                   // Zeile nicht mehr pending (Doppel-Zustellung, D11)
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

// ── Reaktor (Orchestrierung; injizierbare Kanten für Tests) ──────────────────

type reactor struct {
	pool       *pgxpool.Pool
	man        *reactorManifest
	scriptPath string
	owner      string
	lease      time.Duration
	maxSmoke   int
	smokeSleep time.Duration
	smokeDue   time.Duration
	probeTO    time.Duration
	breakerK   int
	stateDir   string // Breaker-Marker
	eventsDir  string // durable Eskalations-Artefakte (D20c)
	logf       func(string, ...any)
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

// runOnce: ein beaufsichtigter Drain (D8 single-shot). Gestrandete deploying-
// Zeilen zurückholen (D11), dann jede pending-Zeile abarbeiten.
func (r *reactor) runOnce(ctx context.Context) error {
	r.reclaimStranded(ctx)

	if r.breakerIsOpen() {
		r.logf("Circuit-Breaker offen — Drain pausiert (Reset: rm %s)", r.breakerFile())
		return nil
	}

	rows, err := r.pool.Query(ctx, `SELECT id, service, environment, git_sha, COALESCE(version,''), status
	     FROM portfolio.deployments WHERE status='pending' ORDER BY deployed_at`)
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
	     WHERE status='deploying' AND owned_until IS NOT NULL AND owned_until < now()`)
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
	         probe_kind=$5, health_url=$6, deploy_method='deploy-gt',
	         deployed_by=$2, prev_version=NULLIF($7,'')
	     WHERE id=$1 AND status='pending'`,
		o.ID, r.owner, until, o.GitSha, rec.ProbeKind, rec.HealthURL, prevVer)
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

	// Smoke (D18) mit Readiness-Backoff.
	if r.smokeGreen(ctx, rec, o.GitSha) {
		if r.commit(ctx, o.ID, "live") {
			r.logf("ok %s@%s: Smoke grün → live", o.Service, tag)
		}
		return false
	}
	r.logf("! %s@%s: Smoke rot → Rollback", o.Service, tag)
	return r.rollback(ctx, o, rec, prevSha, "Smoke rot")
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

// deploy ruft ../deploy-gt.sh SHA-gepinnt. --unit leer = cli-Service.
func (r *reactor) deploy(ctx context.Context, rec ServiceRecipe, service, ref string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.smokeDue+2*time.Minute)
	defer cancel()
	args := []string{"--ref", ref, "--service", service, "--src", rec.Src,
		"--bin", rec.Bin, "--repo", r.man.Repo, "--json"}
	if rec.Unit != "" {
		args = append(args, "--unit", rec.Unit)
	}
	out, err := exec.CommandContext(ctx, r.scriptPath, args...).CombinedOutput()
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

// cmdDeployReactorOutbox — der Outbox-konsumierende Deploy-Reaktor (WP5).
// --once ist der beaufsichtigte Einzel-Drain (D8, MVP-Beweispfad). --loop ist
// das kontinuierliche Re-Arm (WP7) — die zugehörige Unit bleibt disabled, bis
// HAUPT sie hinter dem Test-Gate scharfschaltet.
func cmdDeployReactorOutbox() *cobra.Command {
	var manifestPath, scriptPath, stateDir, eventsDir string
	var once, loop bool
	var interval, lease, smokeDue, probeTO, smokeSleep time.Duration
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
			r := &reactor{
				pool: connect(), man: man,
				scriptPath: scriptPath, owner: "deploy-reactor@" + host,
				lease: lease, maxSmoke: maxSmoke, smokeSleep: smokeSleep,
				smokeDue: smokeDue, probeTO: probeTO, breakerK: breakerK,
				stateDir: stateDir, eventsDir: eventsDir,
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
	c.Flags().StringVar(&scriptPath, "script", defaultScriptPath(), "Pfad zu deploy-gt.sh")
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
	return c
}
