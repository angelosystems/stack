package main

// abfluss.go — mk-abfluss-automatik WP1+WP2 (Politik-Go Mario 2026-08-02:
// der Verwalter darf die ARCHIV-Seite autonom vollziehen).
//
//   WP1  Fertig>NT (Default 14) ⇒ Soft-Archiv. Zwei Pflicht-Guards
//        (triage-verifiziert, treffen heute exakt cf-mk-verwalter-vollzug):
//        (a) Deployment live/deploying haengt, (b) offenes Kind.
//        Pins auf done blockieren NICHT (done ist terminal; WP4 beendet
//        den Pin-Nebeneffekt ohnehin).
//   WP2  3 aufeinanderfolgende flow_action-Diagnosen verlassen/High/archive
//        + unpinned + keine aktiven Beads ⇒ Soft-Archiv mit Diagnose-Note.
//        quantbot: nie (Findings-Kultur). Nach dem Archiv herrscht Ruhe
//        von selbst (archivierte Karten verlassen jeden Sweep/Sage-Scan).
//
// Alles reversibel (archived_at NULL setzen), jede Aktion mit Note im
// archived-Event. Not-Aus: PORTFOLIO_STEWARD_HALT=1 oder ABFLUSS=off.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

// abflussMaxPerSweep: Sturmbremse — der Rest kommt in den Folgerunden.
const abflussMaxPerSweep = 10

func abflussArchivTage() int {
	if v, err := strconv.Atoi(os.Getenv("PORTFOLIO_ARCHIV_TAGE")); err == nil && v > 0 {
		return v
	}
	return 14
}

func abflussVerlassenN() int {
	if v, err := strconv.Atoi(os.Getenv("PORTFOLIO_VERLASSEN_N")); err == nil && v > 0 {
		return v
	}
	return 3
}

// archiveWithNote vollzieht das Soft-Archiv + archived-Event mit Begruendung.
func archiveWithNote(ctx context.Context, p *pgxpool.Pool, id, note string) error {
	tag, err := p.Exec(ctx, `UPDATE portfolio.initiative SET archived_at = now(), updated_at = now()
		WHERE id = $1 AND archived_at IS NULL`, id)
	if err != nil || tag.RowsAffected() == 0 {
		return fmt.Errorf("archiv %s: %v (rows=%d)", id, err, tag.RowsAffected())
	}
	pb, _ := json.Marshal(map[string]any{"note": note})
	_, _ = p.Exec(ctx, `INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
		VALUES ($1,'archived','flow_manager',$2,'abfluss-automatik')`, id, pb)
	return nil
}

// runAbfluss laeuft einmal pro Sweep (nach den Vorwaerts-Regeln).
func runAbfluss(ctx context.Context, p *pgxpool.Pool, dryRun bool) {
	if os.Getenv("ABFLUSS") == "off" || os.Getenv("PORTFOLIO_STEWARD_HALT") == "1" {
		return
	}
	archived := 0

	// ── WP1: Fertig aelter N Tage, beide Guards ─────────────────────────
	rows, err := p.Query(ctx, fmt.Sprintf(`
		SELECT i.id FROM portfolio.initiative i
		WHERE i.archived_at IS NULL AND i.stage='done'
		  AND COALESCE((SELECT max(e.at) FROM portfolio.initiative_event e
		        WHERE e.initiative_id=i.id AND e.kind='moved' AND e.to_stage='done'), i.created_at)
		      < now() - interval '%d days'
		  AND NOT EXISTS (SELECT 1 FROM portfolio.deployments d
		        WHERE d.initiative_id=i.id AND d.status IN ('live','deploying'))
		  AND NOT EXISTS (SELECT 1 FROM portfolio.initiative k
		        WHERE k.parent_id=i.id AND k.archived_at IS NULL AND k.stage<>'done')
		ORDER BY i.created_at LIMIT %d`, abflussArchivTage(), abflussMaxPerSweep))
	if err == nil {
		var ids []string
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			if dryRun {
				fmt.Printf("[dry-run] abfluss: würde %s archivieren (done>%dT)\n", id, abflussArchivTage())
				continue
			}
			if archiveWithNote(ctx, p, id, fmt.Sprintf(
				"Abfluss-Automatik WP1: Fertig seit >%d Tagen, keine Live-Deployments, keine offenen Kinder (Politik-Go Mario 2026-08-02). Reversibel.",
				abflussArchivTage())) == nil {
				fmt.Printf("  🧹 abfluss: %s archiviert (done>%dT)\n", id, abflussArchivTage())
				archived++
			}
		}
	}

	// ── WP2: N× verlassen/High/archive in Folge, unpinned, keine aktive
	//    Arbeit, kein quantbot — dieselben zwei Guards wie WP1 ──────────
	if archived < abflussMaxPerSweep {
		n := abflussVerlassenN()
		rows2, err := p.Query(ctx, fmt.Sprintf(`
			SELECT i.id FROM portfolio.initiative i
			WHERE i.archived_at IS NULL
			  AND i.stage <> 'done'
			  AND i.firma <> 'quantbot'
			  AND NOT COALESCE(i.stage_locked_by_human, false)
			  AND (COALESCE(i.beads_total,0)=0 OR i.beads_closed=i.beads_total)
			  AND NOT EXISTS (SELECT 1 FROM portfolio.deployments d
			        WHERE d.initiative_id=i.id AND d.status IN ('live','deploying'))
			  AND NOT EXISTS (SELECT 1 FROM portfolio.initiative k
			        WHERE k.parent_id=i.id AND k.archived_at IS NULL AND k.stage<>'done')
			  -- FEHLALARM-Lehre 2026-08-02 (20 Fehl-Archive in 2 Sweeps, revertet):
			  -- Gate-Warter sind NICHT verlassen. Wer einen lebenden Plan hat,
			  -- eine Lane traegt oder in 30 Tagen bewusst von Mario platziert
			  -- wurde, wartet — den archiviert keine Automatik.
			  AND NOT EXISTS (SELECT 1 FROM portfolio.plan_item pi
			        WHERE pi.initiative_id=i.id
			          AND pi.status IN ('draft','review','approved','approved-with-notes','in-progress'))
			  AND NOT EXISTS (SELECT 1 FROM portfolio.initiative_tag t
			        WHERE t.initiative_id=i.id AND t.kind='lane')
			  AND NOT EXISTS (SELECT 1 FROM portfolio.initiative_event me
			        WHERE me.initiative_id=i.id AND me.kind='moved'
			          AND me.actor NOT IN ('flow-manager') AND me.at > now() - interval '30 days')
			  -- Nachschaerfung 2: in Spalte GEBORENE Karten haben kein moved-Event
			  -- (sa-estate-rebuild-Fall) — unter 30 Tagen ist nichts verlassen.
			  -- Und die N Diagnosen muessen echte Zeit ueberspannen (>=7 Tage),
			  -- nicht drei Sweeps einer Stunde.
			  AND i.created_at < now() - interval '30 days'
			  AND (SELECT max(e2.at) - min(e2.at) FROM (
			         SELECT e.at FROM portfolio.initiative_event e
			          WHERE e.initiative_id=i.id AND e.kind='flow_action'
			            AND COALESCE(e.payload->'flagged_reasons','[]'::jsonb) <> '[]'::jsonb
			          ORDER BY e.at DESC LIMIT %d
			       ) e2) >= interval '7 days'
			  AND (SELECT count(*) FROM (
			         SELECT e.payload FROM portfolio.initiative_event e
			          WHERE e.initiative_id=i.id AND e.kind='flow_action'
			            AND COALESCE(e.payload->'flagged_reasons','[]'::jsonb) <> '[]'::jsonb
			          ORDER BY e.at DESC LIMIT %d
			       ) letzte
			       WHERE letzte.payload->>'category'='verlassen'
			         AND letzte.payload->>'confidence'='High'
			         AND letzte.payload->>'proposed_action' LIKE 'archive%%') = %d
			ORDER BY i.created_at LIMIT %d`, n, n, n, abflussMaxPerSweep))
		if err == nil {
			var ids []string
			for rows2.Next() {
				var id string
				if rows2.Scan(&id) == nil {
					ids = append(ids, id)
				}
			}
			rows2.Close()
			for _, id := range ids {
				if archived >= abflussMaxPerSweep {
					break
				}
				if dryRun {
					fmt.Printf("[dry-run] abfluss: würde %s archivieren (%d× verlassen)\n", id, n)
					continue
				}
				if archiveWithNote(ctx, p, id, fmt.Sprintf(
					"Abfluss-Automatik WP2: %d aufeinanderfolgende Diagnosen 'verlassen' (High, Vorschlag archive), keine aktive Arbeit, ungepinnt. Reversibel — Reaktivierung = archived_at loeschen.",
					n)) == nil {
					fmt.Printf("  🧹 abfluss: %s archiviert (%d× verlassen-Diagnose)\n", id, n)
					archived++
				}
			}
		}
	}
	if archived > 0 {
		fmt.Printf("=== Abfluss-Automatik: %d Karte(n) archiviert ===\n", archived)
	}
}
