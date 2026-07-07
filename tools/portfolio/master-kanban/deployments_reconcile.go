package main

// deployments_reconcile.go — die Health-Probe als Release-Ledger-Reconciler
// (Release-Pipeline-PRD WP3b/WP4) + /api/releases (WP4).
//
// Der Reconciler versöhnt den Steady-State des Ledgers mit der Wirklichkeit:
// er liest je (service, environment) die jüngste Ledger-Zeile (Head), sondiert
// deren /version-Oberfläche (D18: http = GET health_url, cli = `<binary>
// version --json`) und schreibt live/errored — nie mehr. Er deployt nichts,
// rollt nichts zurück (das gehört dem Deploy-Reaktor, WP5) und fasst geleaste
// Zeilen nicht an (D13). Zusätzlich ist er die EINZIGE Schreibquelle der
// denormalisierten Board-Felder initiative.deploy_state/live_version/live_sha
// (WP4) — nicht der Ledger-Zeilen anderer Akteure.
//
// befund #1: die Denormalisierung greift nur, wenn die Deploy-Zeile ein
// initiative_id trägt (denormalizeInitiatives filtert IS NOT NULL). Der
// Merger-Producer stampt es jetzt aus der service→initiative-Map — so folgt
// die Board-Karte autonom jedem gegateten Merger-Deploy.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

type deploymentHead struct {
	ID          int64
	Service     string
	ProbeKind   string
	Environment string
	Version     string
	GitSha      string
	Status      string
	OwnedBy     *string
	OwnedUntil  *time.Time
	DeployedAt  time.Time
	HealthURL   *string
}

type probeResult struct {
	Reached bool   // Oberfläche erreichbar + Vertrag parsebar
	Sha     string
	Version string
	Env     string
	Err     string // menschenlesbar, für Log
}

type reconcileVerdict struct {
	NewStatus string // "" = keine Änderung / nicht anfassen
	Reason    string
}

// shaMatch prüft die SHA-Identität (D12) präfix-tolerant (Kurz- vs. Lang-SHA);
// Fallback auf version für Oberflächen ohne sha-Feld (wie deploy_reactor.go).
func shaMatch(row deploymentHead, probe probeResult) bool {
	live := probe.Sha
	if live == "" || live == "unknown" {
		live = probe.Version
	}
	if live == "" || row.GitSha == "" {
		return false
	}
	return strings.HasPrefix(row.GitSha, live) || strings.HasPrefix(live, row.GitSha)
}

// decideReconcile ist die Übergangstabelle des Reconcilers (D13) — pur und
// DB-frei, damit sie als Zustandsmaschine testbar ist:
//
//	geleast (owned_until > now)      → skip (Zeile gehört dem Reaktor)
//	pending                          → skip (Outbox; Konsument ist WP5, nicht wir)
//	rolled_back                      → skip (terminal je SHA, Quarantäne-Semantik D15)
//	im Smoke-Fenster (deploy frisch) → skip (D18 Readiness-Backoff: Restart ≠ rot)
//	deploying · Match                → live   (Probe „bestätigt nur", Risiko-Tabelle)
//	deploying · kein Match/rot       → errored (jenseits des Fensters, D13)
//	live      · Match                → bleibt
//	live      · kein Match/rot       → errored
//	errored   · Match                → live   (Selbstheilung: Zustand folgt Realität)
//	errored   · kein Match/rot       → bleibt
func decideReconcile(row deploymentHead, probe probeResult, now time.Time, smokeWindow time.Duration) reconcileVerdict {
	if row.OwnedUntil != nil && row.OwnedUntil.After(now) {
		return reconcileVerdict{Reason: "geleast bis " + row.OwnedUntil.Format(time.RFC3339)}
	}
	switch row.Status {
	case "pending":
		return reconcileVerdict{Reason: "Outbox-Zeile — wartet auf Deploy-Reaktor (WP5)"}
	case "rolled_back":
		return reconcileVerdict{Reason: "rolled_back ist terminal je SHA (D15)"}
	}
	inWindow := now.Sub(row.DeployedAt) < smokeWindow
	match := probe.Reached && shaMatch(row, probe)
	if match {
		switch row.Status {
		case "live":
			return reconcileVerdict{Reason: "Match — bleibt live"}
		case "deploying", "errored":
			return reconcileVerdict{NewStatus: "live", Reason: "Probe bestätigt SHA " + row.GitSha}
		}
		return reconcileVerdict{Reason: "Match, Status " + row.Status + " unbekannt — nicht anfassen"}
	}
	// kein Match (rot oder falsche SHA)
	if inWindow {
		return reconcileVerdict{Reason: "im Smoke-Fenster — Restart ≠ rot (D18)"}
	}
	switch row.Status {
	case "deploying", "live":
		return reconcileVerdict{NewStatus: "errored", Reason: "Probe rot/Fehl-SHA: " + probe.Err}
	}
	return reconcileVerdict{Reason: "bleibt " + row.Status}
}

// probeHTTP sondiert die HTTP-Oberfläche des /version-Vertrags.
func probeHTTP(ctx context.Context, url string, timeout time.Duration) probeResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return probeResult{Err: "Request-Bau: " + err.Error()}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return probeResult{Err: "nicht erreichbar: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return probeResult{Err: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}
	return parseVersionContract(json.NewDecoder(resp.Body))
}

// probeCLI sondiert die CLI-Oberfläche (`<binary> version --json`, D18).
// Poka-yoke: nur absolute Binary-Pfade — der Ledger soll nicht von $PATH oder
// CWD des Probelaufs abhängen.
func probeCLI(ctx context.Context, binary string, timeout time.Duration) probeResult {
	if !filepath.IsAbs(binary) {
		return probeResult{Err: "health_url für probe_kind=cli muss absoluter Binary-Pfad sein, war: " + binary}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "version", "--json").Output()
	if err != nil {
		return probeResult{Err: "version --json fehlgeschlagen: " + err.Error()}
	}
	return parseVersionContract(json.NewDecoder(strings.NewReader(string(out))))
}

func parseVersionContract(dec *json.Decoder) probeResult {
	var v VersionInfo
	if err := dec.Decode(&v); err != nil {
		return probeResult{Err: "Vertrag nicht parsebar: " + err.Error()}
	}
	return probeResult{Reached: true, Sha: v.Sha, Version: v.Version, Env: v.Env}
}

func probeRow(ctx context.Context, row deploymentHead, timeout time.Duration) probeResult {
	if row.HealthURL == nil || *row.HealthURL == "" {
		return probeResult{Err: "keine health_url hinterlegt — Zeile nicht sondierbar"}
	}
	if row.ProbeKind == "cli" {
		return probeCLI(ctx, *row.HealthURL, timeout)
	}
	return probeHTTP(ctx, *row.HealthURL, timeout)
}

const headRowsSQL = `
SELECT DISTINCT ON (service, environment)
       id, service, probe_kind, environment, version, git_sha, status,
       owned_by, owned_until, deployed_at, health_url
  FROM portfolio.deployments
 ORDER BY service, environment, deployed_at DESC`

// reconcileOnce fährt einen Versöhnungs-Durchlauf: Head-Zeilen sondieren,
// Übergänge CAS-geschützt schreiben, dann die Board-Felder denormalisieren.
func reconcileOnce(ctx context.Context, p *pgxpool.Pool, smokeWindow, probeTimeout time.Duration, dryRun bool, logf func(string, ...any)) error {
	rows, err := p.Query(ctx, headRowsSQL)
	if err != nil {
		return fmt.Errorf("Head-Zeilen lesen: %w", err)
	}
	var heads []deploymentHead
	for rows.Next() {
		var h deploymentHead
		if err := rows.Scan(&h.ID, &h.Service, &h.ProbeKind, &h.Environment, &h.Version,
			&h.GitSha, &h.Status, &h.OwnedBy, &h.OwnedUntil, &h.DeployedAt, &h.HealthURL); err != nil {
			rows.Close()
			return fmt.Errorf("Head-Zeile scannen: %w", err)
		}
		heads = append(heads, h)
	}
	rows.Close()
	if rows.Err() != nil {
		return fmt.Errorf("Head-Zeilen iterieren: %w", rows.Err())
	}

	now := time.Now()
	changed := 0
	for _, h := range heads {
		var verdict reconcileVerdict
		leased := h.OwnedUntil != nil && h.OwnedUntil.After(now)
		if leased || h.Status == "pending" || h.Status == "rolled_back" {
			verdict = decideReconcile(h, probeResult{}, now, smokeWindow)
		} else {
			probe := probeRow(ctx, h, probeTimeout)
			verdict = decideReconcile(h, probe, now, smokeWindow)
			if probe.Reached && probe.Env != "" && probe.Env != h.Environment {
				logf("⚠ %s@%s: /version meldet env=%q, Ledger sagt %q — Config prüfen (D20b folgt in WP5)",
					h.Service, h.Environment, probe.Env, h.Environment)
			}
		}
		if verdict.NewStatus == "" || verdict.NewStatus == h.Status {
			logf("· %s@%s [%s] %s", h.Service, h.Environment, h.Status, verdict.Reason)
			continue
		}
		if dryRun {
			logf("DRY %s@%s: %s → %s (%s)", h.Service, h.Environment, h.Status, verdict.NewStatus, verdict.Reason)
			continue
		}
		// Geschützter Übergang (D13): CAS auf Alt-Status + Lease-Riegel — flippt
		// nie eine Zeile, die inzwischen ein Reaktor geleast oder verändert hat.
		tag, err := p.Exec(ctx, `UPDATE portfolio.deployments
		     SET status=$1 WHERE id=$2 AND status=$3
		     AND (owned_until IS NULL OR owned_until < now())`,
			verdict.NewStatus, h.ID, h.Status)
		if err != nil {
			return fmt.Errorf("Übergang %s→%s für %s: %w", h.Status, verdict.NewStatus, h.Service, err)
		}
		if tag.RowsAffected() == 1 {
			changed++
			logf("✓ %s@%s: %s → %s (%s)", h.Service, h.Environment, h.Status, verdict.NewStatus, verdict.Reason)
		} else {
			logf("· %s@%s: Übergang %s→%s übersprungen — Zeile wurde zwischenzeitlich geleast/geändert",
				h.Service, h.Environment, h.Status, verdict.NewStatus)
		}
	}

	if dryRun {
		return nil
	}
	if err := denormalizeInitiatives(ctx, p); err != nil {
		return err
	}
	logf("Reconcile fertig: %d Head-Zeilen, %d Übergänge", len(heads), changed)
	return nil
}

// denormalizeInitiatives schreibt initiative.deploy_state/live_version/live_sha
// aus den Head-Zeilen (WP4). deploy_state = Worst-of über deploy_state_map.rank;
// live_version/live_sha = jüngste live-Head-Zeile der Initiative.
func denormalizeInitiatives(ctx context.Context, p *pgxpool.Pool) error {
	_, err := p.Exec(ctx, `
WITH heads AS (
  SELECT DISTINCT ON (service, environment) initiative_id, status, version, git_sha, deployed_at
    FROM portfolio.deployments
   ORDER BY service, environment, deployed_at DESC
), worst AS (
  SELECT h.initiative_id, (ARRAY_AGG(m.deploy_state ORDER BY m.rank DESC))[1] AS deploy_state
    FROM heads h JOIN portfolio.deploy_state_map m ON m.status = h.status
   WHERE h.initiative_id IS NOT NULL
   GROUP BY h.initiative_id
), livehead AS (
  SELECT DISTINCT ON (initiative_id) initiative_id, version AS live_version, git_sha AS live_sha
    FROM heads
   WHERE initiative_id IS NOT NULL AND status = 'live'
   ORDER BY initiative_id, deployed_at DESC
)
UPDATE portfolio.initiative i
   SET deploy_state = w.deploy_state,
       live_version = lh.live_version,
       live_sha     = lh.live_sha
  FROM worst w LEFT JOIN livehead lh ON lh.initiative_id = w.initiative_id
 WHERE i.id = w.initiative_id
   AND (i.deploy_state IS DISTINCT FROM w.deploy_state
     OR i.live_version IS DISTINCT FROM lh.live_version
     OR i.live_sha     IS DISTINCT FROM lh.live_sha)`)
	if err != nil {
		return fmt.Errorf("Denormalisierung (worst/livehead): %w", err)
	}
	// Initiativen ohne Ledger-Zeilen: Felder räumen (sonst lügt die Karte).
	_, err = p.Exec(ctx, `
UPDATE portfolio.initiative i
   SET deploy_state = NULL, live_version = NULL, live_sha = NULL
 WHERE (i.deploy_state IS NOT NULL OR i.live_version IS NOT NULL OR i.live_sha IS NOT NULL)
   AND NOT EXISTS (SELECT 1 FROM portfolio.deployments d WHERE d.initiative_id = i.id)`)
	if err != nil {
		return fmt.Errorf("Denormalisierung (Aufräumen): %w", err)
	}
	return nil
}

// cmdDeployments — Reconciler-CLI. Ein Durchlauf pro Aufruf; getaktet wird er
// von mk-health (60-s-Timer), nicht von einer eigenen Schleife: die Probe, die
// ohnehin läuft, versöhnt den Ledger (Architektur-Entscheidung 5 im PRD).
func cmdDeployments() *cobra.Command {
	c := &cobra.Command{
		Use:   "deployments",
		Short: "Release-Ledger (portfolio.deployments)",
	}

	var dryRun, quiet bool
	var smokeWindow, probeTimeout time.Duration
	rec := &cobra.Command{
		Use:   "reconcile",
		Short: "Ein Versöhnungs-Durchlauf: /version je Head-Zeile sondieren, live/errored schreiben, Board-Felder denormalisieren",
		RunE: func(cmd *cobra.Command, args []string) error {
			logf := func(format string, a ...any) {
				if !quiet {
					fmt.Printf(format+"\n", a...)
				}
			}
			return reconcileOnce(cmd.Context(), connect(), smokeWindow, probeTimeout, dryRun, logf)
		},
	}
	rec.Flags().BoolVar(&dryRun, "dry-run", false, "nur Verdicts zeigen, nichts schreiben")
	rec.Flags().BoolVar(&quiet, "quiet", false, "nur Fehler ausgeben (für den Timer-Aufruf)")
	rec.Flags().DurationVar(&smokeWindow, "smoke-window", envDuration("MK_RECONCILE_SMOKE_WINDOW", 10*time.Minute),
		"Frist nach deployed_at, in der eine rote Probe NICHT errored setzt (Readiness-Backoff, D18)")
	rec.Flags().DurationVar(&probeTimeout, "probe-timeout", envDuration("MK_RECONCILE_PROBE_TIMEOUT", 5*time.Second),
		"Timeout je /version-Probe (Totmann-Geist von D17)")
	c.AddCommand(rec)
	return c
}

func envDuration(key string, def time.Duration) time.Duration {
	if s := envOr(key, ""); s != "" {
		if d, err := time.ParseDuration(s); err == nil {
			return d
		}
	}
	return def
}

// handleReleases — GET /api/releases (WP4): der Live-Stand je (service,
// environment) aus den Head-Zeilen, plus Initiative-Titel fürs Board.
// releasesBaseSelect ist der Head-je-(service,environment)-Read hinter
// /api/releases (WP4). Als Konstante + reine buildReleasesQuery gebaut, damit
// der Filter-Kontrakt ohne DB testbar ist.
const releasesBaseSelect = `
SELECT DISTINCT ON (d.service, d.environment)
       d.service, d.environment, d.status, d.version, d.git_sha,
       d.deployed_at, d.deployed_by, COALESCE(d.deploy_method,''),
       COALESCE(d.prev_version,''), COALESCE(d.initiative_id,''), COALESCE(i.title,''),
       d.probe_kind, COALESCE(d.health_url,''), COALESCE(d.log_url,''),
       COALESCE(d.migration_version,''), d.owned_by IS NOT NULL AND d.owned_until > now() AS leased
  FROM portfolio.deployments d
  LEFT JOIN portfolio.initiative i ON i.id = d.initiative_id`

const releasesOrderBy = `
 ORDER BY d.service, d.environment, d.deployed_at DESC`

// buildReleasesQuery hängt optional einen service-Filter an den Head-Read an.
// ACI-Kalibrier-Knopf: `/api/releases?service=master-kanban` liefert nur die
// Head-Zeile(n) des gefragten Service statt aller — das Board/der CD-Reconciler
// fragt gezielt, ohne clientseitig die Vollmenge zu filtern. Parametrisiert
// ($1) statt String-Interpolation (poka-yoke gegen SQL-Injection). Leerer/
// whitespace Wert = kein Filter = altes Verhalten.
func buildReleasesQuery(service string) (string, []any) {
	service = strings.TrimSpace(service)
	if service == "" {
		return releasesBaseSelect + releasesOrderBy, nil
	}
	return releasesBaseSelect + "\n WHERE d.service = $1" + releasesOrderBy, []any{service}
}

func handleReleases(p *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sql, args := buildReleasesQuery(r.URL.Query().Get("service"))
		rows, err := p.Query(r.Context(), sql, args...)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()
		type release struct {
			Service          string    `json:"service"`
			Environment      string    `json:"environment"`
			Status           string    `json:"status"`
			Version          string    `json:"version"`
			GitSha           string    `json:"git_sha"`
			DeployedAt       time.Time `json:"deployed_at"`
			DeployedBy       string    `json:"deployed_by"`
			DeployMethod     string    `json:"deploy_method,omitempty"`
			PrevVersion      string    `json:"prev_version,omitempty"`
			InitiativeID     string    `json:"initiative_id,omitempty"`
			InitiativeTitle  string    `json:"initiative_title,omitempty"`
			ProbeKind        string    `json:"probe_kind"`
			HealthURL        string    `json:"health_url,omitempty"`
			LogURL           string    `json:"log_url,omitempty"`
			MigrationVersion string    `json:"migration_version,omitempty"`
			Leased           bool      `json:"leased"`
		}
		out := []release{}
		for rows.Next() {
			var rel release
			if err := rows.Scan(&rel.Service, &rel.Environment, &rel.Status, &rel.Version, &rel.GitSha,
				&rel.DeployedAt, &rel.DeployedBy, &rel.DeployMethod, &rel.PrevVersion,
				&rel.InitiativeID, &rel.InitiativeTitle, &rel.ProbeKind, &rel.HealthURL,
				&rel.LogURL, &rel.MigrationVersion, &rel.Leased); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			out = append(out, rel)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	}
}
