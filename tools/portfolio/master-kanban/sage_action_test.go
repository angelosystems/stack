package main

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSageDryRun_SC4(t *testing.T) {
	testDSN := os.Getenv("PORTFOLIO_DSN")
	if testDSN == "" {
		testDSN = "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable"
	}

	// Assign the package-level variable dsn so connect() uses it
	dsn = testDSN

	ctx := context.Background()
	p, err := pgxpool.New(ctx, testDSN)
	if err != nil {
		t.Skip("skipping integration test; db not reachable:", err)
	}
	defer p.Close()

	if err := p.Ping(ctx); err != nil {
		t.Skip("skipping integration test; db ping failed:", err)
	}

	// 1. Clean up any existing sage_action events for the 3 target workspaces
	targetWSIDs := []string{
		"05021F1F765846E299B6A36B39DC39F8", // sol-st-yozd
		"64D07879DB694345BFA59E9D321AAC08", // sol-st-1bpf
		"B842765043A04994B61AACF51E019956", // sol-st-ib5e
	}

	for _, wsID := range targetWSIDs {
		_, err = p.Exec(ctx, `
			DELETE FROM portfolio.initiative_event 
			WHERE kind = 'sage_action' AND (payload->>'workspace_id') = $1
		`, wsID)
		if err != nil {
			t.Fatalf("failed to clean up old sage_action events: %v", err)
		}
	}

	// Ensure pool is reset so cmdSage connects with the correct dsn
	pool = nil

	// 2. Execute cmdSage programmatically (dry-run mode)
	cmd := cmdSage()
	cmd.SetArgs([]string{}) // Dry-run
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("failed to execute sage command: %v", err)
	}

	// 3. Verify that the board events (sage_action) were logged for each of the 3 target workspaces
	for _, wsID := range targetWSIDs {
		var exists bool
		err = p.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM portfolio.initiative_event 
				WHERE kind = 'sage_action' AND (payload->>'workspace_id') = $1
			)
		`, wsID).Scan(&exists)
		if err != nil {
			t.Fatalf("failed to check logged board event: %v", err)
		}

		if !exists {
			t.Errorf("expected board-event (sage_action) to be logged for workspace %s, but none was found", wsID)
		} else {
			t.Logf("✓ Verified: Board-Event (sage_action) logged for workspace %s", wsID)
		}
	}
}
