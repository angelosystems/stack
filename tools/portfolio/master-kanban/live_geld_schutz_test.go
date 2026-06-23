package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLiveGeldSchutzManagerSweep(t *testing.T) {
	portfolioDsn := envOr("PORTFOLIO_DSN", "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable")
	ctx := context.Background()

	pPool, err := pgxpool.New(ctx, portfolioDsn)
	if err != nil {
		t.Fatalf("Failed to connect to portfolio DB: %v", err)
	}
	defer pPool.Close()

	testInitID := "qb-test-live-geld-protection"

	// Cleanup
	cleanup := func() {
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitID)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)
	}
	cleanup()
	defer cleanup()

	// 1. Create a dummy initiative card for quantbot in PG (marked as now, stale enough to stagnate)
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, description, created_at, updated_at)
		VALUES ($1, 'quantbot', 'now', 'Test Live-Geld Stagnant Card', 'Desc', now() - interval '48 hours', now() - interval '48 hours')
	`, testInitID)
	if err != nil {
		t.Fatalf("Failed to create test initiative: %v", err)
	}

	// 2. We set environment variables to trigger stagnation check
	t.Setenv("MANAGER_STAGNATION_THRESHOLD_NOW", "1h")

	// 3. Run the manager sweep
	err = runManagerSweep(pPool)
	if err != nil {
		t.Fatalf("runManagerSweep failed: %v", err)
	}

	// 4. Retrieve digest payload and verify actions on stagnation flag
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
				t.Errorf("Expected exactly 1 action (escalate) for quantbot stagnant card, got %d", len(flag.Actions))
			} else {
				action := flag.Actions[0]
				if action.Label != "Eskalieren" || action.Endpoint != "/api/escalate" {
					t.Errorf("Expected exclusively Eskalieren action at /api/escalate, got %s at %s", action.Label, action.Endpoint)
				}
			}
		}
	}
	if !foundStagnant {
		t.Errorf("Expected quantbot stagnant initiative to be flagged as stagnant")
	}
}

func TestLiveGeldSchutzAllBeadsClosed(t *testing.T) {
	portfolioDsn := envOr("PORTFOLIO_DSN", "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable")
	ctx := context.Background()

	pPool, err := pgxpool.New(ctx, portfolioDsn)
	if err != nil {
		t.Fatalf("Failed to connect to portfolio DB: %v", err)
	}
	defer pPool.Close()

	testInitID := "qb-test-live-geld-all-closed"

	// Cleanup
	cleanup := func() {
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitID)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)
	}
	cleanup()
	defer cleanup()

	// 1. Create a dummy initiative card for quantbot
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, description, created_at, updated_at)
		VALUES ($1, 'quantbot', 'now', 'Test Live-Geld All Beads Closed Card', 'Desc', now(), now())
	`, testInitID)
	if err != nil {
		t.Fatalf("Failed to create test initiative: %v", err)
	}

	// 2. Link a bead to it (bead exists or not, checkAndMoveToWatching counts it if link exists)
	_, err = pPool.Exec(ctx, `
		INSERT INTO portfolio.initiative_link (initiative_id, ref, kind)
		VALUES ($1, 'qu-test-bead-closed', 'bead')
	`, testInitID)
	if err != nil {
		t.Fatalf("Failed to link bead: %v", err)
	}

	// 3. We run checkAndMoveToWatching. Because solartownPool is reachable,
	// but qu-test-bead-closed is not in solartown, wait, let's make sure it counts as closed.
	// Oh! checkAndMoveToWatching queries Beads Dolt DB `status <> 'closed'`.
	// Since `qu-test-bead-closed` won't be in Dolt DB beads.issues, it will return count = 0 open beads (actually it queries count where status <> 'closed' and since there are no rows, the count is indeed 0!).
	// So it will see it as all beads closed!
	checkAndMoveToWatching(ctx, pPool, testInitID)

	// 4. Verify that sage_action event was logged, and it has proposed_action='escalate'
	var payloadStr string
	err = pPool.QueryRow(ctx, `
		SELECT payload::text FROM portfolio.initiative_event
		WHERE initiative_id = $1 AND kind = 'sage_action' AND payload->>'classification' = 'all-beads-closed'
		ORDER BY at DESC LIMIT 1
	`, testInitID).Scan(&payloadStr)
	if err != nil {
		t.Fatalf("Failed to fetch all-beads-closed sage_action: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
		t.Fatalf("Failed to parse event payload: %v", err)
	}

	if payload["proposed_action"] != "escalate" {
		t.Errorf("Expected proposed_action 'escalate' for quantbot card under Live-Geld-Schutz, got %v", payload["proposed_action"])
	}
	if payload["to_stage"] != "" {
		t.Errorf("Expected to_stage to be empty, got %v", payload["to_stage"])
	}
}
