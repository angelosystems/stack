package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFlowSignals_Enrichment(t *testing.T) {
	dsn := os.Getenv("PORTFOLIO_DSN")
	if dsn == "" {
		dsn = "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable"
	}

	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skip("skipping integration test; db not reachable:", err)
	}
	defer p.Close()

	if err := p.Ping(ctx); err != nil {
		t.Skip("skipping integration test; db ping failed:", err)
	}

	testInitiativeID := "init-flow-test-1"
	testBeadID1 := "bead-flow-test-1"
	testBeadID2 := "bead-flow-test-2"

	// 1. Clean up old test data
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitiativeID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitiativeID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	sp, err := solartownPool()
	if err == nil {
		_, _ = sp.Exec(ctx, "DELETE FROM beads.labels WHERE issue_id IN ($1, $2)", testBeadID1, testBeadID2)
		_, _ = sp.Exec(ctx, "DELETE FROM beads.issues WHERE id IN ($1, $2)", testBeadID1, testBeadID2)
	}

	// 2. Create test initiative card in NOW stage
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, title, stage, stage_locked_by_human, firma, primary_backend)
		VALUES ($1, 'Flow Signals Test Card', 'now', false, 'stayawesome', 'plan_file')
	`, testInitiativeID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitiativeID)
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitiativeID)
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	// 3. Link two test beads
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		VALUES ($1, 'bead', $2), ($1, 'bead', $3)
	`, testInitiativeID, testBeadID1, testBeadID2)
	if err != nil {
		t.Fatalf("failed to insert test links: %v", err)
	}

	// 4. Set bead statuses in beads database (one closed, one open)
	if sp != nil {
		_, err = sp.Exec(ctx, "INSERT INTO beads.issues (id, rig, title, status) VALUES ($1, 'stayawesomeOS', 'Flow Test Issue 1', 'closed')", testBeadID1)
		if err != nil {
			t.Fatalf("failed to insert issue 1: %v", err)
		}
		defer sp.Exec(ctx, "DELETE FROM beads.issues WHERE id IN ($1, $2)", testBeadID1, testBeadID2)

		_, err = sp.Exec(ctx, "INSERT INTO beads.issues (id, rig, title, status) VALUES ($1, 'stayawesomeOS', 'Flow Test Issue 2', 'open')", testBeadID2)
		if err != nil {
			t.Fatalf("failed to insert issue 2: %v", err)
		}
	}

	// 5. Query /api/initiatives endpoint
	req, err := http.NewRequest("GET", "/api/initiatives", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	rr := httptest.NewRecorder()
	pool = p

	// Invoke the cmdServe handler wrapper function for testing
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows, err := p.Query(r.Context(), `SELECT row_to_json(s) FROM portfolio.initiative_summary s ORDER BY firma, stage, id`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()

		var items []map[string]any
		for rows.Next() {
			var j []byte
			if err := rows.Scan(&j); err != nil {
				continue
			}
			var item map[string]any
			if err := json.Unmarshal(j, &item); err == nil {
				items = append(items, item)
			}
		}

		if len(items) > 0 {
			enrichLane(r.Context(), p, items)
			enrichFlowSignals(r.Context(), p, items)
		}

		w.Header().Set("Content-Type", "application/json")
		if items == nil {
			items = []map[string]any{}
		}
		json.NewEncoder(w).Encode(items)
	})

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var items []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	var targetCard map[string]any
	for _, item := range items {
		if item["id"] == testInitiativeID {
			targetCard = item
			break
		}
	}

	if targetCard == nil {
		t.Fatalf("target initiative %s not found in /api/initiatives response", testInitiativeID)
	}

	// 6. Verify lane (fallback)
	if targetCard["lane"] == "" {
		t.Errorf("lane should be populated")
	}

	// 7. Verify all four flow signals are present
	flow, ok := targetCard["flow_signals"].(map[string]any)
	if !ok {
		t.Fatalf("flow_signals field missing or invalid in response: %v", targetCard)
	}

	// Signal 1: Zeit-in-Stage
	tSec, ok1 := flow["time_in_stage_seconds"].(float64)
	tStr, ok2 := flow["time_in_stage"].(string)
	if !ok1 || !ok2 {
		t.Errorf("time_in_stage_seconds or time_in_stage missing from flow_signals")
	} else {
		t.Logf("Time in stage: %.0f seconds (%s)", tSec, tStr)
		if tSec < 0 {
			t.Errorf("time_in_stage_seconds cannot be negative, got %.0f", tSec)
		}
	}

	// Signal 2: Aktivitäts-Stille
	sSec, ok1 := flow["activity_silence_seconds"].(float64)
	sStr, ok2 := flow["activity_silence"].(string)
	if !ok1 || !ok2 {
		t.Errorf("activity_silence_seconds or activity_silence missing from flow_signals")
	} else {
		t.Logf("Activity silence: %.0f seconds (%s)", sSec, sStr)
		if sSec < 0 {
			t.Errorf("activity_silence_seconds cannot be negative, got %.0f", sSec)
		}
	}

	// Signal 3: Bead-Fortschritt
	closed, ok1 := flow["bead_progress_closed"].(float64)
	total, ok2 := flow["bead_progress_total"].(float64)
	pct, ok3 := flow["bead_progress_percentage"].(float64)
	progStr, ok4 := flow["bead_progress_str"].(string)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		t.Errorf("bead progress fields missing from flow_signals")
	} else {
		if sp != nil {
			if closed != 1 || total != 2 {
				t.Errorf("expected bead progress to be 1/2, got %.0f/%.0f", closed, total)
			}
			if pct != 50.0 {
				t.Errorf("expected bead progress percentage to be 50.0, got %.1f", pct)
			}
			if progStr != "1/2" {
				t.Errorf("expected bead progress string to be '1/2', got %q", progStr)
			}
		}
	}

	// Signal 4: WIP-vs-Limit
	cardsInNow, ok1 := flow["wip_cards_in_now"].(float64)
	limit, ok2 := flow["wip_limit"].(float64)
	wipStr, ok3 := flow["wip_vs_limit_str"].(string)
	if !ok1 || !ok2 || !ok3 {
		t.Errorf("wip_vs_limit fields missing from flow_signals")
	} else {
		t.Logf("Cards in NOW: %.0f, Limit: %.0f, String: %s", cardsInNow, limit, wipStr)
		if cardsInNow < 1 {
			t.Errorf("expected cards_in_now to be at least 1, got %.0f", cardsInNow)
		}
	}

	// Verify top-level duplicate fields are present too
	if targetCard["time_in_stage_seconds"] == nil || targetCard["time_in_stage"] == nil {
		t.Errorf("time_in_stage fields missing from top-level")
	}
	if targetCard["activity_silence_seconds"] == nil || targetCard["activity_silence"] == nil {
		t.Errorf("activity_silence fields missing from top-level")
	}
	if targetCard["bead_progress_closed"] == nil || targetCard["bead_progress_total"] == nil {
		t.Errorf("bead progress fields missing from top-level")
	}
	if targetCard["wip_cards_in_now"] == nil || targetCard["wip_limit"] == nil {
		t.Errorf("wip fields missing from top-level")
	}
}
