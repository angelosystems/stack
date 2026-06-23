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
