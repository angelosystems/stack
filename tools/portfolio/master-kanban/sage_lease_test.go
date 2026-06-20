package main

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestExecuteSageAction_Concurrency verifies that when two Sage cycles
// try to act on the same dead workspace concurrently, only one succeeds
// in acquiring the lease and incrementing the heal counter, while the other is skipped.
func TestExecuteSageAction_Concurrency(t *testing.T) {
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

	testBead := "test-concurrency-bead-123"
	testWS := "test-concurrency-ws-123"

	// 1. Clean up existing test state to start clean
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_lease WHERE bead_id = $1", testBead)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_heal_count WHERE bead_id = $1", testBead)

	defer func() {
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_lease WHERE bead_id = $1", testBead)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_heal_count WHERE bead_id = $1", testBead)
	}()

	// 2. We will run 2 concurrent attempts to execute a Sage action on the same bead
	var wg sync.WaitGroup
	wg.Add(2)

	// Keep track of which execution succeeded
	acquiredChan := make(chan bool, 2)
	actionExecutedCount := 0
	var mu sync.Mutex

	actionFn := func(tx pgx.Tx, healCount int) error {
		mu.Lock()
		actionExecutedCount++
		mu.Unlock()
		return nil
	}

	// Trigger concurrently
	for i := 0; i < 2; i++ {
		go func(actorName string) {
			defer wg.Done()
			acquired, err := ExecuteSageAction(ctx, p, testBead, testWS, actorName, actionFn)
			if err != nil {
				t.Errorf("error during ExecuteSageAction: %v", err)
				return
			}
			acquiredChan <- acquired
		}(string(rune('A' + i))) // actor names "A", "B"
	}

	wg.Wait()
	close(acquiredChan)

	// 3. Evaluate results
	acquiredCount := 0
	skippedCount := 0
	for acq := range acquiredChan {
		if acq {
			acquiredCount++
		} else {
			skippedCount++
		}
	}

	// Only one should have successfully acquired the lease!
	if acquiredCount != 1 {
		t.Errorf("expected exactly 1 successful lease acquisition, got %d", acquiredCount)
	}
	if skippedCount != 1 {
		t.Errorf("expected exactly 1 skipped lease acquisition, got %d", skippedCount)
	}

	// The action callback should be executed exactly once
	if actionExecutedCount != 1 {
		t.Errorf("expected action callback to be executed exactly once, got %d", actionExecutedCount)
	}

	// 4. Verify lease and heal counter state in DB
	var dbHealCounter int
	err = p.QueryRow(ctx, "SELECT heal_counter FROM portfolio.sage_lease WHERE bead_id = $1", testBead).Scan(&dbHealCounter)
	if err != nil {
		t.Fatalf("failed to query sage_lease: %v", err)
	}
	if dbHealCounter != 1 {
		t.Errorf("expected database heal_counter to be 1, got %d", dbHealCounter)
	}

	var dbHealCount int
	err = p.QueryRow(ctx, "SELECT heal_count FROM portfolio.sage_heal_count WHERE bead_id = $1", testBead).Scan(&dbHealCount)
	if err != nil {
		t.Fatalf("failed to query sage_heal_count: %v", err)
	}
	if dbHealCount != 1 {
		t.Errorf("expected database heal_count to be 1, got %d", dbHealCount)
	}
}
