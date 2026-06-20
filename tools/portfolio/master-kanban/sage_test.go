package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSageSteward_API(t *testing.T) {
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

	// 1. Initial health clean up
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_status WHERE id = 'sage-steward'")
	_, err = p.Exec(ctx,
		`INSERT INTO portfolio.sage_status (id, last_run, status, error_message)
		 VALUES ('sage-steward', $1, 'healthy', NULL)`,
		time.Now())
	if err != nil {
		t.Fatalf("failed to insert initial sage status: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.sage_status WHERE id = 'sage-steward'")

	// Create test server mux and register handlers
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sage/status", func(w http.ResponseWriter, r *http.Request) {
		var lastRun time.Time
		var status string
		var errMsg *string
		err := p.QueryRow(r.Context(),
			`SELECT last_run, status, error_message FROM portfolio.sage_status WHERE id = 'sage-steward'`).
			Scan(&lastRun, &status, &errMsg)
		if err != nil {
			lastRun = time.Now()
			status = "unknown"
		}

		if time.Since(lastRun) > 30*time.Second {
			status = "alarm"
		}

		dangling, _ := getDanglingWorkspaces()

		resp := map[string]any{
			"last_run":          lastRun,
			"status":            status,
			"error_message":     errMsg,
			"dangling_count":    len(dangling),
			"dangling_baseline": 4,
			"outage_simulated":  sageOutageSimulated,
		}
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/api/sage/simulate-outage", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Simulate bool `json:"simulate"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}

		if body.Simulate {
			sageOutageSimulated = true
			_, _ = p.Exec(r.Context(),
				`UPDATE portfolio.sage_status 
				 SET last_run = now() - interval '10 minutes', status = 'alarm' 
				 WHERE id = 'sage-steward'`)
		} else {
			sageOutageSimulated = false
			_, _ = p.Exec(r.Context(),
				`UPDATE portfolio.sage_status 
				 SET last_run = now(), status = 'healthy', error_message = NULL 
				 WHERE id = 'sage-steward'`)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	// 2. Test GET /api/sage/status - initially healthy
	resp, err := http.Get(server.URL + "/api/sage/status")
	if err != nil {
		t.Fatalf("failed to send GET request: %v", err)
	}
	defer resp.Body.Close()

	var initialData map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&initialData); err != nil {
		t.Fatalf("failed to decode initial GET response: %v", err)
	}

	if initialData["status"] != "healthy" {
		t.Errorf("expected status 'healthy', got %q", initialData["status"])
	}
	if initialData["outage_simulated"] != false {
		t.Errorf("expected outage_simulated to be false")
	}

	// 3. Test POST /api/sage/simulate-outage - activate simulation
	postBody, _ := json.Marshal(map[string]any{"simulate": true})
	postResp, err := http.Post(server.URL+"/api/sage/simulate-outage", "application/json", bytes.NewReader(postBody))
	if err != nil {
		t.Fatalf("failed to send POST simulate-outage request: %v", err)
	}
	defer postResp.Body.Close()

	if postResp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", postResp.StatusCode)
	}

	// 4. Test GET /api/sage/status - should now be in alarm instantly!
	resp2, err := http.Get(server.URL + "/api/sage/status")
	if err != nil {
		t.Fatalf("failed to send second GET request: %v", err)
	}
	defer resp2.Body.Close()

	var simulatedData map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&simulatedData); err != nil {
		t.Fatalf("failed to decode simulated GET response: %v", err)
	}

	if simulatedData["status"] != "alarm" {
		t.Errorf("expected status 'alarm' after simulating outage, got %q", simulatedData["status"])
	}
	if simulatedData["outage_simulated"] != true {
		t.Errorf("expected outage_simulated to be true")
	}

	// 5. Test POST /api/sage/simulate-outage - stop simulation
	postBodyReset, _ := json.Marshal(map[string]any{"simulate": false})
	postRespReset, err := http.Post(server.URL+"/api/sage/simulate-outage", "application/json", bytes.NewReader(postBodyReset))
	if err != nil {
		t.Fatalf("failed to send POST reset request: %v", err)
	}
	defer postRespReset.Body.Close()

	// 6. Test GET /api/sage/status - should be healthy again
	resp3, err := http.Get(server.URL + "/api/sage/status")
	if err != nil {
		t.Fatalf("failed to send third GET request: %v", err)
	}
	defer resp3.Body.Close()

	var recoveredData map[string]any
	if err := json.NewDecoder(resp3.Body).Decode(&recoveredData); err != nil {
		t.Fatalf("failed to decode recovered GET response: %v", err)
	}

	if recoveredData["status"] != "healthy" {
		t.Errorf("expected status 'healthy' after resetting simulate-outage, got %q", recoveredData["status"])
	}
	if recoveredData["outage_simulated"] != false {
		t.Errorf("expected outage_simulated to be false after reset")
	}
}

func TestSageStopAndEscalate(t *testing.T) {
	dsn := os.Getenv("PORTFOLIO_DSN")
	if dsn == "" {
		dsn = "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable"
	}

	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skip("skipping integration test; db not reachable:", err)
		return
	}
	defer p.Close()

	if err := p.Ping(ctx); err != nil {
		t.Skip("skipping integration test; db ping failed:", err)
		return
	}

	// 1. Self-healing/Ensure schema migrations for Sage are applied in the test DB
	_, _ = p.Exec(ctx, "ALTER TABLE portfolio.initiative ADD COLUMN IF NOT EXISTS heal_count integer NOT NULL DEFAULT 0")
	_, _ = p.Exec(ctx, "ALTER TABLE portfolio.initiative_event DROP CONSTRAINT IF EXISTS initiative_event_kind_check")
	_, _ = p.Exec(ctx, `ALTER TABLE portfolio.initiative_event ADD CONSTRAINT initiative_event_kind_check
		CHECK (kind = ANY (ARRAY[
			'created', 'moved', 'edited', 'linked', 'unlinked', 'activity',
			'stage_proposed', 'completed', 'commented', 'archived', 'dispatched',
			'deployed', 'workspace_started', 'ai_message', 'ai_action', 'sage_action'
		]))`)
	_, _ = p.Exec(ctx, `CREATE OR REPLACE VIEW portfolio.sage_escalation_view AS
		SELECT DISTINCT ON (initiative_id)
			id,
			initiative_id,
			kind,
			source_backend,
			payload,
			actor,
			at
		FROM portfolio.initiative_event
		WHERE kind = 'sage_action' AND (payload->>'action') = 'escalate'
		ORDER BY initiative_id, at DESC`)

	// 2. Setup clean test initiatives
	testBeadID := "sa-test-sage-bead"
	testLiveGeldID := "qb-test-sage-livegeld-bead"

	// Clean any old test records
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id IN ($1, $2)", testBeadID, testLiveGeldID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id IN ($1, $2)", testBeadID, testLiveGeldID)

	// Insert test initiative (regular)
	_, err = p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend, heal_count)
		VALUES ($1, 'stayawesome', 'idea', 'Test Regular Bead', 'plan_file', 0)`, testBeadID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}

	// Insert test initiative (live geld / quantbot)
	_, err = p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend, heal_count)
		VALUES ($1, 'quantbot', 'idea', 'Test Live Geld Bead', 'plan_file', 0)`, testLiveGeldID)
	if err != nil {
		t.Fatalf("failed to insert test live-geld initiative: %v", err)
	}

	defer func() {
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id IN ($1, $2)", testBeadID, testLiveGeldID)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id IN ($1, $2)", testBeadID, testLiveGeldID)
	}()

	engine := NewSageDecisionEngine(p, 2) // Default N = 2

	// --- TEST 1: Regular Bead (N=2 retry budget) ---
	// First Failure (Retry 1/2) -> Should heal
	res1, err := engine.ProcessFailure(ctx, testBeadID)
	if err != nil {
		t.Fatalf("ProcessFailure 1 failed: %v", err)
	}
	if res1 != "healed" {
		t.Errorf("expected res1 to be 'healed', got %q", res1)
	}

	var healCount1 int
	_ = p.QueryRow(ctx, "SELECT heal_count FROM portfolio.initiative WHERE id = $1", testBeadID).Scan(&healCount1)
	if healCount1 != 1 {
		t.Errorf("expected heal_count after first failure to be 1, got %d", healCount1)
	}

	// Second Failure (Retry 2/2) -> Should heal
	res2, err := engine.ProcessFailure(ctx, testBeadID)
	if err != nil {
		t.Fatalf("ProcessFailure 2 failed: %v", err)
	}
	if res2 != "healed" {
		t.Errorf("expected res2 to be 'healed', got %q", res2)
	}

	var healCount2 int
	_ = p.QueryRow(ctx, "SELECT heal_count FROM portfolio.initiative WHERE id = $1", testBeadID).Scan(&healCount2)
	if healCount2 != 2 {
		t.Errorf("expected heal_count after second failure to be 2, got %d", healCount2)
	}

	// Third Failure (Retry 3/2 -> Budget Exhausted!) -> Should stop and escalate (SC3)
	res3, err := engine.ProcessFailure(ctx, testBeadID)
	if err != nil {
		t.Fatalf("ProcessFailure 3 failed: %v", err)
	}
	if res3 != "escalated (budget-exhausted)" {
		t.Errorf("expected res3 to be 'escalated (budget-exhausted)', got %q", res3)
	}

	var healCount3 int
	_ = p.QueryRow(ctx, "SELECT heal_count FROM portfolio.initiative WHERE id = $1", testBeadID).Scan(&healCount3)
	if healCount3 != 2 {
		t.Errorf("expected heal_count after third failure to remain 2, got %d", healCount3)
	}

	// Verify events logged
	var eventsCount int
	err = p.QueryRow(ctx, "SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'sage_action'", testBeadID).Scan(&eventsCount)
	if err != nil {
		t.Fatalf("failed to query events count: %v", err)
	}
	if eventsCount != 3 {
		t.Errorf("expected 3 events logged, got %d", eventsCount)
	}

	// Verify latest event was escalation
	var payloadStr string
	err = p.QueryRow(ctx, "SELECT payload::text FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'sage_action' ORDER BY at DESC LIMIT 1", testBeadID).Scan(&payloadStr)
	if err != nil {
		t.Fatalf("failed to query latest event payload: %v", err)
	}

	var action SageAction
	if err := json.Unmarshal([]byte(payloadStr), &action); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if action.Action != "escalate" {
		t.Errorf("expected action to be 'escalate', got %q", action.Action)
	}
	if action.HealCount != 2 {
		t.Errorf("expected action heal count to be 2, got %d", action.HealCount)
	}

	// --- TEST 2: Live Geld Bead (quantbot) ---
	// First Failure -> Should immediately escalate with Live-Geld-Konvention exception
	resLG, err := engine.ProcessFailure(ctx, testLiveGeldID)
	if err != nil {
		t.Fatalf("ProcessFailure for Live-Geld failed: %v", err)
	}
	if resLG != "escalated (live-geld)" {
		t.Errorf("expected live-geld result to be 'escalated (live-geld)', got %q", resLG)
	}

	var healCountLG int
	_ = p.QueryRow(ctx, "SELECT heal_count FROM portfolio.initiative WHERE id = $1", testLiveGeldID).Scan(&healCountLG)
	if healCountLG != 0 {
		t.Errorf("expected live-geld heal_count to remain 0, got %d", healCountLG)
	}

	// Verify latest event is escalation
	var payloadLGStr string
	err = p.QueryRow(ctx, "SELECT payload::text FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'sage_action' ORDER BY at DESC LIMIT 1", testLiveGeldID).Scan(&payloadLGStr)
	if err != nil {
		t.Fatalf("failed to query live-geld event: %v", err)
	}

	var actionLG SageAction
	_ = json.Unmarshal([]byte(payloadLGStr), &actionLG)
	if actionLG.Action != "escalate" || !actionLG.IsLiveGeld {
		t.Errorf("expected action 'escalate' and is_live_geld=true, got %q, %v", actionLG.Action, actionLG.IsLiveGeld)
	}

	// --- TEST 3: Deduplication in sage_escalation_view (R-C) ---
	// Let's log a second escalation for the regular bead to verify deduplication (one per bead)
	err = engine.Escalate(ctx, testBeadID, "Simuliertes Folgeproblem", 2, false)
	if err != nil {
		t.Fatalf("failed to escalate regular bead again: %v", err)
	}

	// Query sage_escalation_view and count entries
	rows, err := p.Query(ctx, "SELECT id, initiative_id FROM portfolio.sage_escalation_view WHERE initiative_id IN ($1, $2)", testBeadID, testLiveGeldID)
	if err != nil {
		t.Fatalf("failed to query sage_escalation_view: %v", err)
	}
	defer rows.Close()

	viewCount := 0
	for rows.Next() {
		var id int64
		var initID string
		if rows.Scan(&id, &initID) == nil {
			viewCount++
		}
	}

	// We expect exactly 2 rows in the view, one for testBeadID and one for testLiveGeldID,
	// because duplicate escalations are grouped/deduplicated per bead (DISTINCT ON initiative_id).
	if viewCount != 2 {
		t.Errorf("expected exactly 2 deduplicated escalation entries in the view, got %d", viewCount)
	}
}
