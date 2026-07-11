//go:build integration

package main

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPromoteReif_CompletenessCheckAndDamping(t *testing.T) {
	os.Setenv("PORTFOLIO_CONFIDENCE_THRESHOLD", "0.0")
	defer os.Unsetenv("PORTFOLIO_CONFIDENCE_THRESHOLD")

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

	// Apply self-healing schema migration for promote_damped
	_, _ = p.Exec(ctx, "ALTER TABLE portfolio.initiative_event DROP CONSTRAINT IF EXISTS initiative_event_kind_check")
	_, _ = p.Exec(ctx, `ALTER TABLE portfolio.initiative_event ADD CONSTRAINT initiative_event_kind_check
		CHECK (kind = ANY (ARRAY[
			'created', 'moved', 'edited', 'linked', 'unlinked', 'activity',
			'stage_proposed', 'completed', 'commented', 'archived', 'dispatched',
			'deployed', 'workspace_started', 'ai_message', 'ai_action', 'sage_action',
			'promote_damped'
		]))`)

	testInitID := "init-promote-test"
	testBeadID := "bead-promote-test-1"

	// 1. Clean up old test data
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)

	sp, err := solartownPool()
	if err == nil {
		_, _ = sp.Exec(ctx, "DELETE FROM beads.issues WHERE id = $1", testBeadID)
	}

	// 2. Create test initiative in 'now' stage
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, title, stage, stage_locked_by_human, firma, primary_backend)
		VALUES ($1, 'Promote Reif Test Initiative', 'now', false, 'stayawesome', 'plan_file')
	`, testInitID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID)
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitID)
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)

	// 3. Link test bead to the initiative
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		VALUES ($1, 'bead', $2)
	`, testInitID, testBeadID)
	if err != nil {
		t.Fatalf("failed to insert test links: %v", err)
	}

	// 4. Create the bead in Dolt and mark it closed
	if sp != nil {
		_, err = sp.Exec(ctx, "INSERT INTO beads.issues (id, rig, title, status) VALUES ($1, 'stayawesomeOS', 'Test Issue 1', 'closed')", testBeadID)
		if err != nil {
			t.Fatalf("failed to insert issue: %v", err)
		}
		defer sp.Exec(ctx, "DELETE FROM beads.issues WHERE id = $1", testBeadID)
	}

	// 5. Test getCaptureCompleteness
	comp, err := getCaptureCompleteness(ctx, p)
	if err != nil {
		t.Fatalf("getCaptureCompleteness failed: %v", err)
	}
	t.Logf("Current capture completeness is: %.2f%%", comp)

	// Save original function and restore at the end
	origFunc := getCaptureCompletenessFunc
	defer func() { getCaptureCompletenessFunc = origFunc }()

	// --- Test Scenario 1: Low Completeness (Damped Promotion) ---
	getCaptureCompletenessFunc = func(ctx context.Context, p *pgxpool.Pool) (float64, error) {
		return 25.0, nil
	}

	items := []map[string]any{
		{
			"id":    testInitID,
			"stage": "now",
		},
	}
	err = enrichInitiativesWithPromoteReady(ctx, p, items)
	if err != nil {
		t.Fatalf("enrichInitiativesWithPromoteReady failed: %v", err)
	}

	promoteReady, ok := items[0]["promote_ready"].(bool)
	if !ok {
		t.Fatalf("promote_ready field is not boolean or missing")
	}
	promoteConfidence, ok := items[0]["promote_ready_confidence"].(string)
	if !ok {
		t.Fatalf("promote_ready_confidence is missing or not a string")
	}
	promoteCaveat, _ := items[0]["promote_ready_caveat"].(string)

	t.Logf("Scenario 1 Enriched values: promote_ready=%t, confidence=%s, caveat=%s",
		promoteReady, promoteConfidence, promoteCaveat)

	if !promoteReady {
		t.Errorf("expected promote_ready to be true since all linked beads are closed")
	}
	if promoteConfidence != "low" {
		t.Errorf("expected confidence to be 'low' under 25.0%% completeness, got %q", promoteConfidence)
	}
	if promoteCaveat == "" {
		t.Errorf("expected a caveat message for low completeness promote-ready")
	}

	// Trigger checkAndMoveToWatching
	checkAndMoveToWatching(ctx, p, testInitID)

	// Stage should remain 'now'
	var currentStage string
	err = p.QueryRow(ctx, "SELECT stage FROM portfolio.initiative WHERE id = $1", testInitID).Scan(&currentStage)
	if err != nil {
		t.Fatalf("failed to query stage: %v", err)
	}
	if currentStage != "now" {
		t.Errorf("expected stage to remain 'now' due to low completeness damping, got %q", currentStage)
	}

	// Verify that a 'promote_damped' event was inserted
	var eventCount int
	err = p.QueryRow(ctx, "SELECT COUNT(*) FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'promote_damped'", testInitID).Scan(&eventCount)
	if err != nil {
		t.Fatalf("failed to query event count: %v", err)
	}
	if eventCount == 0 {
		t.Errorf("expected 'promote_damped' event to be logged")
	}

	// --- Test Scenario 2: High Completeness (Allowed Promotion) ---
	getCaptureCompletenessFunc = func(ctx context.Context, p *pgxpool.Pool) (float64, error) {
		return 85.0, nil
	}

	items2 := []map[string]any{
		{
			"id":    testInitID,
			"stage": "now",
		},
	}
	err = enrichInitiativesWithPromoteReady(ctx, p, items2)
	if err != nil {
		t.Fatalf("enrichInitiativesWithPromoteReady failed: %v", err)
	}

	promoteConfidence2, _ := items2[0]["promote_ready_confidence"].(string)
	promoteCaveat2, _ := items2[0]["promote_ready_caveat"].(string)

	t.Logf("Scenario 2 Enriched values: promote_ready=%t, confidence=%s, caveat=%s",
		items2[0]["promote_ready"], promoteConfidence2, promoteCaveat2)

	if promoteConfidence2 != "high" {
		t.Errorf("expected confidence to be 'high' under 85.0%% completeness, got %q", promoteConfidence2)
	}
	if promoteCaveat2 != "" {
		t.Errorf("expected no caveat message for high completeness promote-ready, got %q", promoteCaveat2)
	}

	// Trigger checkAndMoveToWatching again
	checkAndMoveToWatching(ctx, p, testInitID)

	// Verify a sage_action event with classification 'all-beads-closed' is logged
	var exists bool
	err = p.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM portfolio.initiative_event
			WHERE initiative_id = $1 AND kind = 'sage_action' AND (payload->>'classification') = 'all-beads-closed'
		)
	`, testInitID).Scan(&exists)
	if err != nil {
		t.Fatalf("failed to query initiative events: %v", err)
	}
	if !exists {
		t.Errorf("expected 'sage_action' event with 'all-beads-closed' classification to be logged, but it was not found")
	}
}
