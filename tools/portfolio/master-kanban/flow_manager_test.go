package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d        time.Duration
		expected string
	}{
		{5 * time.Hour, "5h"},
		{25 * time.Hour, "1d"},
		{48 * time.Hour, "2d"},
		{72 * time.Hour, "3d"},
		{10 * time.Minute, "0h"},
	}

	for _, tc := range tests {
		res := formatDuration(tc.d)
		if res != tc.expected {
			t.Errorf("formatDuration(%v) = %q; expected %q", tc.d, res, tc.expected)
		}
	}
}

func TestFlowManagerDetectionsIntegration(t *testing.T) {
	dsn := os.Getenv("PORTFOLIO_DSN")
	if dsn == "" {
		dsn = "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable"
	}

	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skip("skipping integration test; db not reachable:", err)
		return
	}
	defer p.Close()

	if err := p.Ping(ctx); err != nil {
		t.Skip("skipping integration test; db ping failed:", err)
		return
	}

	sp, err := solartownPool()
	if err != nil {
		t.Skip("skipping integration test; solartown db not reachable:", err)
		return
	}

	// Setup dummy data
	initIDStagnant := "test-init-stagnant"
	initIDPromote := "test-init-promote"
	initIDRot := "test-init-rot"

	beadIDClosed := "test-bead-closed"
	beadIDOpen := "test-bead-open"

	// 1. Clean up potential old test state
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id IN ($1, $2, $3)", initIDStagnant, initIDPromote, initIDRot)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id IN ($1, $2, $3)", initIDStagnant, initIDPromote, initIDRot)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id IN ($1, $2, $3)", initIDStagnant, initIDPromote, initIDRot)
	_, _ = sp.Exec(ctx, "DELETE FROM beads.issues WHERE id IN ($1, $2)", beadIDClosed, beadIDOpen)

	// 2. Insert dummy initiatives
	// - stagnant: stage 'now', created 5 days ago (so silence > 3 days)
	fiveDaysAgo := time.Now().Add(-5 * 24 * time.Hour)
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, created_at, updated_at)
		VALUES ($1, 'stayawesome', 'now', 'Test Stagnant Card', $2, $2)
	`, initIDStagnant, fiveDaysAgo)
	if err != nil {
		t.Fatalf("failed to insert stagnant initiative: %v", err)
	}

	// - promote: stage 'now' with all linked beads closed
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, created_at, updated_at)
		VALUES ($1, 'stayawesome', 'now', 'Test Promote Card', NOW(), NOW())
	`, initIDPromote)
	if err != nil {
		t.Fatalf("failed to insert promote initiative: %v", err)
	}

	// - rot: stage 'idea', created 35 days ago (silence > 30 days)
	thirtyFiveDaysAgo := time.Now().Add(-35 * 24 * time.Hour)
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, created_at, updated_at)
		VALUES ($1, 'stayawesome', 'idea', 'Test Rot Card', $2, $2)
	`, initIDRot, thirtyFiveDaysAgo)
	if err != nil {
		t.Fatalf("failed to insert rot initiative: %v", err)
	}

	// 3. Insert beads and links
	_, err = sp.Exec(ctx, `
		INSERT INTO beads.issues (id, rig, title, status)
		VALUES ($1, 'stayawesomeOS', 'Closed Bead', 'closed'),
		       ($2, 'stayawesomeOS', 'Open Bead', 'open')
	`, beadIDClosed, beadIDOpen)
	if err != nil {
		t.Fatalf("failed to insert beads: %v", err)
	}

	// Link closed bead to promote card
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		VALUES ($1, 'bead', $2)
	`, initIDPromote, beadIDClosed)
	if err != nil {
		t.Fatalf("failed to insert link for promote: %v", err)
	}

	defer func() {
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id IN ($1, $2, $3)", initIDStagnant, initIDPromote, initIDRot)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id IN ($1, $2, $3)", initIDStagnant, initIDPromote, initIDRot)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id IN ($1, $2, $3)", initIDStagnant, initIDPromote, initIDRot)
		_, _ = sp.Exec(ctx, "DELETE FROM beads.issues WHERE id IN ($1, $2)", beadIDClosed, beadIDOpen)
	}()

	// 4. Verify helper function getLinkedBeadsActive
	hasActive, all, err := getLinkedBeadsActive(ctx, p, initIDPromote)
	if err != nil {
		t.Fatalf("getLinkedBeadsActive failed: %v", err)
	}
	if hasActive {
		t.Errorf("expected promote card to have NO active beads, got active")
	}
	if len(all) != 1 || all[0] != beadIDClosed {
		t.Errorf("expected linked beads of promote card to be [%s], got %v", beadIDClosed, all)
	}

	// 5. Test running flow manager without writing events
	err = runFlowManager(p, "non-existent-sqlite.db", false)
	if err != nil {
		t.Fatalf("runFlowManager failed: %v", err)
	}

	// 6. Test running flow manager and writing events
	err = runFlowManager(p, "non-existent-sqlite.db", true)
	if err != nil {
		t.Fatalf("runFlowManager with writeEvents failed: %v", err)
	}

	// 7. Verify events were logged
	var existsStagnant, existsPromote, existsRot bool
	_ = p.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM portfolio.initiative_event 
			WHERE initiative_id = $1 AND kind = 'activity' AND payload->>'detection_type' = 'stagnation'
		)
	`, initIDStagnant).Scan(&existsStagnant)

	_ = p.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM portfolio.initiative_event 
			WHERE initiative_id = $1 AND kind = 'activity' AND payload->>'detection_type' = 'promote_ready'
		)
	`, initIDPromote).Scan(&existsPromote)

	_ = p.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM portfolio.initiative_event 
			WHERE initiative_id = $1 AND kind = 'activity' AND payload->>'detection_type' = 'backlog_rot'
		)
	`, initIDRot).Scan(&existsRot)

	if !existsStagnant {
		t.Error("expected stagnation event to be written for stagnant initiative")
	}
	if !existsPromote {
		t.Error("expected promote_ready event to be written for promote initiative")
	}
	if !existsRot {
		t.Error("expected backlog_rot event to be written for rot initiative")
	}
}
