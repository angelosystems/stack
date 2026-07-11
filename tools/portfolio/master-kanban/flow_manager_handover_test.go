//go:build integration

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

func TestFlowManager_Handover_Integration(t *testing.T) {
	dsn := mkIntegrationDSN(t)

	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skip("skipping integration test; db not reachable:", err)
	}
	defer p.Close()

	if err := p.Ping(ctx); err != nil {
		t.Skip("skipping integration test; db ping failed:", err)
	}

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

	testInitiativeID := "init-flow-handover-test"

	// 2. Clean up old test data
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitiativeID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	// 3. Create test initiative under PORTFOLIO schema
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, title, stage, stage_locked_by_human, firma, primary_backend)
		VALUES ($1, 'Flow Handover Test Card', 'now', false, 'stayawesome', 'plan_file')
	`, testInitiativeID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitiativeID)
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	// 4. Force stagnation by updating updated_at to 10 days ago
	_, err = p.Exec(ctx, `
		UPDATE portfolio.initiative 
		SET updated_at = now() - interval '10 days'
		WHERE id = $1
	`, testInitiativeID)
	if err != nil {
		t.Fatalf("failed to update initiative updated_at: %v", err)
	}

	// We can manually call runFlowManager with dryRun = false
	err = runFlowManager(p, false)
	if err != nil {
		t.Fatalf("runFlowManager failed: %v", err)
	}

	// 5. Verify that the handover sage_action event was logged!
	var count int
	err = p.QueryRow(ctx, `
		SELECT count(*) FROM portfolio.initiative_event 
		WHERE initiative_id = $1 AND kind = 'sage_action' 
		  AND payload->>'action' = 'handover'
	`, testInitiativeID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query logged handover event: %v", err)
	}

	if count != 1 {
		t.Errorf("expected exactly 1 handover event to be logged, got %d", count)
	}

	// Verify that the flow_action event was also logged with the overridden proposed_action='handover'
	var flowActionCount int
	var proposedAction string
	err = p.QueryRow(ctx, `
		SELECT count(*), COALESCE(payload->>'proposed_action', '') 
		FROM portfolio.initiative_event 
		WHERE initiative_id = $1 AND kind = 'flow_action'
		GROUP BY payload->>'proposed_action'
	`, testInitiativeID).Scan(&flowActionCount, &proposedAction)
	if err != nil {
		t.Fatalf("failed to query logged flow_action event: %v", err)
	}

	if flowActionCount != 1 {
		t.Errorf("expected exactly 1 flow_action event to be logged, got %d", flowActionCount)
	}
	if proposedAction != "handover" {
		t.Errorf("expected flow_action event's proposed_action to be 'handover', got %q", proposedAction)
	}
}
