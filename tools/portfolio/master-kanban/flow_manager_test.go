package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIsLowerLayerEngaged(t *testing.T) {
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

	testBeadID := "bead-flow-manager-hierarchy-test"

	// Ensure clean state in lease table
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_lease WHERE bead_id = $1", testBeadID)
	defer p.Exec(ctx, "DELETE FROM portfolio.sage_lease WHERE bead_id = $1", testBeadID)

	// Test Case 1: No lower layers engaged
	beads := []LinkedBead{
		{Ref: testBeadID, Status: "closed"},
	}
	workspaces := []LinkedWorkspace{
		{ID: "ws1", Status: "completed"},
	}

	engaged, reason, err := isLowerLayerEngaged(ctx, p, "init-1", beads, workspaces)
	if err != nil {
		t.Fatalf("unexpected error in Case 1: %v", err)
	}
	if engaged {
		t.Errorf("expected not engaged in Case 1, but got engaged with reason: %s", reason)
	}

	// Test Case 2: Active/waiting workspace exists
	workspacesActive := []LinkedWorkspace{
		{ID: "ws1", Status: "running"},
	}
	engaged, reason, err = isLowerLayerEngaged(ctx, p, "init-1", beads, workspacesActive)
	if err != nil {
		t.Fatalf("unexpected error in Case 2: %v", err)
	}
	if !engaged {
		t.Errorf("expected engaged in Case 2 (active workspace), but got not engaged")
	} else if !strings.Contains(reason, "active/waiting workspace exists") {
		t.Errorf("unexpected reason in Case 2: %s", reason)
	}

	// Test Case 3: Bead is not closed
	beadsActive := []LinkedBead{
		{Ref: testBeadID, Status: "open"},
	}
	engaged, reason, err = isLowerLayerEngaged(ctx, p, "init-1", beadsActive, workspaces)
	if err != nil {
		t.Fatalf("unexpected error in Case 3: %v", err)
	}
	if !engaged {
		t.Errorf("expected engaged in Case 3 (bead active), but got not engaged")
	} else if !strings.Contains(reason, "active/pending status") {
		t.Errorf("unexpected reason in Case 3: %s", reason)
	}

	// Test Case 4: Active vk-Sage lease exists
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.sage_lease (bead_id, locked_until, locked_by, heal_counter, updated_at)
		VALUES ($1, $2, 'test-runner', 1, NOW())
	`, testBeadID, time.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatalf("failed to insert test lease for Case 4: %v", err)
	}

	engaged, reason, err = isLowerLayerEngaged(ctx, p, "init-1", beads, workspaces)
	if err != nil {
		t.Fatalf("unexpected error in Case 4: %v", err)
	}
	if !engaged {
		t.Errorf("expected engaged in Case 4 (active lease exists), but got not engaged")
	} else if !strings.Contains(reason, "active vk-Sage lease exists") {
		t.Errorf("unexpected reason in Case 4: %s", reason)
	}
}
