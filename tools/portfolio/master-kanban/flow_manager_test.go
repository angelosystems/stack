package main

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFlowManager_Handoff(t *testing.T) {
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

	testInitiativeID := "init-flow-handoff-test"
	testBeadID := "st-yozd" // use the pre-seeded target workspace for st-yozd

	// 1. Clean up old test data
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitiativeID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitiativeID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	// 2. Create test initiative card in NOW stage
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, title, stage, stage_locked_by_human, firma, primary_backend)
		VALUES ($1, 'Flow Handoff Test Card', 'now', false, 'stayawesome', 'plan_file')
	`, testInitiativeID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitiativeID)
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitiativeID)
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	// 3. Link the test bead
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		VALUES ($1, 'bead', $2)
	`, testInitiativeID, testBeadID)
	if err != nil {
		t.Fatalf("failed to insert test link: %v", err)
	}

	// 4. Run the Flow Manager
	vkDB := os.Getenv("VIBE_KANBAN_DB")
	if vkDB == "" {
		vkDB = "/root/.local/share/vibe-kanban/db.v2.sqlite"
	}
	err = runFlowManager(p, vkDB)
	if err != nil {
		t.Fatalf("runFlowManager failed: %v", err)
	}

	// 5. Verify the stagnation flag event was written
	var hasActivityEvent bool
	err = p.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM portfolio.initiative_event
			WHERE initiative_id = $1 AND kind = 'activity'
			  AND payload->>'type' = 'flow_stagnation'
			  AND payload->>'bead_id' = $2
		)
	`, testInitiativeID, testBeadID).Scan(&hasActivityEvent)
	if err != nil {
		t.Fatalf("failed to check activity event: %v", err)
	}
	if !hasActivityEvent {
		t.Errorf("expected activity event with type=flow_stagnation, but it was not found")
	}

	// 6. Verify the vk-Sage handoff sage_action event was written
	var hasSageActionEvent bool
	err = p.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM portfolio.initiative_event
			WHERE initiative_id = $1 AND kind = 'sage_action'
			  AND payload->>'bead_id' = $2
			  AND payload->>'proposed_action' = 're-dispatch'
		)
	`, testInitiativeID, testBeadID).Scan(&hasSageActionEvent)
	if err != nil {
		t.Fatalf("failed to check sage_action event: %v", err)
	}
	if !hasSageActionEvent {
		t.Errorf("expected sage_action event with proposed_action=re-dispatch, but it was not found")
	}
}
