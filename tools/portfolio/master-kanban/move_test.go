//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCmdMove(t *testing.T) {
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

	testInitID := "st-test-move-specific"

	// Ensure any old test entry is removed
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)

	// Insert test initiative with stage 'idea'
	_, err = p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend)
		VALUES ($1, 'solartown', 'idea', 'Test Move Initiative', 'plan_file')`, testInitID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}
	defer func() {
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)
	}()

	// Execute cmdMove to move st-test-move-specific to 'now'
	cmd := cmdMove()
	oldPool := pool
	pool = p // override the global db pool
	defer func() {
		pool = oldPool
	}()

	cmd.SetArgs([]string{testInitID, "now"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmdMove execution failed: %v", err)
	}

	// Verify the database has the updated stage
	var stage string
	var locked bool
	err = p.QueryRow(ctx, `SELECT stage, stage_locked_by_human FROM portfolio.initiative WHERE id = $1`, testInitID).Scan(&stage, &locked)
	if err != nil {
		t.Fatalf("failed to query database for update validation: %v", err)
	}

	if stage != "now" {
		t.Errorf("expected stage to be updated to 'now', got %q", stage)
	}
	if !locked {
		t.Errorf("expected stage_locked_by_human to be true, got false")
	}
}
