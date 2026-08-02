package main

// lane_inbox.go — Autopilot Stufe 1, W2 (PRD mk-autopilot-stufe1):
// GET /api/lane-inbox liefert alle wartenden Lane-Entscheidungen
// (steward_findings-Klasse lane-pending) mit einer konkreten Empfehlung:
// Verwalter-Vorschlag (Tag lane-vorschlag, confidence hoch) vor
// View-Heuristik (confidence niedrig). Der Dispatch selbst laeuft
// unveraendert ueber POST /api/dispatch — die Inbox buendelt nur Marios
// Klicks, sie ersetzt sie nicht (LANE_AUTO bleibt off).

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// laneFromEmpfehlung uebersetzt den Heuristik-Text der View in den
// /api/dispatch-Lane-Wert ("" = keine belastbare Empfehlung).
func laneFromEmpfehlung(s string) string {
	switch {
	case strings.HasPrefix(s, "solartown"):
		return "plan"
	case strings.HasPrefix(s, "vibe-kanban"):
		return "hack"
	default:
		return ""
	}
}

// verwalterLaneToDispatch mappt Tag-Werte (lane-vorschlag) auf Dispatch-Lanes.
var verwalterLaneToDispatch = map[string]string{
	"solartown": "plan", "vibe-kanban": "hack", "human": "human", "session": "",
}

func handleLaneInbox(p *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		rows, err := p.Query(r.Context(), `
			SELECT f.initiative_id, f.titel, f.firma, f.stage,
			       COALESCE(f.detail->>'empfehlung',''),
			       COALESCE(f.detail->>'plan_items','0'),
			       COALESCE(t.value,'')
			  FROM portfolio.steward_findings f
			  LEFT JOIN portfolio.initiative_tag t
			         ON t.initiative_id = f.initiative_id AND t.kind = 'lane-vorschlag'
			 WHERE f.klasse = 'lane-pending'
			 ORDER BY f.firma, f.initiative_id
		`)
		if err != nil {
			http.Error(w, `{"error":"lane-inbox-query","hint":"steward_findings nicht lesbar — Migration 030+ vorhanden?"}`, 500)
			return
		}
		defer rows.Close()
		type row struct {
			ID          string `json:"id"`
			Titel       string `json:"titel"`
			Firma       string `json:"firma"`
			Stage       string `json:"stage"`
			Empfehlung  string `json:"empfehlung"`  // Dispatch-Lane: plan|hack|human|"" (unklar)
			Begruendung string `json:"begruendung"` // Heuristik-/Vorschlags-Text
			Confidence  string `json:"confidence"`  // hoch (Verwalter) | niedrig (Heuristik)
			PlanItems   string `json:"plan_items"`
		}
		items := []row{}
		for rows.Next() {
			var it row
			var heuristik, verwalter string
			if rows.Scan(&it.ID, &it.Titel, &it.Firma, &it.Stage, &heuristik, &it.PlanItems, &verwalter) != nil {
				continue
			}
			if v, ok := verwalterLaneToDispatch[verwalter]; ok && v != "" {
				it.Empfehlung, it.Begruendung, it.Confidence = v, "Verwalter-Vorschlag: "+verwalter, "hoch"
			} else {
				it.Empfehlung, it.Begruendung, it.Confidence = laneFromEmpfehlung(heuristik), heuristik, "niedrig"
			}
			items = append(items, it)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "count": len(items)})
	}
}
