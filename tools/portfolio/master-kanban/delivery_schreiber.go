package main

// delivery_schreiber.go — Autopilot Stufe 1, W1 (PRD mk-autopilot-stufe1):
// Nachweis-Karten mit komplett geschlossenen Beads bekommen ihren
// Delivery-Report AUTOMATISCH aus der Evidenz (Beads + Ledger + PRs)
// generiert — deterministisch, kein LLM. Datei + lokaler git-Commit +
// Karten-Link; den Rest (Adapter-Sync, watching→done) erledigt die
// bestehende Maschinerie. Kein Auto-Push (Morgenbrief zaehlt wartende
// Commits); quantbot bekommt NIE Auto-Reports (Live-Geld-Kultur:
// Kommentar statt Datei).

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// deliveryRepoWhitelist: nur in diese Repo-Wurzeln schreibt der Schreiber
// (Poka-yoke gegen Pfad-Spielereien in plan_item.path). Bewusst OHNE
// /opt/quantbot — dort gilt ohnehin die firma-Sperre, doppelt haelt besser.
var deliveryRepoWhitelist = []string{
	"/opt/master-kanban", "/opt/stack", "/root/solartown",
	"/root/stayawesomeOS", "/root/mario-brain",
}

// deliverySchreiberMaxPerSweep: Sturmbremse — mehr Kandidaten warten auf
// die naechste Runde (30-s-Takt, kein Verlust).
const deliverySchreiberMaxPerSweep = 3

// maybeWriteDeliveryReport prueft eine watching-Karte ohne Delivery-Beleg
// und generiert bei wasserdichter Evidenz den Report. Rueckgabe true, wenn
// ein Report geschrieben wurde (Sturmbremsen-Zaehler des Aufrufers).
func maybeWriteDeliveryReport(ctx context.Context, p *pgxpool.Pool, init FlowInitiative, beads []LinkedBead, dryRun bool) bool {
	if os.Getenv("DELIVERY_SCHREIBER") == "off" || os.Getenv("PORTFOLIO_STEWARD_HALT") == "1" {
		return false
	}
	if init.Stage != "watching" || len(beads) == 0 {
		return false
	}
	var locked bool
	if err := p.QueryRow(ctx, `SELECT COALESCE(stage_locked_by_human,false) FROM portfolio.initiative WHERE id=$1`,
		init.ID).Scan(&locked); err != nil || locked {
		return false
	}

	// Frischer Bead-Cross-Check gegen solartown (Regel 1: nie der Cache
	// allein) — jeder Bead muss live 'closed' sein.
	sp, err := solartownPool()
	if err != nil {
		return false
	}
	type beadRow struct{ ID, Title, ClosedAt string }
	var rows []beadRow
	for _, b := range beads {
		var title string
		var closedAt *time.Time
		err := sp.QueryRow(ctx, `SELECT title, closed_at FROM beads.issues
			WHERE id=$1 AND deleted_at IS NULL AND status='closed'`, b.Ref).Scan(&title, &closedAt)
		if err != nil {
			return false // ein Bead nicht live-closed ⇒ keine Evidenz, kein Report
		}
		ca := ""
		if closedAt != nil {
			ca = closedAt.Format("2006-01-02")
		}
		rows = append(rows, beadRow{ID: b.Ref, Title: title, ClosedAt: ca})
	}

	// quantbot: nie Auto-Report — sichtbarer Kommentar als Urteilsfall.
	if init.Firma == "quantbot" {
		payload := fmt.Sprintf(`{"text":"Delivery-Schreiber: alle %d Beads closed, aber quantbot bekommt keine Auto-Reports (Live-Geld) — Delivery-Beleg bitte menschlich pruefen."}`, len(rows))
		_, _ = p.Exec(ctx, `INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor, at)
			SELECT $1,'commented','flow_manager',$2::jsonb,'delivery-schreiber',now()
			WHERE NOT EXISTS (SELECT 1 FROM portfolio.initiative_event
				WHERE initiative_id=$1 AND actor='delivery-schreiber' AND at > now()-interval '7 days')`,
			init.ID, payload)
		return false
	}

	// plan_item liefert Slug + Ziel-Repo (dirname(dirname(path))).
	var slug, prdPath string
	if err := p.QueryRow(ctx, `SELECT slug, COALESCE(path,'') FROM portfolio.plan_item
		WHERE initiative_id=$1 AND layer <> 'delivery' AND path LIKE '%/docs/plans/%'
		ORDER BY (status IN ('approved','approved-with-notes','delivered','done')) DESC, updated_at DESC
		LIMIT 1`, init.ID).Scan(&slug, &prdPath); err != nil || prdPath == "" {
		return false
	}
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(prdPath))) // <repo>/docs/plans/x.md
	allowed := false
	for _, w := range deliveryRepoWhitelist {
		if repoRoot == w {
			allowed = true
			break
		}
	}
	if !allowed {
		fmt.Printf("  · delivery-schreiber: %s uebersprungen — Repo %q nicht in Whitelist\n", init.ID, repoRoot)
		return false
	}
	outPath := filepath.Join(repoRoot, "docs", "plans", slug+"-delivery.md")

	// Idempotenz: Datei existiert schon ⇒ nur Link sicherstellen.
	if _, err := os.Stat(outPath); err == nil {
		ensureDeliveryLink(ctx, p, init.ID, outPath)
		return false
	}

	// Dirty-Repo-Schutz: der Schreiber committet nur in sauberem Zustand
	// (nie fremde WIP-Aenderungen einsammeln).
	if out, err := exec.Command("git", "-C", repoRoot, "status", "--porcelain").Output(); err != nil || len(strings.TrimSpace(string(out))) > 0 {
		fmt.Printf("  · delivery-schreiber: %s uebersprungen — %s hat uncommittete Aenderungen\n", init.ID, repoRoot)
		return false
	}

	// Ledger + PRs fuer den Evidenz-Teil.
	var deployLines []string
	if drows, err := p.Query(ctx, `SELECT service, status, COALESCE(version,''), deployed_at
		FROM portfolio.deployments WHERE initiative_id=$1 ORDER BY deployed_at DESC LIMIT 5`, init.ID); err == nil {
		for drows.Next() {
			var svc, st, ver string
			var at time.Time
			if drows.Scan(&svc, &st, &ver, &at) == nil {
				deployLines = append(deployLines, fmt.Sprintf("- %s @ %s — %s (%s)", svc, ver, st, at.Format("2006-01-02")))
			}
		}
		drows.Close()
	}
	var prLines []string
	if prows, err := p.Query(ctx, `SELECT ref FROM portfolio.initiative_link
		WHERE initiative_id=$1 AND kind='github_pr'`, init.ID); err == nil {
		for prows.Next() {
			var ref string
			if prows.Scan(&ref) == nil {
				prLines = append(prLines, "- "+ref)
			}
		}
		prows.Close()
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "title: \"Delivery: %s\"\n", strings.ReplaceAll(init.Title, "\"", "'"))
	fmt.Fprintf(&sb, "slug: %s\nlayer: delivery\nstatus: delivered\n", slug)
	fmt.Fprintf(&sb, "parent_plan: %s\n", prdPath)
	fmt.Fprintf(&sb, "scope: \"Auto-generierter Delivery-Report (Delivery-Schreiber, PRD mk-autopilot-stufe1): alle %d Beads live-closed verifiziert.\"\n", len(rows))
	fmt.Fprintf(&sb, "created: %s\nreview:\n  quick: auto\n  deep: none\n---\n\n", time.Now().Format("2006-01-02"))
	fmt.Fprintf(&sb, "# Delivery-Report %s (auto-generiert %s)\n\n", slug, time.Now().Format("2006-01-02"))
	sb.WriteString("> Auto-generiert vom Delivery-Schreiber aus Bead-/Ledger-Evidenz —\n> kein Mensch hat diesen Report geschrieben. Karte: " + init.ID + ".\n\n")
	sb.WriteString("## Beads (alle live-closed, solartown :5433 verifiziert)\n\n")
	for _, r := range rows {
		fmt.Fprintf(&sb, "- %s — %s (closed %s)\n", r.ID, r.Title, r.ClosedAt)
	}
	if len(deployLines) > 0 {
		sb.WriteString("\n## Release-Ledger (eigene Deployments)\n\n" + strings.Join(deployLines, "\n") + "\n")
	}
	if len(prLines) > 0 {
		sb.WriteString("\n## Pull Requests\n\n" + strings.Join(prLines, "\n") + "\n")
	}

	if dryRun {
		fmt.Printf("[dry-run] delivery-schreiber würde %s erzeugen (%d Beads)\n", outPath, len(rows))
		return false
	}
	if err := os.WriteFile(outPath, []byte(sb.String()), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "  ❌ delivery-schreiber %s: schreiben: %v\n", init.ID, err)
		return false
	}
	commit := exec.Command("git", "-C", repoRoot,
		"-c", "user.name=delivery-schreiber", "-c", "user.email=mk@werkstatt",
		"commit", "--no-verify", "-q", "-m",
		fmt.Sprintf("delivery-schreiber: %s (auto-generiert aus Bead/Ledger-Evidenz, Karte %s)", slug, init.ID))
	add := exec.Command("git", "-C", repoRoot, "add", outPath)
	if err := add.Run(); err == nil {
		if err := commit.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ delivery-schreiber %s: Commit fehlgeschlagen (Datei bleibt): %v\n", init.ID, err)
		}
	}
	ensureDeliveryLink(ctx, p, init.ID, outPath)
	payload := fmt.Sprintf(`{"text":"Delivery-Report auto-generiert: %s (%d Beads live-closed, %d Ledger-Zeilen, %d PRs). Commit lokal — Push macht Mensch/Session; watching→done vollzieht der naechste Sweep."}`,
		outPath, len(rows), len(deployLines), len(prLines))
	_, _ = p.Exec(ctx, `INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor, at)
		VALUES ($1,'commented','flow_manager',$2::jsonb,'delivery-schreiber',now())`, init.ID, payload)
	fmt.Printf("  ✍ delivery-schreiber: %s → %s (%d Beads)\n", init.ID, outPath, len(rows))
	return true
}

// ensureDeliveryLink setzt den plan_file-Link auf die Karte (idempotent) —
// er ist die HasDelivery-Evidenz fuer watchingDoneDecision.
func ensureDeliveryLink(ctx context.Context, p *pgxpool.Pool, initiativeID, path string) {
	_, _ = p.Exec(ctx, `INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		SELECT $1,'plan_file',$2
		WHERE NOT EXISTS (SELECT 1 FROM portfolio.initiative_link
			WHERE initiative_id=$1 AND kind='plan_file' AND ref=$2)`, initiativeID, path)
}
