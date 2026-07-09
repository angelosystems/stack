package main

// promote.go — `deploy promote <app>`: das Promotion-Gate der SA-Deploy-Stufen
// (PRD sa-deploy-stufen W3). Merge landet automatisch auf Staging (W2); nach
// Prod kommt AUSSCHLIESSLICH, was durch dieses Gate geht.
//
// Zwei harte Vorbedingungen VOR jedem Prod-Deploy (beide werden geprüft, BEVOR
// deploy-gt.sh überhaupt läuft — „kein Deploy ohne grün"):
//   (a) Grüner letzter Staging-Smoke für EXAKT diese SHA. Wahrheitsquelle ist
//       der Release-Ledger portfolio.deployments (:5434): die jüngste Zeile
//       service=<app> environment='staging' muss status='live' sein. Die dort
//       stehende git_sha ist das Promotion-Ziel (nicht HEAD, nicht ein
//       Argument — die Stufe, die auf Staging bewiesen wurde).
//   (b) Erteiltes, frisches Approval für (app, sha) — s. approval.go. Das
//       promote-Kommando SENDET nichts nach extern; es liest nur, ob die
//       Freigabe existiert. Das Erinnern läuft über die bestehende
//       WA-Approval-Schiene (Poll), nicht über diesen Pfad.
//
// Erst wenn beide grün sind, fährt das Kommando denselben bewiesenen
// Prod-Deploy wie der Reaktor (../portfolio/deploy-gt.sh --box: SHA-gepinnter
// Remote-Build + atomarer Swap + Restart), smoked Prod und schreibt die
// Ledger-Zeile environment='prod' source='promote'.
//
// Trading-Wall (Mario-Go 2026-07-07/08): identisches Muster wie deploy-gt.sh —
// die Factory darf Live-Geld-Einheiten NIE bauen/swappen/restarten, auch nicht
// über den promote-Pfad und auch nicht über --box. Verstoß = exit 64, kein
// Override. Doppelte Absicherung: promote prüft die Wall SELBST (früher,
// handlungsleitender Fehler) UND deploy-gt.sh prüft sie erneut.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Exit-Codes (Poka-yoke, deckungsgleich mit deploy-gt.sh wo möglich). Jede
// Verweigerung hat einen EIGENEN Code, damit ein Aufrufer/Test „warum" ohne
// Log-Parsing unterscheiden kann (der Verweigerungs-Beweis prüft exit 66).
const (
	exOK           = 0
	exUsage        = 64 // Aufruf-/Wall-/Config-Fehler (wie deploy-gt.sh die64)
	exNoStaging    = 65 // Gate (a): kein grüner Staging-Smoke für die SHA
	exNoApproval   = 66 // Gate (b): kein frisches Approval → kein Deploy
	exUnavailable  = 69 // Ledger/DB nicht erreichbar (EX_UNAVAILABLE)
	exDeployFailed = 70 // deploy-gt.sh Build/Ship/Swap-Miss
	exSmokeFailed  = 75 // Prod-Smoke rot (Rollback versucht)
)

// promoteRecipe sagt dem Gate, WOHIN und WIE ein Staging-bewiesener Service auf
// Prod geht. Bewusst analog zur ServiceRecipe des Reaktors, aber prod-spezifisch
// (eigene Box, eigene Unit, eigener localhost-only Port).
type promoteRecipe struct {
	Src   string `yaml:"src"`   // Go-Paket-Relpfad im Repo (deploy-gt.sh --src)
	Bin   string `yaml:"bin"`   // Ziel-Binary auf der Prod-Box (absoluter Pfad)
	Unit  string `yaml:"unit"`  // systemd-Unit auf Prod; leer = kein Restart
	Box   string `yaml:"box"`   // Box-NAME aus deploy.json (wird zu ssh-Host aufgelöst)
	Smoke string `yaml:"smoke"` // Prod-Smoke: absoluter Pfad eines cli-Wrappers, der den /version-Vertrag der laufenden Prod-Unit ausgibt
}

type promoteManifest struct {
	Repo     string                   `yaml:"repo"`     // git-Wurzel mit der Merge-SHA (Bare-Mirror)
	Services map[string]promoteRecipe `yaml:"services"` // app → Rezept
}

func loadPromoteManifest(path string) (*promoteManifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("Promote-Manifest %s nicht lesbar: %w (Pfad via --manifest oder DEPLOY_PROMOTE_MANIFEST setzen)", path, err)
	}
	var m promoteManifest
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("Promote-Manifest %s nicht parsebar: %w", path, err)
	}
	if m.Repo == "" {
		return nil, fmt.Errorf("Promote-Manifest %s: 'repo:' fehlt (git-Wurzel mit der Merge-SHA, z.B. der Bare-Mirror)", path)
	}
	return &m, nil
}

// liveMoneyPatterns — dieselben breiten Muster wie deploy-gt.sh. Kein
// Override-Flag: Aufweichen heißt, diese Zeile sehenden Auges zu editieren.
var liveMoneyPatterns = []string{"quantbot", "supervisor", "strategies", "dublin", "live-trad"}

// tradingWallViolation liefert (Feld, Muster), falls irgendein Feld ein
// Live-Geld-Muster trägt — sonst ("",""). Pure Funktion (WP-testbar).
func tradingWallViolation(fields map[string]string) (field, pat string) {
	for name, val := range fields {
		low := strings.ToLower(val)
		for _, p := range liveMoneyPatterns {
			if strings.Contains(low, p) {
				return name, p
			}
		}
	}
	return "", ""
}

// stagingHead ist die jüngste Ledger-Zeile einer Stufe.
type stagingHead struct {
	Sha     string
	Version string
	Status  string
	Found   bool
}

// latestStagingHead liest die jüngste Staging-Zeile für <app>. Parametrisiert
// (pgx) → injektionssicher; der app-Name kommt vom Aufrufer/CLI.
func latestStagingHead(ctx context.Context, dsn, app string) (stagingHead, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return stagingHead{}, fmt.Errorf("Ledger nicht erreichbar (%s): %w", dsn, err)
	}
	defer conn.Close(ctx)
	var h stagingHead
	err = conn.QueryRow(ctx, `SELECT git_sha, COALESCE(version,''), status
	     FROM portfolio.deployments
	     WHERE service=$1 AND environment='staging'
	     ORDER BY deployed_at DESC LIMIT 1`, app).Scan(&h.Sha, &h.Version, &h.Status)
	if err == pgx.ErrNoRows {
		return stagingHead{Found: false}, nil
	}
	if err != nil {
		return stagingHead{}, fmt.Errorf("Staging-Head lesen: %w", err)
	}
	h.Found = true
	return h, nil
}

// prevProdVersion liest die aktuell live Prod-Version als Rollback-Anker.
func prevProdVersion(ctx context.Context, dsn, app string) string {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return ""
	}
	defer conn.Close(ctx)
	var v string
	_ = conn.QueryRow(ctx, `SELECT COALESCE(version,'') FROM portfolio.deployments
	     WHERE service=$1 AND environment='prod' AND status='live'
	     ORDER BY deployed_at DESC LIMIT 1`, app).Scan(&v)
	return v
}

// recordProdLive schreibt (Upsert) die Prod-Zeile env='prod' source='promote'
// status='live'. Upsert auf (service,environment,git_sha) = Idempotenz (D11);
// eine quarantänisierte Zeile (rolled_back) wird NIE überschrieben.
func recordProdLive(ctx context.Context, dsn, app, sha, version, smokePath, prevVersion, actor string) (int64, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return 0, fmt.Errorf("Ledger nicht erreichbar: %w", err)
	}
	defer conn.Close(ctx)
	var id int64
	err = conn.QueryRow(ctx, `INSERT INTO portfolio.deployments
	     (service, probe_kind, environment, version, git_sha, status,
	      deployed_by, deploy_method, source, health_url, prev_version)
	   VALUES ($1,'cli','prod',$2,$3,'live',$4,'deploy-gt-promote','promote',$5,NULLIF($6,''))
	   ON CONFLICT (service, environment, git_sha) DO UPDATE
	     SET status='live', deployed_at=now(), version=EXCLUDED.version,
	         deployed_by=EXCLUDED.deployed_by, deploy_method=EXCLUDED.deploy_method,
	         source=EXCLUDED.source, health_url=EXCLUDED.health_url,
	         prev_version=EXCLUDED.prev_version
	     WHERE portfolio.deployments.status <> 'rolled_back'
	   RETURNING id`,
		app, version, sha, actor, smokePath, prevVersion).Scan(&id)
	if err == pgx.ErrNoRows {
		return 0, fmt.Errorf("%s@prod %s ist quarantänisiert (status=rolled_back) — bewusst freigeben, dann erneut promoten", app, sha)
	}
	if err != nil {
		return 0, fmt.Errorf("Prod-Ledger-Zeile schreiben: %w", err)
	}
	return id, nil
}

// smokeContract ist der /version-Vertrag, den der Prod-Smoke-Wrapper ausgibt.
type smokeContract struct {
	Service string `json:"service"`
	Version string `json:"version"`
	Sha     string `json:"sha"`
	Env     string `json:"env"`
}

// runProdSmoke ruft den cli-Smoke-Wrapper (der per ssh den /version-Vertrag der
// laufenden Prod-Unit liest) und prüft SHA-Präfix-Match + env='prod'. Rot =
// Fehler. Timeout via ctx.
func runProdSmoke(ctx context.Context, smokePath, wantSha string) (smokeContract, error) {
	out, err := exec.CommandContext(ctx, smokePath).Output()
	if err != nil {
		return smokeContract{}, fmt.Errorf("Smoke-Wrapper %s nicht erreichbar/rot: %w", smokePath, err)
	}
	var c smokeContract
	if err := json.Unmarshal(out, &c); err != nil {
		return smokeContract{}, fmt.Errorf("Smoke-Antwort nicht parsebar (%q): %w", strings.TrimSpace(string(out)), err)
	}
	if !shaPrefixMatch(c.Sha, wantSha) && !shaPrefixMatch(c.Version, wantSha) {
		return c, fmt.Errorf("laufende Prod-Unit meldet sha=%q/version=%q, erwartet %s — Deploy nicht scharf", c.Sha, c.Version, short(wantSha))
	}
	if c.Env != "prod" {
		return c, fmt.Errorf("laufende Prod-Unit meldet env=%q, erwartet 'prod' — falsche Stufe/Config", c.Env)
	}
	return c, nil
}

// shaPrefixMatch — präfix-tolerant (kurze vs. lange SHA), wie der Reaktor-Smoke.
func shaPrefixMatch(a, b string) bool {
	if a == "" || b == "" || a == "unknown" {
		return false
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// fail: handlungsleitender Abbruch mit EXAKTEM Exit-Code (Gate-Disziplin).
func fail(code int, format string, a ...any) {
	fmt.Fprintf(os.Stderr, "\033[1;31mx\033[0m deploy promote: "+format+"\n", a...)
	os.Exit(code)
}

func promoteCmd() *cobra.Command {
	var manifestPath, approvalDir, dsn, deployGt, actor string
	var dryRun bool

	c := &cobra.Command{
		Use:   "promote <app>",
		Short: "Staging-bewiesene SHA nach Prod promoten — nur mit grünem Staging-Smoke UND Approval",
		Long: "Promotion-Gate (sa-deploy-stufen W3). Prüft grünen letzten Staging-Smoke\n" +
			"der SHA (Ledger env=staging status=live) UND ein frisches Approval, dann\n" +
			"Prod-Deploy (deploy-gt.sh --box) + Prod-Smoke + Ledger env=prod source=promote.\n" +
			"Ohne beide Vorbedingungen: harte Verweigerung, kein Deploy.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := args[0]
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()

			// ── Config laden ────────────────────────────────────────────────
			m, err := loadPromoteManifest(manifestPath)
			if err != nil {
				fail(exUsage, "%v", err)
			}
			rec, ok := m.Services[app]
			if !ok {
				known := make([]string, 0, len(m.Services))
				for k := range m.Services {
					known = append(known, k)
				}
				fail(exUsage, "unbekannte App %q im Promote-Manifest — bekannt: %s", app, strings.Join(known, ", "))
			}
			if rec.Src == "" || rec.Bin == "" || rec.Box == "" || rec.Smoke == "" {
				fail(exUsage, "Rezept für %q unvollständig (src/bin/box/smoke Pflicht)", app)
			}
			cfg, cfgErr := loadConfig()
			if cfgErr != nil {
				fail(exUsage, "deploy.json: %v", cfgErr)
			}
			b, err := cfg.box(rec.Box)
			if err != nil {
				fail(exUsage, "Rezept-Box %q: %v", rec.Box, err)
			}
			sshHost := b.Host

			// ── Trading-Wall (früh, VOR jeder DB-/Deploy-Aktion) ────────────
			if f, p := tradingWallViolation(map[string]string{
				"app": app, "unit": rec.Unit, "src": rec.Src,
				"bin": rec.Bin, "box": sshHost, "repo": m.Repo,
			}); f != "" {
				fail(exUsage, "Trading-Wall: Feld %s matcht Live-Geld-Muster %q — promote hart verweigert. Live-Geld-Deploys laufen NIE über die Factory.", f, p)
			}

			// ── Gate (a): grüner Staging-Smoke für die SHA ──────────────────
			head, err := latestStagingHead(ctx, dsn, app)
			if err != nil {
				fail(exUnavailable, "%v", err)
			}
			if !head.Found {
				fail(exNoStaging, "keine Staging-Zeile für %q im Ledger — erst auf Staging deployen (W2-Kette).", app)
			}
			if head.Status != "live" {
				fail(exNoStaging, "jüngster Staging-Deploy von %q ist status=%q (nicht 'live') — kein grüner Smoke, keine Promotion.", app, head.Status)
			}
			sha := head.Sha
			version := head.Version
			if version == "" {
				version = sha
			}
			fmt.Printf("→ Staging grün: %s @ %s (version %s) [Ledger env=staging live]\n", app, short(sha), short(version))

			// ── Gate (b): frisches Approval für (app, sha) ──────────────────
			appr, err := checkApproval(approvalDir, app, sha, time.Now())
			if err != nil {
				fail(exNoApproval, "%v\n  Approval anlegen (root):  sa-deploy-approve %s %s <approver> [ttl-hours]\n  (Datei %s)",
					err, app, sha, filepath.Join(approvalDir, app+"-"+sha+".json"))
			}
			fmt.Printf("→ Approval frisch: %s@%s von %s (bis %s)\n",
				app, short(sha), appr.ApprovedBy, appr.expiry().Format(time.RFC3339))

			if dryRun {
				fmt.Printf("DRY-RUN: würde %s @ %s → %s:%s (unit %s) bauen, smoken (%s), Ledger env=prod source=promote schreiben.\n",
					app, short(sha), sshHost, rec.Bin, rec.Unit, rec.Smoke)
				return nil
			}

			// ── Prod-Deploy: derselbe bewiesene Pfad wie der Reaktor ────────
			prev := prevProdVersion(ctx, dsn, app)
			gtArgs := []string{
				"--ref", sha, "--service", app, "--src", rec.Src,
				"--bin", rec.Bin, "--repo", m.Repo, "--box", sshHost,
			}
			if rec.Unit != "" {
				gtArgs = append(gtArgs, "--unit", rec.Unit)
			}
			fmt.Printf("→ Prod-Deploy via %s %s\n", deployGt, strings.Join(gtArgs, " "))
			dep := exec.CommandContext(ctx, deployGt, gtArgs...)
			dep.Stdout = os.Stdout
			dep.Stderr = os.Stderr
			if err := dep.Run(); err != nil {
				fail(exDeployFailed, "deploy-gt.sh fehlgeschlagen (%v) — Prod ggf. unverändert; siehe Log oben.", err)
			}

			// ── Prod-Smoke ──────────────────────────────────────────────────
			sc, err := runProdSmoke(ctx, rec.Smoke, sha)
			if err != nil {
				// Best-effort Rollback: die .prev-Binary zurückswappen + restarten.
				rollbackProd(ctx, sshHost, rec.Bin, rec.Unit)
				fail(exSmokeFailed, "Prod-Smoke rot (%v) — Rollback auf .prev versucht, KEINE Ledger-live-Zeile geschrieben.", err)
			}
			fmt.Printf("→ Prod-Smoke grün: %s meldet sha=%s env=%s\n", sc.Service, short(sc.Sha), sc.Env)

			// ── Ledger env=prod source=promote ─────────────────────────────
			id, err := recordProdLive(ctx, dsn, app, sha, version, rec.Smoke, prev, actor)
			if err != nil {
				fail(exUnavailable, "Prod ist scharf, aber Ledger-Schreiben ging nicht (%v) — manuell nachtragen!", err)
			}
			fmt.Printf("\033[1;32mok\033[0m Promotion: portfolio.deployments #%d ← %s@prod %s status=live source=promote\n", id, app, short(sha))
			return nil
		},
	}

	c.Flags().StringVar(&manifestPath, "manifest", envOr("DEPLOY_PROMOTE_MANIFEST", "/etc/sa-deploy/promote-manifest.yaml"), "Promote-Manifest (app→Prod-Rezept)")
	c.Flags().StringVar(&approvalDir, "approval-dir", envOr("DEPLOY_APPROVAL_DIR", "/etc/sa-deploy/approvals"), "Approval-Store (root-only)")
	c.Flags().StringVar(&dsn, "ledger-dsn", envOr("PORTFOLIO_DSN", envOr("MERGER_LEDGER_PG_URI", "postgres://mario@127.0.0.1:5434/mario_brain?sslmode=disable")), "Release-Ledger DSN (:5434)")
	c.Flags().StringVar(&deployGt, "deploy-gt", envOr("DEPLOY_GT_SH", "/opt/stack/tools/portfolio/deploy-gt.sh"), "Pfad zu deploy-gt.sh (die Deploy-Hände)")
	hn, _ := os.Hostname()
	c.Flags().StringVar(&actor, "actor", envOr("DEPLOY_ACTOR", "promote@"+hn), "deployed_by im Ledger")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "prüft beide Gates, deployt/smoked/schreibt aber nicht")
	return c
}

// rollbackProd swappt die .prev-Binary zurück und restartet die Unit (best
// effort — jeder Fehler wird nur gewarnt; der Aufrufer bricht ohnehin ab).
func rollbackProd(ctx context.Context, sshHost, bin, unit string) {
	cmd := fmt.Sprintf("test -x '%s.prev' && mv -f '%s.prev' '%s'", bin, bin, bin)
	if unit != "" {
		cmd += fmt.Sprintf(" && systemctl restart '%s'", unit)
	}
	rb := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", sshHost, cmd)
	rb.Stdout = os.Stderr
	rb.Stderr = os.Stderr
	if err := rb.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "! Rollback auf %s.prev ging nicht (%v) — Prod manuell prüfen!\n", bin, err)
	}
}
