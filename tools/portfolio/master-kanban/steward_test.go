package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSageLeaseSequential(t *testing.T) {
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

	beadID := "st-test-sequential-bead"

	// 1. Clean up potential old test state
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_lease WHERE bead_id = $1", beadID)

	// 2. First acquisition should succeed and set counter to 1
	counter, until, err := AcquireSageLease(ctx, p, beadID, 2*time.Second, "worker-1")
	if err != nil {
		t.Fatalf("first lease acquisition failed: %v", err)
	}
	if counter != 1 {
		t.Errorf("expected heal counter to be 1, got %d", counter)
	}
	if until.Before(time.Now()) {
		t.Errorf("expected locked_until to be in the future, got %v", until)
	}

	// 3. Second acquisition while lease is active should fail
	_, _, err = AcquireSageLease(ctx, p, beadID, 2*time.Second, "worker-2")
	if err == nil {
		t.Error("expected error trying to acquire an active lease, but got none")
	}

	// 4. Release lease manually
	err = ReleaseSageLease(ctx, p, beadID, "worker-1")
	if err != nil {
		t.Fatalf("failed to release lease: %v", err)
	}

	// 5. Acquisition after release should succeed and increment counter to 2
	counter, _, err = AcquireSageLease(ctx, p, beadID, 2*time.Second, "worker-3")
	if err != nil {
		t.Fatalf("acquisition after release failed: %v", err)
	}
	if counter != 2 {
		t.Errorf("expected heal counter to be 2, got %d", counter)
	}

	// Clean up
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_lease WHERE bead_id = $1", beadID)
}

func TestSageLeaseConcurrency(t *testing.T) {
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

	beadID := "st-test-concurrent-bead"

	// 1. Clean up potential old test state
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_lease WHERE bead_id = $1", beadID)

	// 2. Spawn 50 concurrent goroutines to acquire the lease at the same time
	const numWorkers = 50
	var successCount int64
	var failureCount int64

	var wg sync.WaitGroup
	startChan := make(chan struct{})

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			// Wait for the starting gun
			<-startChan

			// Attempt to acquire the lease
			_, _, err := AcquireSageLease(ctx, p, beadID, 10*time.Second, "concurrent-worker")
			if err == nil {
				atomic.AddInt64(&successCount, 1)
			} else {
				atomic.AddInt64(&failureCount, 1)
			}
		}(i)
	}

	// Trigger all goroutines simultaneously
	close(startChan)
	wg.Wait()

	// 3. Exactly one worker must succeed, and all others must fail
	if successCount != 1 {
		t.Errorf("expected exactly 1 successful lease acquisition, got %d", successCount)
	}
	if failureCount != numWorkers-1 {
		t.Errorf("expected exactly %d failed acquisitions, got %d", numWorkers-1, failureCount)
	}

	// 4. Final heal counter should be exactly 1
	counter, err := GetHealCounter(ctx, p, beadID)
	if err != nil {
		t.Fatalf("failed to query heal counter: %v", err)
	}
	if counter != 1 {
		t.Errorf("expected final heal counter to be 1, got %d", counter)
	}

	// Clean up
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_lease WHERE bead_id = $1", beadID)
}

func TestExecuteSageActionConcurrency(t *testing.T) {
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

	beadID := "st-test-execute-concurrent-bead"

	// 1. Clean up potential old test state
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_lease WHERE bead_id = $1", beadID)

	// 2. Spawn 50 concurrent goroutines calling ExecuteSageAction on the same beadID
	const numWorkers = 50
	var successCount int64
	var failureCount int64
	var actionExecutions int64

	var wg sync.WaitGroup
	startChan := make(chan struct{})

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			// Wait for the starting gun
			<-startChan

			// Attempt to execute Sage Action
			err := testExecuteSageAction(ctx, p, beadID, 10*time.Second, "concurrent-worker", func() error {
				atomic.AddInt64(&actionExecutions, 1)
				return nil
			})
			if err == nil {
				atomic.AddInt64(&successCount, 1)
			} else {
				atomic.AddInt64(&failureCount, 1)
			}
		}(i)
	}

	// Trigger all goroutines simultaneously
	close(startChan)
	wg.Wait()

	// 3. Exactly one worker must succeed, and all others must fail
	if successCount != 1 {
		t.Errorf("expected exactly 1 successful testExecuteSageAction execution, got %d", successCount)
	}
	if failureCount != numWorkers-1 {
		t.Errorf("expected exactly %d failed executions, got %d", numWorkers-1, failureCount)
	}

	// 4. The action itself must have been executed exactly once
	if actionExecutions != 1 {
		t.Errorf("expected exactly 1 action execution, got %d", actionExecutions)
	}

	// 5. Final heal counter should be exactly 1
	counter, err := GetHealCounter(ctx, p, beadID)
	if err != nil {
		t.Fatalf("failed to query heal counter: %v", err)
	}
	if counter != 1 {
		t.Errorf("expected final heal counter to be 1, got %d", counter)
	}

	// Clean up
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_lease WHERE bead_id = $1", beadID)
}

func TestStewardReport(t *testing.T) {
	vkDB := os.Getenv("VK_DB")
	if vkDB == "" {
		vkDB = "/root/.local/share/vibe-kanban/db.v2.sqlite"
	}
	if _, err := os.Stat(vkDB); os.IsNotExist(err) {
		t.Skip("vibe-kanban SQLite database not found, skipping steward report test")
		return
	}

	// Set default test DSN if empty
	if dsn == "" {
		dsn = os.Getenv("PORTFOLIO_DSN")
		if dsn == "" {
			dsn = "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable"
		}
	}

	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skip("skipping integration test; db not reachable:", err)
		return
	}
	defer p.Close()

	err = runSteward(p, vkDB)
	if err != nil {
		t.Errorf("expected no error running runSteward, got: %v", err)
	}
}

func AcquireSageLease(ctx context.Context, p *pgxpool.Pool, beadID string, duration time.Duration, lockedBy string) (int, time.Time, error) {
	lockedUntil := time.Now().Add(duration)

	_, _ = p.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS portfolio.sage_lease (
			bead_id       text PRIMARY KEY,
			locked_until  timestamptz NOT NULL,
			locked_by     text NOT NULL,
			heal_counter  integer DEFAULT 0 NOT NULL,
			updated_at    timestamptz DEFAULT now() NOT NULL
		)
	`)

	var healCounter int
	var actualUntil time.Time
	var actualBy string

	query := `
		INSERT INTO portfolio.sage_lease (bead_id, locked_until, locked_by, heal_counter, updated_at)
		VALUES ($1, $2, $3, 1, now())
		ON CONFLICT (bead_id) DO UPDATE
		SET
			heal_counter = portfolio.sage_lease.heal_counter + 1,
			locked_until = EXCLUDED.locked_until,
			locked_by = EXCLUDED.locked_by,
			updated_at = now()
		WHERE portfolio.sage_lease.locked_until < now()
		RETURNING heal_counter, locked_until, locked_by
	`

	err := p.QueryRow(ctx, query, beadID, lockedUntil, lockedBy).Scan(&healCounter, &actualUntil, &actualBy)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("lease acquisition failed for bead %s (active lease exists)", beadID)
	}

	return healCounter, actualUntil, nil
}

func ReleaseSageLease(ctx context.Context, p *pgxpool.Pool, beadID string, lockedBy string) error {
	_, err := p.Exec(ctx, `
		UPDATE portfolio.sage_lease
		SET locked_until = now()
		WHERE bead_id = $1 AND locked_by = $2
	`, beadID, lockedBy)
	return err
}

func GetHealCounter(ctx context.Context, p *pgxpool.Pool, beadID string) (int, error) {
	var counter int
	err := p.QueryRow(ctx, `
		SELECT heal_counter FROM portfolio.sage_lease WHERE bead_id = $1
	`, beadID).Scan(&counter)
	if err != nil {
		return 0, nil
	}
	return counter, nil
}

func testExecuteSageAction(ctx context.Context, p *pgxpool.Pool, beadID string, duration time.Duration, lockedBy string, action func() error) error {
	_, _, err := AcquireSageLease(ctx, p, beadID, duration, lockedBy)
	if err != nil {
		return fmt.Errorf("failed to acquire lease for action: %w", err)
	}

	if err := action(); err != nil {
		_ = ReleaseSageLease(ctx, p, beadID, lockedBy)
		return fmt.Errorf("sage action failed: %w", err)
	}

	return nil
}
