package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
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
	http.DefaultServeMux = http.NewServeMux()
	srvCmd := cmdServe()
	testPort, err := getFreePort()
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
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

func TestManagerSweep_GlmDiagnosisIntegration(t *testing.T) {
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

	testInitID := "st-test-glm-integration-init"

	// Cleanup
	cleanup := func() {
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)
	}
	cleanup()
	defer cleanup()

	// 1. Create a stagnant initiative card in PostgreSQL (stage: now, updated_at 10 days ago)
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, description, created_at, updated_at)
		VALUES ($1, 'solartown', 'now', 'Test Stagnant GLM Card', 'Desc', now() - interval '10 days', now() - interval '10 days')
	`, testInitID)
	if err != nil {
		t.Fatalf("Failed to create test initiative: %v", err)
	}

	// 2. Set environment variables to enable fast stagnation check
	t.Setenv("MANAGER_STAGNATION_THRESHOLD_NOW", "1h")

	// Case A: High Confidence GLM payload
	payloadHigh := map[string]any{
		"category":        "Workspace-gescheitert",
		"confidence":      "High",
		"reasoning":       "The workspace has failed completely with exit status 1.",
		"proposed_action": "handover",
	}
	payloadBytesHigh, _ := json.Marshal(payloadHigh)
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor, at)
		VALUES ($1, 'flow_action', 'flow_manager', $2::jsonb, 'flow-manager', now() - interval '10 days')
	`, testInitID, string(payloadBytesHigh))
	if err != nil {
		t.Fatalf("Failed to insert high confidence flow_action event: %v", err)
	}

	// Run the sweep
	err = runManagerSweep(pPool)
	if err != nil {
		t.Fatalf("runManagerSweep failed: %v", err)
	}

	// Retrieve digest and verify
	var payloadStr string
	err = pPool.QueryRow(ctx, "SELECT payload FROM portfolio.manager_digest WHERE id = 'latest'").Scan(&payloadStr)
	if err != nil {
		t.Fatalf("Failed to fetch manager digest: %v", err)
	}

	var digest ManagerDigest
	if err := json.Unmarshal([]byte(payloadStr), &digest); err != nil {
		t.Fatalf("Failed to parse digest payload: %v", err)
	}

	foundHigh := false
	for _, flag := range digest.Stagnant {
		if flag.InitiativeID == testInitID {
			foundHigh = true
			if flag.Classification != "Stagnation: Workspace-gescheitert" {
				t.Errorf("Expected classification 'Stagnation: Workspace-gescheitert', got %q", flag.Classification)
			}
			if flag.Description != "The workspace has failed completely with exit status 1." {
				t.Errorf("Expected description to match reasoning, got %q", flag.Description)
			}
			// Actions should be present because confidence is High
			if len(flag.Actions) == 0 {
				t.Errorf("Expected actions to be present for High confidence, got 0")
			}
		}
	}
	if !foundHigh {
		t.Errorf("Expected initiative to be flagged under stagnant with High confidence")
	}

	// Cleanup events for Case B
	_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID)

	// Case B: Low Confidence GLM payload -> suppresses proposed actions
	payloadLow := map[string]any{
		"category":        "wartet-auf-Mensch",
		"confidence":      "Low",
		"reasoning":       "No activity detected, probably waiting for user feedback.",
		"proposed_action": "ask owner",
	}
	payloadBytesLow, _ := json.Marshal(payloadLow)
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor, at)
		VALUES ($1, 'flow_action', 'flow_manager', $2::jsonb, 'flow-manager', now() - interval '10 days')
	`, testInitID, string(payloadBytesLow))
	if err != nil {
		t.Fatalf("Failed to insert low confidence flow_action event: %v", err)
	}

	// Run the sweep
	err = runManagerSweep(pPool)
	if err != nil {
		t.Fatalf("runManagerSweep failed: %v", err)
	}

	// Retrieve digest and verify
	err = pPool.QueryRow(ctx, "SELECT payload FROM portfolio.manager_digest WHERE id = 'latest'").Scan(&payloadStr)
	if err != nil {
		t.Fatalf("Failed to fetch manager digest: %v", err)
	}

	if err := json.Unmarshal([]byte(payloadStr), &digest); err != nil {
		t.Fatalf("Failed to parse digest payload: %v", err)
	}

	foundLow := false
	for _, flag := range digest.Stagnant {
		if flag.InitiativeID == testInitID {
			foundLow = true
			if flag.Classification != "Stagnation: wartet-auf-Mensch" {
				t.Errorf("Expected classification 'Stagnation: wartet-auf-Mensch', got %q", flag.Classification)
			}
			if flag.Description != "No activity detected, probably waiting for user feedback." {
				t.Errorf("Expected description to match reasoning, got %q", flag.Description)
			}
			// Actions MUST be suppressed (len(Actions) == 0) because confidence is Low
			if len(flag.Actions) != 0 {
				t.Errorf("Expected actions to be suppressed (0 actions) for Low confidence, got %d actions", len(flag.Actions))
			}
		}
	}
	if !foundLow {
		t.Errorf("Expected initiative to be flagged under stagnant with Low confidence")
	}
}

func TestManagerLiveGeldSchutz(t *testing.T) {
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

	testInitID := "qb-test-livegeld-protection"

	// Cleanup
	cleanup := func() {
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitID)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)
	}
	cleanup()
	defer cleanup()

	// 1. Create a stagnant quantbot initiative card in PostgreSQL (stage: now, updated_at 10 days ago)
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, description, created_at, updated_at)
		VALUES ($1, 'quantbot', 'now', 'Test Stagnant Live Geld Card', 'Desc', now() - interval '10 days', now() - interval '10 days')
	`, testInitID)
	if err != nil {
		t.Fatalf("Failed to create test initiative: %v", err)
	}

	// 2. Set environment variables to enable fast stagnation check
	t.Setenv("MANAGER_STAGNATION_THRESHOLD_NOW", "1h")

	// Run the sweep
	err = runManagerSweep(pPool)
	if err != nil {
		t.Fatalf("runManagerSweep failed: %v", err)
	}

	// 3. Retrieve digest payload and check if our card has only "Eskalieren" action for stagnant flag
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
			if len(flag.Actions) != 1 {
				t.Errorf("Expected stagnant flag to have exactly 1 action (Eskalieren), got %d", len(flag.Actions))
			} else {
				action := flag.Actions[0]
				if action.Label != "Eskalieren" {
					t.Errorf("Expected action label 'Eskalieren', got %q", action.Label)
				}
				if action.Endpoint != "/api/escalate" {
					t.Errorf("Expected endpoint '/api/escalate', got %q", action.Endpoint)
				}
			}
		}
	}
	if !foundStagnant {
		t.Errorf("Expected test initiative to be flagged as stagnant")
	}

	// 4. Test promote-ready live-geld actions
	testBeadID := "bead-livegeld-test"
	sp, _ := solartownPool()
	if sp != nil {
		_, _ = sp.Exec(ctx, "DELETE FROM beads.issues WHERE id = $1", testBeadID)
	}

	testInitID2 := "qb-test-livegeld-promote"
	_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID2)
	_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitID2)
	_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID2)

	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, description, created_at, updated_at)
		VALUES ($1, 'quantbot', 'idea', 'Test Promote Ready Live Geld Card', 'Desc', now(), now())
	`, testInitID2)
	if err != nil {
		t.Fatalf("Failed to create test initiative 2: %v", err)
	}
	defer func() {
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID2)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitID2)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID2)
	}()

	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		VALUES ($1, 'bead', $2)
	`, testInitID2, testBeadID)
	if err != nil {
		t.Fatalf("failed to insert test link: %v", err)
	}

	if sp != nil {
		_, err = sp.Exec(ctx, "INSERT INTO beads.issues (id, rig, title, status) VALUES ($1, 'quantumshift', 'Test Live Geld Issue', 'closed')", testBeadID)
		if err != nil {
			t.Fatalf("failed to insert issue: %v", err)
		}
		defer sp.Exec(ctx, "DELETE FROM beads.issues WHERE id = $1", testBeadID)
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

	foundPromote := false
	for _, flag := range digest.PromoteReady {
		if flag.InitiativeID == testInitID2 {
			foundPromote = true
			if len(flag.Actions) != 1 {
				t.Errorf("Expected promote_ready flag to have exactly 1 action (Eskalieren), got %d", len(flag.Actions))
			} else {
				action := flag.Actions[0]
				if action.Label != "Eskalieren" {
					t.Errorf("Expected action label 'Eskalieren', got %q", action.Label)
				}
				if action.Endpoint != "/api/escalate" {
					t.Errorf("Expected endpoint '/api/escalate', got %q", action.Endpoint)
				}
			}
		}
	}
	if !foundPromote {
		t.Errorf("Expected test initiative 2 to be flagged as promote-ready")
	}

	// 5. Test API escalation endpoint dynamics
	srvCmd := cmdServe()
	testPort := "39822"
	srvCmd.SetArgs([]string{"--port", testPort})
	go func() {
		_ = srvCmd.Execute()
	}()
	// Allow server to boot up
	time.Sleep(300 * time.Millisecond)

	escalatePayload := map[string]string{
		"id":     testInitID,
		"reason": "Test escalation for Live-Geld-Schutz",
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

	// 6. Verify escalation event was logged with is_live_geld = true
	var payloadLoggedStr string
	err = pPool.QueryRow(ctx, `
		SELECT payload::text FROM portfolio.initiative_event 
		WHERE initiative_id = $1 AND kind = 'sage_action' 
		  AND payload->>'action' = 'escalate'
		ORDER BY at DESC LIMIT 1
	`, testInitID).Scan(&payloadLoggedStr)
	if err != nil {
		t.Fatalf("Failed to retrieve escalation event payload: %v", err)
	}

	var loggedPayload struct {
		Action     string `json:"action"`
		IsLiveGeld bool   `json:"is_live_geld"`
	}
	if err := json.Unmarshal([]byte(payloadLoggedStr), &loggedPayload); err != nil {
		t.Fatalf("Failed to parse logged event payload: %v", err)
	}

	if !loggedPayload.IsLiveGeld {
		t.Errorf("Expected is_live_geld to be true, got false")
	}
}
