package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AcquireSageLease attempts to atomically acquire a lease/lock for a bead and
// increments its heal counter in a single atomic database operation.
// Returns the updated heal counter and locked_until time if successful.
// If the lease is already held by another client, returns an error.
func AcquireSageLease(ctx context.Context, p *pgxpool.Pool, beadID string, duration time.Duration, lockedBy string) (int, time.Time, error) {
	lockedUntil := time.Now().Add(duration)

	// Ensure the table exists (self-healing for tests/environments)
	_, _ = p.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS portfolio.sage_lease (
			bead_id       text PRIMARY KEY,
			locked_until  timestamptz NOT NULL,
			locked_by     text NOT NULL,
			heal_counter  integer DEFAULT 0 NOT NULL,
			updated_at    timestamptz DEFAULT now() NOT NULL
		)
	`)

	// Perform atomic upsert/update with condition:
	// Only update locked_by and locked_until, and increment heal_counter if:
	// 1) The row does not exist, or
	// 2) The existing lease has expired (locked_until < now())
	// RETURNING will return 1 row only if the INSERT succeeded or the UPDATE's WHERE was true.
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
		// If no row is returned (pgx.ErrNoRows), the lease acquisition failed
		// because the lease is still active (locked_until >= now()).
		return 0, time.Time{}, fmt.Errorf("lease acquisition failed for bead %s (active lease exists)", beadID)
	}

	return healCounter, actualUntil, nil
}

// ReleaseSageLease releases an actively held lease by setting locked_until to now.
func ReleaseSageLease(ctx context.Context, p *pgxpool.Pool, beadID string, lockedBy string) error {
	_, err := p.Exec(ctx, `
		UPDATE portfolio.sage_lease
		SET locked_until = now()
		WHERE bead_id = $1 AND locked_by = $2
	`, beadID, lockedBy)
	return err
}

// GetHealCounter returns the current heal counter for a bead.
func GetHealCounter(ctx context.Context, p *pgxpool.Pool, beadID string) (int, error) {
	var counter int
	err := p.QueryRow(ctx, `
		SELECT heal_counter FROM portfolio.sage_lease WHERE bead_id = $1
	`, beadID).Scan(&counter)
	if err != nil {
		return 0, nil // Return 0 if no record exists
	}
	return counter, nil
}
