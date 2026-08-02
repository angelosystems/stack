package main

// portfolio_bericht.go — Autopilot Stufe 2, W4 (PRD mk-autopilot-stufe2,
// Deep-Tech-Panel: auflagenfrei). `mk portfolio-bericht` erzeugt die
// monatlichen Quermuster als Vault-Dokument (mario-brain, ADR-0010) —
// deterministisch, KEIN LLM, KEIN Vollzug. Der Morgenbrief verlinkt den
// Bericht an den ersten drei Tagen des Monats.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

const vaultBerichtDir = "/root/mario-brain/vault/angeloos/wiki"

func berichtPath(t time.Time) string {
	return filepath.Join(vaultBerichtDir, fmt.Sprintf("mk-portfolio-bericht-%s.md", t.Format("2006-01")))
}

func cmdPortfolioBericht() *cobra.Command {
	var dryRun bool
	c := &cobra.Command{
		Use:   "portfolio-bericht",
		Short: "Monatliche Portfolio-Quermuster als Vault-Dokument (W4, deterministisch, kein Vollzug)",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := connect()
			body := buildPortfolioBericht(context.Background(), p)
			if dryRun {
				fmt.Println(body)
				return nil
			}
			out := berichtPath(time.Now())
			if err := os.WriteFile(out, []byte(body), 0o644); err != nil {
				return fmt.Errorf("Vault-Schreiben %s: %w (existiert %s?)", out, err, vaultBerichtDir)
			}
			fmt.Println("✓ Bericht:", out, "— Index-Zeile + Commit/Push macht die pflegende Session (Vault-Konvention).")
			return nil
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "nur drucken, nicht schreiben")
	return c
}

func buildPortfolioBericht(ctx context.Context, p *pgxpool.Pool) string {
	now := time.Now()
	var sb strings.Builder
	fmt.Fprintf(&sb, `---
title: "Master-Kanban Portfolio-Bericht %s"
tenant: angeloos
created: %s
quelle: mk portfolio-bericht (deterministisch, W4 mk-autopilot-stufe2)
---

# Portfolio-Quermuster %s

`, now.Format("2006-01"), now.Format("2006-01-02"), now.Format("January 2006"))

	// 1. Zustrom/Abfluss je Firma (28T)
	sb.WriteString("## Zustrom vs. Abfluss je Firma (28 Tage)\n\n| Firma | neu | archiviert | netto |\n|---|---|---|---|\n")
	if rows, err := p.Query(ctx, `
		SELECT firma,
		       count(*) FILTER (WHERE created_at > now()-interval '28 days'),
		       count(*) FILTER (WHERE archived_at > now()-interval '28 days')
		  FROM portfolio.initiative GROUP BY firma ORDER BY 2 DESC`); err == nil {
		for rows.Next() {
			var f string
			var neu, arch int
			if rows.Scan(&f, &neu, &arch) == nil {
				fmt.Fprintf(&sb, "| %s | %d | %d | %+d |\n", f, neu, arch, neu-arch)
			}
		}
		rows.Close()
	}

	// 2. Alters-Verteilung je Stage (Median-Tage in Spalte)
	sb.WriteString("\n## Spalten-Alter (Median Tage in aktueller Stage)\n\n| Stage | Karten | Median | Max |\n|---|---|---|---|\n")
	if rows, err := p.Query(ctx, `
		WITH a AS (
		  SELECT i.stage, extract(epoch FROM now()-COALESCE(
		    (SELECT max(e.at) FROM portfolio.initiative_event e
		      WHERE e.initiative_id=i.id AND e.kind='moved' AND e.to_stage=i.stage), i.created_at))/86400 AS d
		  FROM portfolio.initiative i WHERE i.archived_at IS NULL)
		SELECT stage, count(*), round(percentile_cont(0.5) WITHIN GROUP (ORDER BY d))::int, round(max(d))::int
		  FROM a GROUP BY stage
		 ORDER BY array_position(ARRAY['idea','soon','now','watching','done'], stage)`); err == nil {
		for rows.Next() {
			var s string
			var n, med, max int
			if rows.Scan(&s, &n, &med, &max) == nil {
				fmt.Fprintf(&sb, "| %s | %d | %d | %d |\n", s, n, med, max)
			}
		}
		rows.Close()
	}

	// 3. Doppel-Absichten (firmenuebergreifend, pg_trgm > 0.5, Top 10)
	sb.WriteString("\n## Doppel-Absichten (Titel-Aehnlichkeit > 0.5, firmenuebergreifend)\n\n")
	if rows, err := p.Query(ctx, `
		SELECT a.id, b.id, round(similarity(lower(a.title), lower(b.title))::numeric, 2)
		  FROM portfolio.initiative a
		  JOIN portfolio.initiative b ON a.id < b.id
		 WHERE a.archived_at IS NULL AND b.archived_at IS NULL
		   AND a.id NOT LIKE 'fabrik-%' AND b.id NOT LIKE 'fabrik-%'
		   AND similarity(lower(a.title), lower(b.title)) > 0.5
		 ORDER BY 3 DESC LIMIT 10`); err == nil {
		n := 0
		for rows.Next() {
			var a, b string
			var sim float64
			if rows.Scan(&a, &b, &sim) == nil {
				fmt.Fprintf(&sb, "- %s ↔ %s (%.2f)\n", a, b, sim)
				n++
			}
		}
		rows.Close()
		if n == 0 {
			sb.WriteString("- keine — Board ist absichts-sauber\n")
		}
	}

	// 4. Fluss-Bilanz (Moves 28T nach Aktor-Klasse) + Autopilot-Quote
	var mensch, auto, abschl, autopilot int
	_ = p.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE actor <> 'flow-manager'),
		count(*) FILTER (WHERE actor = 'flow-manager')
		FROM portfolio.initiative_event
		WHERE kind='moved' AND at > now()-interval '28 days'`).Scan(&mensch, &auto)
	_ = p.QueryRow(ctx, `
		WITH fertig AS (SELECT id FROM portfolio.initiative
		  WHERE archived_at > now()-interval '28 days' OR (stage='done' AND archived_at IS NULL))
		SELECT count(*), count(*) FILTER (WHERE NOT EXISTS (
		  SELECT 1 FROM portfolio.initiative_event e WHERE e.initiative_id=fertig.id
		    AND e.kind IN ('moved','dispatched') AND e.actor <> 'flow-manager'))
		FROM fertig`).Scan(&abschl, &autopilot)
	fmt.Fprintf(&sb, "\n## Fluss-Bilanz (28 Tage)\n\n- Moves: %d menschlich · %d Flow-Manager\n- Autopilot-Quote: %d/%d Abschluesse ohne menschlichen Move/Dispatch\n",
		mensch, auto, autopilot, abschl)

	sb.WriteString("\nBoard: master.stayawesome.app · Bericht ist Analyse, kein Vollzug.\n")
	return sb.String()
}
