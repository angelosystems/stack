package main

// kpis.go — Prozess-Gesundheits-KPIs fuers Cockpit (Marios Ask 2026-07-16):
// EIN Endpoint /api/kpis mit den Fluss-Metriken (Durchsatz, Alter der Arbeit,
// Automatik-Anteil, Staus, Befunde, Puls) + stage_since je Karte fuer das
// Spalten-Alter auf der Karte. Alles read-only, Set-Queries, kein neuer
// Speicher — berechnet aus initiative/initiative_event/steward_findings/
// sage_status.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// loadStageSince liefert je unarchivierter Karte den Eintritt in die AKTUELLE
// Stage (juengstes moved-Event mit to_stage=stage; Fallback created_at) —
// Grundlage des Spalten-Alter-Chips.
func loadStageSince(ctx context.Context, p *pgxpool.Pool) map[string]time.Time {
	m := make(map[string]time.Time)
	rows, err := p.Query(ctx, `
		SELECT i.id, COALESCE(
		  (SELECT max(e.at) FROM portfolio.initiative_event e
		    WHERE e.initiative_id = i.id AND e.kind = 'moved' AND e.to_stage = i.stage),
		  i.created_at)
		FROM portfolio.initiative i
		WHERE i.archived_at IS NULL
	`)
	if err != nil {
		return m
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var at time.Time
		if rows.Scan(&id, &at) == nil {
			m[id] = at
		}
	}
	return m
}

// handleKPIs berechnet die Board-Gesundheit. response_format bewusst knapp:
// nur die Felder, die die KPI-Leiste rendert (ACI: high-signal).
func handleKPIs(p *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		w.Header().Set("Content-Type", "application/json")

		out := map[string]any{}

		// 1. Durchsatz: Karten → done, letzte 7 Tage (heute = letzter Slot).
		spark := make([]int, 7)
		done7 := 0
		if rows, err := p.Query(ctx, `
			SELECT (now()::date - at::date) AS tage_zurueck, count(*)
			  FROM portfolio.initiative_event
			 WHERE kind='moved' AND to_stage='done' AND at > now() - interval '7 days'
			 GROUP BY 1
		`); err == nil {
			for rows.Next() {
				var back, n int
				if rows.Scan(&back, &n) == nil && back >= 0 && back < 7 {
					spark[6-back] = n
					done7 += n
				}
			}
			rows.Close()
		}
		out["durchsatz_7d"] = done7
		out["durchsatz_spark"] = spark

		// 2. Moves gesamt + Automatik-Anteil (actor=flow-manager, seit der
		//    GUC-Attribution ehrlich unterscheidbar), letzte 7 Tage.
		var moves7, auto7 int
		_ = p.QueryRow(ctx, `
			SELECT count(*), count(*) FILTER (WHERE actor='flow-manager')
			  FROM portfolio.initiative_event
			 WHERE kind='moved' AND at > now() - interval '7 days'
		`).Scan(&moves7, &auto7)
		out["moves_7d"] = moves7
		out["auto_moves_7d"] = auto7

		// 3. Alter der Arbeit: Verweildauer der aktuellen now-Karten in ihrer
		//    Spalte (Median + Maximum, Tage).
		var nowCount int
		var medianD, maxD float64
		_ = p.QueryRow(ctx, `
			WITH alter_tage AS (
			  SELECT extract(epoch FROM now() - COALESCE(
			    (SELECT max(e.at) FROM portfolio.initiative_event e
			      WHERE e.initiative_id = i.id AND e.kind='moved' AND e.to_stage='now'),
			    i.created_at)) / 86400.0 AS d
			  FROM portfolio.initiative i
			  WHERE i.archived_at IS NULL AND i.stage='now'
			)
			SELECT count(*), COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY d), 0),
			       COALESCE(max(d), 0)
			  FROM alter_tage
		`).Scan(&nowCount, &medianD, &maxD)
		out["now_count"] = nowCount
		out["now_alter_median_tage"] = int(medianD + 0.5)
		out["now_alter_max_tage"] = int(maxD + 0.5)

		// 4. Nachweis-Stau: watching gesamt / davon OHNE Delivery-Beleg.
		var watching, watchingMitBeleg int
		_ = p.QueryRow(ctx, `
			SELECT count(*),
			       count(*) FILTER (WHERE
			         EXISTS(SELECT 1 FROM portfolio.plan_item pi
			                  WHERE pi.initiative_id=i.id AND pi.status IN ('delivered','done'))
			         OR EXISTS(SELECT 1 FROM portfolio.initiative_link l
			                  WHERE l.initiative_id=i.id AND l.kind='plan_file' AND l.ref LIKE '%-delivery.md'))
			  FROM portfolio.initiative i
			 WHERE i.archived_at IS NULL AND i.stage='watching'
		`).Scan(&watching, &watchingMitBeleg)
		out["nachweis_gesamt"] = watching
		out["nachweis_ohne_beleg"] = watching - watchingMitBeleg

		// 5. Befunde + wartende Lane-Entscheidungen.
		var findings, lanePending int
		_ = p.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE klasse='lane-pending')
			FROM portfolio.steward_findings`).Scan(&findings, &lanePending)
		out["findings"] = findings
		out["lane_pending"] = lanePending

		// 6. WIP-Ueberbuchung: Firmen ueber ihrem now-Limit (getWIPLimits =
		//    dieselbe Quelle wie Durchsetzung + Cockpit-Anzeige).
		wipOver := 0
		if rows, err := p.Query(ctx, `
			SELECT firma, count(*) FROM portfolio.initiative
			 WHERE archived_at IS NULL AND stage='now' GROUP BY firma
		`); err == nil {
			for rows.Next() {
				var firma string
				var n int
				if rows.Scan(&firma, &n) == nil {
					if limit, _ := getWIPLimits(firma); limit > 0 && n > limit {
						wipOver++
					}
				}
			}
			rows.Close()
		}
		out["wip_ueber_limit_firmen"] = wipOver

		// 7. Gepinnte Karten (menschliches Stage-Lock).
		var locked int
		_ = p.QueryRow(ctx, `SELECT count(*) FROM portfolio.initiative
			WHERE archived_at IS NULL AND stage_locked_by_human`).Scan(&locked)
		out["gepinnt"] = locked

		// 8. Puls: lebt der Flow-Manager-Sweep?
		var pulsStatus string
		var pulsAgeSec float64
		_ = p.QueryRow(ctx, `SELECT status, extract(epoch FROM now() - last_run)
			FROM portfolio.sage_status WHERE id='flow-manager'`).Scan(&pulsStatus, &pulsAgeSec)
		out["puls_status"] = pulsStatus
		out["puls_alter_sekunden"] = int(pulsAgeSec)

		_ = json.NewEncoder(w).Encode(out)
	}
}
