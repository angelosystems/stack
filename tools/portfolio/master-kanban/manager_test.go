package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestManagerSweepAndEscalate(t *testing.T) {
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

	testInitID := "st-test-manager-initiative"

	// Cleanup
	cleanup := func() {
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)
	}
	cleanup()
	defer cleanup()

	// 1. Create a dummy initiative card in PostgreSQL
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, description, created_at, updated_at)
		VALUES ($1, 'solartown', 'idea', 'Test Manager Stale Card', 'Desc', now() - interval '40 days', now() - interval '40 days')
	`, testInitID)
	if err != nil {
		t.Fatalf("Failed to create test initiative: %v", err)
	}

	// 2. Set environment variables to enable fast stagnation / stale check in testing
	t.Setenv("MANAGER_STALE_THRESHOLD_IDEA", "24h")

	// 3. Run the manager sweep
	err = runManagerSweep(pPool)
	if err != nil {
		t.Fatalf("runManagerSweep failed: %v", err)
	}

	// 4. Retrieve digest payload and check if our card is categorized as stale with our actions
	var payloadStr string
	err = pPool.QueryRow(ctx, "SELECT payload FROM portfolio.manager_digest WHERE id = 'latest'").Scan(&payloadStr)
	if err != nil {
		t.Fatalf("Failed to fetch manager digest: %v", err)
	}

	t.Logf("MANAGER DIGEST PAYLOAD: %s", payloadStr)

	var digest ManagerDigest
	if err := json.Unmarshal([]byte(payloadStr), &digest); err != nil {
		t.Fatalf("Failed to parse digest payload: %v", err)
	}

	foundStale := false
	for _, flag := range digest.Stale {
		if flag.InitiativeID == testInitID {
			foundStale = true
			if len(flag.Actions) == 0 {
				t.Errorf("Expected stale flag to have actions, got 0")
			}
			// Check one of the actions
			hasReview := false
			for _, action := range flag.Actions {
				if action.Label == "Review" {
					hasReview = true
					if action.Endpoint != "/api/comment" {
						t.Errorf("Expected endpoint /api/comment, got %s", action.Endpoint)
					}
				}
			}
			if !hasReview {
				t.Errorf("Expected stale actions to include Review")
			}
		}
	}
	if !foundStale {
		t.Errorf("Expected test initiative to be flagged as stale")
	}

	// 5. Test the POST /api/escalate endpoint using httptest
	srvCmd := cmdServe()
	testPort := "39821"
	srvCmd.SetArgs([]string{"--port", testPort})
	go func() {
		_ = srvCmd.Execute()
	}()
	// Allow server to boot up
	time.Sleep(300 * time.Millisecond)

	escalatePayload := map[string]string{
		"id":     testInitID,
		"reason": "Test escalation for manager test",
	}
	pBytes, _ := json.Marshal(escalatePayload)
	req, _ := http.NewRequest("POST", "http://localhost:"+testPort+"/api/escalate", bytes.NewReader(pBytes))
	req.Header.Set("Content-Type", "application/json")

	cl := &http.Client{Timeout: 2 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("POST to escalate endpoint failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected escalate endpoint to return status 200, got %d", resp.StatusCode)
	}

	// 6. Verify escalation event was logged in database
	var count int
	err = pPool.QueryRow(ctx, `
		SELECT count(*) FROM portfolio.initiative_event 
		WHERE initiative_id = $1 AND kind = 'sage_action' 
		  AND payload->>'action' = 'escalate' 
		  AND payload->>'reason' = 'Test escalation for manager test'
	`, testInitID).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to check if escalation event was logged: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected exactly 1 escalation event, got %d", count)
	}
}

func TestManagerEscalationLadder(t *testing.T) {
	portfolioDsn := envOr("PORTFOLIO_DSN", "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable")
	ctx := context.Background()

	pPool, err := pgxpool.New(ctx, portfolioDsn)
	if err != nil {
		t.Fatalf("Failed to connect to portfolio DB: %v", err)
	}
	defer pPool.Close()

	testInitID := "st-test-escalation-ladder-initiative"
	testBeadID := "st-test-escalation-bead"

	// Cleanup
	cleanup := func() {
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitID)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.sage_lease WHERE bead_id = $1", testBeadID)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.sage_heal_count WHERE bead_id = $1", testBeadID)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)
	}
	cleanup()
	defer cleanup()

	// 1. Create a dummy initiative card in PostgreSQL
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, description, created_at, updated_at)
		VALUES ($1, 'solartown', 'now', 'Test Escalation Ladder Card', 'Desc', now() - interval '4 days', now() - interval '4 days')
	`, testInitID)
	if err != nil {
		t.Fatalf("Failed to create test initiative: %v", err)
	}

	// Link initiative to bead
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative_link (initiative_id, ref, kind)
		VALUES ($1, $2, 'bead')
	`, testInitID, testBeadID)
	if err != nil {
		t.Fatalf("Failed to link test initiative to bead: %v", err)
	}

	// Set thresholds to enable fast stagnation check in testing
	t.Setenv("MANAGER_STAGNATION_THRESHOLD_NOW", "1h")

	// --- CASE A: No lower layers engaged (should flag as stagnant) ---
	err = runManagerSweep(pPool)
	if err != nil {
		t.Fatalf("runManagerSweep failed: %v", err)
	}

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
		}
	}
	if !foundStagnant {
		t.Errorf("CASE A: Expected initiative to be flagged as stagnant, but it was not")
	}

	// --- CASE B: Lower layer engaged - Reactor/Dispatch (recent dispatched event) ---
	cleanup()
	// Re-create initiative and link
	_, _ = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, description, created_at, updated_at)
		VALUES ($1, 'solartown', 'now', 'Test Escalation Ladder Card', 'Desc', now() - interval '4 days', now() - interval '4 days')
	`, testInitID)
	_, _ = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative_link (initiative_id, ref, kind)
		VALUES ($1, $2, 'bead')
	`, testInitID, testBeadID)

	// Insert recent dispatched event (within 10 minutes)
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
		VALUES ($1, 'dispatched', 'plan_file', '{"lane":"plan"}'::jsonb, 'test-reactor')
	`, testInitID)
	if err != nil {
		t.Fatalf("Failed to log dispatch event: %v", err)
	}

	err = runManagerSweep(pPool)
	if err != nil {
		t.Fatalf("runManagerSweep failed: %v", err)
	}

	err = pPool.QueryRow(ctx, "SELECT payload FROM portfolio.manager_digest WHERE id = 'latest'").Scan(&payloadStr)
	if err != nil {
		t.Fatalf("Failed to fetch manager digest: %v", err)
	}
	if err := json.Unmarshal([]byte(payloadStr), &digest); err != nil {
		t.Fatalf("Failed to parse digest payload: %v", err)
	}

	foundStagnant = false
	for _, flag := range digest.Stagnant {
		if flag.InitiativeID == testInitID {
			foundStagnant = true
		}
	}
	if foundStagnant {
		t.Errorf("CASE B: Expected initiative NOT to be stagnant because of recent dispatch (Reactor attempt), but it was flagged")
	}

	// --- CASE C: Lower layer engaged - vk-Sage's queue (active lease) ---
	cleanup()
	// Re-create initiative and link
	_, _ = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, description, created_at, updated_at)
		VALUES ($1, 'solartown', 'now', 'Test Escalation Ladder Card', 'Desc', now() - interval '4 days', now() - interval '4 days')
	`, testInitID)
	_, _ = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative_link (initiative_id, ref, kind)
		VALUES ($1, $2, 'bead')
	`, testInitID, testBeadID)

	// Create active sage_lease
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.sage_lease (bead_id, locked_until, locked_by, heal_counter, updated_at)
		VALUES ($1, now() + interval '5 minutes', 'vk-sage', 1, now())
	`, testBeadID)
	if err != nil {
		t.Fatalf("Failed to create sage lease: %v", err)
	}

	err = runManagerSweep(pPool)
	if err != nil {
		t.Fatalf("runManagerSweep failed: %v", err)
	}

	err = pPool.QueryRow(ctx, "SELECT payload FROM portfolio.manager_digest WHERE id = 'latest'").Scan(&payloadStr)
	if err != nil {
		t.Fatalf("Failed to fetch manager digest: %v", err)
	}
	if err := json.Unmarshal([]byte(payloadStr), &digest); err != nil {
		t.Fatalf("Failed to parse digest payload: %v", err)
	}

	foundStagnant = false
	for _, flag := range digest.Stagnant {
		if flag.InitiativeID == testInitID {
			foundStagnant = true
		}
	}
	if foundStagnant {
		t.Errorf("CASE C: Expected initiative NOT to be stagnant because of active vk-Sage lease, but it was flagged")
	}

	// --- CASE D: Lower layer engaged - vk-Sage's queue (healing retry < 2, no escalation yet) ---
	cleanup()
	// Re-create initiative and link
	_, _ = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, description, created_at, updated_at)
		VALUES ($1, 'solartown', 'now', 'Test Escalation Ladder Card', 'Desc', now() - interval '4 days', now() - interval '4 days')
	`, testInitID)
	_, _ = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative_link (initiative_id, ref, kind)
		VALUES ($1, $2, 'bead')
	`, testInitID, testBeadID)

	// Create a sage_heal_count of 1
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.sage_heal_count (bead_id, heal_count, escalated, updated_at)
		VALUES ($1, 1, false, now())
	`, testBeadID)
	if err != nil {
		t.Fatalf("Failed to create sage heal count: %v", err)
	}

	err = runManagerSweep(pPool)
	if err != nil {
		t.Fatalf("runManagerSweep failed: %v", err)
	}

	err = pPool.QueryRow(ctx, "SELECT payload FROM portfolio.manager_digest WHERE id = 'latest'").Scan(&payloadStr)
	if err != nil {
		t.Fatalf("Failed to fetch manager digest: %v", err)
	}
	if err := json.Unmarshal([]byte(payloadStr), &digest); err != nil {
		t.Fatalf("Failed to parse digest payload: %v", err)
	}

	foundStagnant = false
	for _, flag := range digest.Stagnant {
		if flag.InitiativeID == testInitID {
			foundStagnant = true
		}
	}
	if foundStagnant {
		t.Errorf("CASE D: Expected initiative NOT to be stagnant because of active vk-Sage retries (heal_count = 1), but it was flagged")
	}

	// --- CASE E: Lower layer NOT engaged - vk-Sage has escalated (heal_count >= 2, escalation logged) ---
	cleanup()
	// Re-create initiative and link
	_, _ = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, description, created_at, updated_at)
		VALUES ($1, 'solartown', 'now', 'Test Escalation Ladder Card', 'Desc', now() - interval '4 days', now() - interval '4 days')
	`, testInitID)
	_, _ = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative_link (initiative_id, ref, kind)
		VALUES ($1, $2, 'bead')
	`, testInitID, testBeadID)

	// Create sage_heal_count of 2
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.sage_heal_count (bead_id, heal_count, escalated, updated_at)
		VALUES ($1, 2, false, now())
	`, testBeadID)
	if err != nil {
		t.Fatalf("Failed to create sage heal count: %v", err)
	}

	// Log vk-Sage escalation event
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor, at)
		VALUES ($1, 'sage_action', 'sage', '{"action":"escalate"}'::jsonb, 'vk-sage', now() - interval '4 days')
	`, testInitID)
	if err != nil {
		t.Fatalf("Failed to log escalation event: %v", err)
	}

	err = runManagerSweep(pPool)
	if err != nil {
		t.Fatalf("runManagerSweep failed: %v", err)
	}

	err = pPool.QueryRow(ctx, "SELECT payload FROM portfolio.manager_digest WHERE id = 'latest'").Scan(&payloadStr)
	if err != nil {
		t.Fatalf("Failed to fetch manager digest: %v", err)
	}
	if err := json.Unmarshal([]byte(payloadStr), &digest); err != nil {
		t.Fatalf("Failed to parse digest payload: %v", err)
	}

	foundStagnant = false
	for _, flag := range digest.Stagnant {
		if flag.InitiativeID == testInitID {
			foundStagnant = true
		}
	}
	if !foundStagnant {
		t.Errorf("CASE E: Expected initiative to be stagnant because vk-Sage has already escalated (completed its retries), but it was not")
	}
}

func TestManagerGLMDiagnosis(t *testing.T) {
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

	testInitID := "st-test-glm-diag-initiative"

	// Cleanup
	cleanup := func() {
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)
	}
	cleanup()
	defer cleanup()

	// Create test card in postgres
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, description, created_at, updated_at)
		VALUES ($1, 'solartown', 'idea', 'Test GLM Diag Card', 'Desc', now() - interval '40 days', now() - interval '40 days')
	`, testInitID)
	if err != nil {
		t.Fatalf("Failed to create test initiative: %v", err)
	}

	t.Setenv("MANAGER_STALE_THRESHOLD_IDEA", "24h")
	t.Setenv("ZAI_KEY", "dummy-test-key")

	// Set up mock GLM Server that returns low confidence to suppress actions
	mockResponse := `{
		"content": [
			{
				"type": "text",
				"text": "{\"category\":\"verlassen\", \"justification\":\"The task has been completely abandoned due to zero activity.\", \"confidence\":\"low\"}"
			}
		]
	}`

	mockGLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(mockResponse))
	}))
	defer mockGLM.Close()

	t.Setenv("REVIEWER_BASE_URL", mockGLM.URL)

	// Run the manager sweep
	err = runManagerSweep(pPool)
	if err != nil {
		t.Fatalf("runManagerSweep failed: %v", err)
	}

	// Fetch digest and assert
	var payloadStr string
	err = pPool.QueryRow(ctx, "SELECT payload FROM portfolio.manager_digest WHERE id = 'latest'").Scan(&payloadStr)
	if err != nil {
		t.Fatalf("Failed to fetch manager digest: %v", err)
	}

	var digest ManagerDigest
	if err := json.Unmarshal([]byte(payloadStr), &digest); err != nil {
		t.Fatalf("Failed to parse digest payload: %v", err)
	}

	found := false
	for _, flag := range digest.Stale {
		if flag.InitiativeID == testInitID {
			found = true
			if flag.Classification != "Veraltet (Backlog-Fäule): verlassen" {
				t.Errorf("Expected classification 'Veraltet (Backlog-Fäule): verlassen', got '%s'", flag.Classification)
			}
			if !strings.Contains(flag.Description, "The task has been completely abandoned") {
				t.Errorf("Expected description to contain justification, got '%s'", flag.Description)
			}
			// Low confidence should suppress actions
			if len(flag.Actions) != 0 {
				t.Errorf("Expected actions to be suppressed (len 0) for low confidence, got %d", len(flag.Actions))
			}
		}
	}
	if !found {
		t.Errorf("Expected to find diagnosed card in stale flags")
	}
}

