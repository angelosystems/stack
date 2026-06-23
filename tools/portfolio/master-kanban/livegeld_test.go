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

func TestLiveGeldSchutz_StagnationAndPromoteReady(t *testing.T) {
	portfolioDsn := envOr("PORTFOLIO_DSN", "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable")
	ctx := context.Background()

	pPool, err := pgxpool.New(ctx, portfolioDsn)
	if err != nil {
		t.Fatalf("Failed to connect to portfolio DB: %v", err)
	}
	defer pPool.Close()

	// Swap global pool
	oldPool := pool
	pool = pPool
	defer func() {
		pool = oldPool
	}()

	testInitID := "qb-test-live-geld-init"
	testBeadID := "bead-live-geld-test-1"

	sp, err := solartownPool()
	if err != nil {
		t.Logf("Warning: solartownPool returned error: %v", err)
	}

	// Clean up old test data
	cleanup := func() {
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitID)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)
		if sp != nil {
			_, _ = sp.Exec(ctx, "DELETE FROM beads.issues WHERE id = $1", testBeadID)
		}
	}
	cleanup()
	defer cleanup()

	// 1. Create a stagnant quantbot (Live-Geld) initiative card in PostgreSQL
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, description, created_at, updated_at)
		VALUES ($1, 'quantbot', 'now', 'Live Geld Stagnant Test Initiative', 'Desc', now() - interval '10 days', now() - interval '10 days')
	`, testInitID)
	if err != nil {
		t.Fatalf("Failed to create test initiative: %v", err)
	}

	// 2. We set environment variables to enable fast stagnation check in testing
	t.Setenv("MANAGER_STAGNATION_THRESHOLD_NOW", "1h")

	// 3. Run the manager sweep
	err = runManagerSweep(pPool)
	if err != nil {
		t.Fatalf("runManagerSweep failed: %v", err)
	}

	// 4. Retrieve digest payload and check if our card is stagnant with ONLY the "Eskalieren" action
	var payloadStr string
	err = pPool.QueryRow(ctx, "SELECT payload FROM portfolio.manager_digest WHERE id = 'latest'").Scan(&payloadStr)
	if err != nil {
		t.Fatalf("Failed to fetch manager digest: %v", err)
	}

	var digest ManagerDigest
	if err := json.Unmarshal([]byte(payloadStr), &digest); err != nil {
		t.Fatalf("Failed to parse digest payload: %v", err)
	}

	foundStagnant := false
	for _, flag := range digest.Stagnant {
		if flag.InitiativeID == testInitID {
			foundStagnant = true
			if flag.Firma != "quantbot" {
				t.Errorf("Expected flag company to be quantbot, got %s", flag.Firma)
			}
			if len(flag.Actions) != 1 {
				t.Fatalf("Expected stagnant flag to have exactly 1 action (Eskalieren), got %d", len(flag.Actions))
			}
			if flag.Actions[0].Label != "Eskalieren" {
				t.Errorf("Expected the only action to be 'Eskalieren', got %q", flag.Actions[0].Label)
			}
		}
	}
	if !foundStagnant {
		t.Errorf("Expected test initiative to be flagged as stagnant")
	}

	// Now clean up and test Promote-Reif Live-Geld-Schutz
	cleanup()

	// 1. Create a quantbot (Live-Geld) initiative card in PostgreSQL
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, description, created_at, updated_at)
		VALUES ($1, 'quantbot', 'now', 'Live Geld Promote-Reif Test Initiative', 'Desc', now(), now())
	`, testInitID)
	if err != nil {
		t.Fatalf("Failed to create test initiative: %v", err)
	}

	// 2. Link test bead to the initiative
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		VALUES ($1, 'bead', $2)
	`, testInitID, testBeadID)
	if err != nil {
		t.Fatalf("failed to insert test link: %v", err)
	}

	// 3. Create the bead in Dolt and mark it closed
	if sp != nil {
		_, err = sp.Exec(ctx, "INSERT INTO beads.issues (id, rig, title, status) VALUES ($1, 'stayawesomeOS', 'Test Issue 1', 'closed')", testBeadID)
		if err != nil {
			t.Fatalf("failed to insert issue in Dolt: %v", err)
		}
	}

	// 4. Run the manager sweep
	err = runManagerSweep(pPool)
	if err != nil {
		t.Fatalf("runManagerSweep failed: %v", err)
	}

	// 5. Retrieve digest payload and check if our card is promote_ready with ONLY the "Eskalieren" action
	err = pPool.QueryRow(ctx, "SELECT payload FROM portfolio.manager_digest WHERE id = 'latest'").Scan(&payloadStr)
	if err != nil {
		t.Fatalf("Failed to fetch manager digest: %v", err)
	}

	if err := json.Unmarshal([]byte(payloadStr), &digest); err != nil {
		t.Fatalf("Failed to parse digest payload: %v", err)
	}

	foundPromoteReady := false
	for _, flag := range digest.PromoteReady {
		if flag.InitiativeID == testInitID {
			foundPromoteReady = true
			if flag.Firma != "quantbot" {
				t.Errorf("Expected flag company to be quantbot, got %s", flag.Firma)
			}
			if len(flag.Actions) != 1 {
				t.Fatalf("Expected promote_ready flag to have exactly 1 action (Eskalieren), got %d", len(flag.Actions))
			}
			if flag.Actions[0].Label != "Eskalieren" {
				t.Errorf("Expected the only action to be 'Eskalieren', got %q", flag.Actions[0].Label)
			}
			if flag.Actions[0].Payload["reason"] != "Eskalation wegen Promote-Reife (Live-Geld-Schutz)" {
				t.Errorf("Expected specific Live-Geld escalation reason, got %q", flag.Actions[0].Payload["reason"])
			}
		}
	}
	if !foundPromoteReady {
		t.Errorf("Expected test initiative to be flagged as promote_ready")
	}
}

func TestFlowManager_LiveGeldSchutz_Overriding(t *testing.T) {
	portfolioDsn := envOr("PORTFOLIO_DSN", "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable")
	ctx := context.Background()

	pPool, err := pgxpool.New(ctx, portfolioDsn)
	if err != nil {
		t.Fatalf("Failed to connect to portfolio DB: %v", err)
	}
	defer pPool.Close()

	// Swap global pool
	oldPool := pool
	pool = pPool
	defer func() {
		pool = oldPool
	}()

	// 1. Mock the GLM/LLM endpoint
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		response := map[string]any{
			"content": []map[string]any{
				{
					"type": "text",
					"text": `{"category": "Workspace-gescheitert", "confidence": "High", "reasoning": "Workspace failed with exit code 1.", "proposed_action": "re-dispatch"}`,
				},
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer mockServer.Close()

	// Override env vars
	os.Setenv("REVIEWER_BASE_URL", mockServer.URL)
	os.Setenv("ZAI_KEY", "mock-key")
	defer func() {
		os.Unsetenv("REVIEWER_BASE_URL")
		os.Unsetenv("ZAI_KEY")
	}()

	testInitID := "qb-test-flow-manager-init"

	// Cleanup
	cleanup := func() {
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)
	}
	cleanup()
	defer cleanup()

	// 1. Create a stagnant quantbot (Live-Geld) initiative card in PostgreSQL
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, description, created_at, updated_at)
		VALUES ($1, 'quantbot', 'now', 'Live Geld Flow Manager Test Initiative', 'Desc', now() - interval '10 days', now() - interval '10 days')
	`, testInitID)
	if err != nil {
		t.Fatalf("Failed to create test initiative: %v", err)
	}

	// 2. Set environment variables to enable fast stagnation check in testing
	t.Setenv("MANAGER_STAGNATION_THRESHOLD_NOW", "1h")

	// 3. We run the Flow-Manager via runFlowManager
	// We run it with dryRun=false to verify events logged in DB
	err = runFlowManager(pPool, false)
	if err != nil {
		t.Fatalf("runFlowManager failed: %v", err)
	}

	// 4. Verify that the flow_action event has been logged and the proposed_action has been overridden to 'escalate'
	var payloadStr string
	err = pPool.QueryRow(ctx, `
		SELECT payload::text FROM portfolio.initiative_event 
		WHERE initiative_id = $1 AND kind = 'flow_action'
		ORDER BY at DESC LIMIT 1
	`, testInitID).Scan(&payloadStr)
	if err != nil {
		t.Fatalf("Failed to fetch flow_action event payload: %v", err)
	}

	var payloadMap map[string]any
	if err := json.Unmarshal([]byte(payloadStr), &payloadMap); err != nil {
		t.Fatalf("Failed to parse flow_action event payload: %v", err)
	}

	proposedAction, _ := payloadMap["proposed_action"].(string)
	if proposedAction != "escalate" {
		t.Errorf("Expected proposed_action to be 'escalate' for Live-Geld, got %q", proposedAction)
	}

	// 5. Verify that if category was Workspace-gescheitert, a sage_action of action=escalate is logged instead of handover
	var sageActionPayload string
	err = pPool.QueryRow(ctx, `
		SELECT payload::text FROM portfolio.initiative_event 
		WHERE initiative_id = $1 AND kind = 'sage_action'
		ORDER BY at DESC LIMIT 1
	`, testInitID).Scan(&sageActionPayload)
	if err != nil {
		t.Fatalf("Failed to fetch sage_action event payload: %v", err)
	}

	var sagePayloadMap map[string]any
	if err := json.Unmarshal([]byte(sageActionPayload), &sagePayloadMap); err != nil {
		t.Fatalf("Failed to parse sage_action event payload: %v", err)
	}

	actionType, _ := sagePayloadMap["action"].(string)
	if actionType != "escalate" {
		t.Errorf("Expected sage_action to log action='escalate' for Live-Geld, got %q", actionType)
	}
}
