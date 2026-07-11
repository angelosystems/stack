//go:build integration

package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGetPromoteTargetStage(t *testing.T) {
	ctx := context.Background()

	// 1. Test static non-idea transitions
	cases := []struct {
		current  string
		expected string
	}{
		{"soon", "now"},
		{"now", "watching"},
		{"watching", "done"},
		{"done", "done"}, // default fallback or done case
		{"invalid", "done"},
	}

	for _, tc := range cases {
		actual := GetPromoteTargetStage(ctx, nil, nil, fmt.Errorf("no db"), tc.current, "solartown", 0)
		if actual != tc.expected {
			t.Errorf("GetPromoteTargetStage(nil, nil, ..., %q, ...) = %q; expected %q", tc.current, actual, tc.expected)
		}
	}

	// 2. Test idea transition when there is no database / capacity (fallback to soon)
	actualSoon := GetPromoteTargetStage(ctx, nil, nil, fmt.Errorf("no db"), "idea", "solartown", 0)
	if actualSoon != "soon" {
		t.Errorf("expected idea with no db capacity to promote to 'soon', got %q", actualSoon)
	}

	// 3. Test idea transition with database pool (and mock stPool)
	dsn := mkIntegrationDSN(t)
	pPool, err := pgxpool.New(ctx, dsn)
	if err == nil {
		defer pPool.Close()

		oldStPool := stPool
		stPool = pPool
		defer func() {
			stPool = oldStPool
		}()

		testAgentID := "testrig-polecat-testagent"
		_, _ = pPool.Exec(ctx, "DELETE FROM beads.labels WHERE issue_id = $1", testAgentID)
		_, _ = pPool.Exec(ctx, "DELETE FROM beads.issues WHERE id = $1", testAgentID)

		// A. Test when idle polecats > 0 (hasCapacity = true)
		_, err = pPool.Exec(ctx, `
			INSERT INTO beads.issues (id, rig, status, assignee, title, created_at, updated_at)
			VALUES ($1, 'testrig', 'open', 'unassigned', 'Agent issue', now(), now())
		`, testAgentID)
		if err == nil {
			_, _ = pPool.Exec(ctx, `
				INSERT INTO beads.labels (issue_id, rig, label, created_at)
				VALUES ($1, 'testrig', 'gt:agent', now())
			`, testAgentID)

			// GetPromoteTargetStage with nowCount = 0 (under limit 3) -> should be "now"
			target := GetPromoteTargetStage(ctx, pPool, pPool, nil, "idea", "solartown", 0)
			if target != "now" {
				t.Errorf("expected target stage 'now' when there is capacity, got %q", target)
			}

			// GetPromoteTargetStage with nowCount = 4 (above limit 3) -> should be "soon"
			targetAbove := GetPromoteTargetStage(ctx, pPool, pPool, nil, "idea", "solartown", 4)
			if targetAbove != "soon" {
				t.Errorf("expected target stage 'soon' when nowCount is above WIP limit, got %q", targetAbove)
			}
		}

		// Clean up
		_, _ = pPool.Exec(ctx, "DELETE FROM beads.labels WHERE issue_id = $1", testAgentID)
		_, _ = pPool.Exec(ctx, "DELETE FROM beads.issues WHERE id = $1", testAgentID)
	}
}
