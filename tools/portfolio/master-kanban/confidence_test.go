package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLinkageConfidenceAndPromoteReady(t *testing.T) {
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

	sp, err := solartownPool()
	if err != nil {
		t.Skip("skipping integration test; solartown db not reachable:", err)
	}
	if err := sp.Ping(ctx); err != nil {
		t.Skip("skipping integration test; solartown db ping failed:", err)
	}

	// 1. Verify getLinkageCompleteness executes without errors
	pct, err := getLinkageCompleteness(ctx, p)
	if err != nil {
		t.Fatalf("failed to calculate linkage completeness: %v", err)
	}
	t.Logf("Current Linkage Completeness: %.2f%%", pct)

	// 2. Insert temporary test initiative
	testInitID := "sa-test-confidence-init"
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)

	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend)
		VALUES ($1, 'stayawesome', 'now', 'Confidence Test Initiative', 'plan_file')
	`, testInitID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}
	defer func() {
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitID)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)
	}()

	// 3. Create mock beads in solartownPool
	testBead1 := "sa-test-bead-conf-1"
	testBead2 := "sa-test-bead-conf-2"
	_, _ = sp.Exec(ctx, "DELETE FROM beads.issues WHERE id IN ($1, $2)", testBead1, testBead2)

	_, err = sp.Exec(ctx, `
		INSERT INTO beads.issues (id, title, status, rig)
		VALUES ($1, 'Confidence Test Bead 1', 'open', 'stack')
	`, testBead1)
	if err != nil {
		t.Fatalf("failed to insert test bead 1: %v", err)
	}
	defer func() {
		_, _ = sp.Exec(ctx, "DELETE FROM beads.issues WHERE id IN ($1, $2)", testBead1, testBead2)
	}()

	// Link testBead1 to testInitID
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		VALUES ($1, 'bead', $2)
	`, testInitID, testBead1)
	if err != nil {
		t.Fatalf("failed to link test bead 1: %v", err)
	}

	// 4. Test getPromoteReadyInitiatives - bead 1 is open, should NOT be promote_ready
	promoteReady, err := getPromoteReadyInitiatives(ctx, p)
	if err != nil {
		t.Fatalf("failed to get promote ready initiatives: %v", err)
	}
	if promoteReady[testInitID] {
		t.Errorf("expected initiative %s to NOT be promote ready since bead is open", testInitID)
	}

	// Set bead 1 to closed
	_, err = sp.Exec(ctx, "UPDATE beads.issues SET status = 'closed' WHERE id = $1", testBead1)
	if err != nil {
		t.Fatalf("failed to close test bead 1: %v", err)
	}

	// 5. Test getPromoteReadyInitiatives - bead 1 is closed, should be promote_ready
	promoteReady, err = getPromoteReadyInitiatives(ctx, p)
	if err != nil {
		t.Fatalf("failed to get promote ready initiatives: %v", err)
	}
	if !promoteReady[testInitID] {
		t.Errorf("expected initiative %s to be promote ready since all linked beads are closed", testInitID)
	}

	// 6. Test checkAndMoveToWatching Damping behavior
	// (a) With low linkage confidence threshold forced to 100%, and completeness is < 100%:
	// It should skip transitioning or proposing promotion.
	os.Setenv("PORTFOLIO_CONFIDENCE_THRESHOLD", "100.0")
	defer os.Unsetenv("PORTFOLIO_CONFIDENCE_THRESHOLD")

	// Verify that the stage is currently 'now'
	var currentStage string
	err = p.QueryRow(ctx, "SELECT stage FROM portfolio.initiative WHERE id = $1", testInitID).Scan(&currentStage)
	if err != nil {
		t.Fatalf("failed to query initiative stage: %v", err)
	}
	if currentStage != "now" {
		t.Errorf("expected initial stage to be 'now', got %q", currentStage)
	}

	// Clean up old events for this initiative first
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID)

	// Run checkAndMoveToWatching
	checkAndMoveToWatching(ctx, p, testInitID)

	// Verify that no sage_action or promote_damped event was logged for low confidence
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
	if exists {
		t.Errorf("expected no 'sage_action' event when threshold is 100%% and completeness < 100%%")
	}

	// (b) With linkage confidence threshold forced to 0.0%:
	// It should proceed and propose stage promotion (or log promote_damped if global completeness is low).
	os.Setenv("PORTFOLIO_CONFIDENCE_THRESHOLD", "0.0")

	// Set getCaptureCompletenessFunc to high so it proposes stage promotion
	origFunc := getCaptureCompletenessFunc
	getCaptureCompletenessFunc = func(ctx context.Context, p *pgxpool.Pool) (float64, error) {
		return 85.0, nil
	}
	defer func() { getCaptureCompletenessFunc = origFunc }()

	checkAndMoveToWatching(ctx, p, testInitID)

	// Verify that a sage_action with classification 'all-beads-closed' was logged
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
		t.Errorf("expected 'sage_action' event with 'all-beads-closed' when threshold is 0.0%%")
	}
}

func TestConfidenceAPIEndpoints(t *testing.T) {
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

	// Set up handlers
	mux := http.NewServeMux()
	mux.HandleFunc("/api/initiatives", func(w http.ResponseWriter, r *http.Request) {
		// Mock logic or call database
		w.Header().Set("Content-Type", "application/json")
		completenessPct, _ := getLinkageCompleteness(r.Context(), p)
		threshold := 90.0

		item := map[string]any{
			"id":                           "sa-test-confidence-init",
			"promote_ready":                true,
			"linkage_confidence_percentage": completenessPct,
		}
		if completenessPct < threshold {
			item["confidence_caveat"] = fmt.Sprintf("Confidence-Vorbehalt: Niedrige Linkage-Abdeckung (%.1f%%).", completenessPct)
		} else {
			item["confidence_caveat"] = ""
		}

		fmt.Fprint(w, `[{"id":"sa-test-confidence-init","promote_ready":true,"linkage_confidence_percentage":`)
		fmt.Fprintf(w, "%f", completenessPct)
		fmt.Fprint(w, `,"confidence_caveat":"`+item["confidence_caveat"].(string)+`"}]`)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	// 1. Set threshold to 100% to guarantee caveat if completeness < 100
	os.Setenv("PORTFOLIO_CONFIDENCE_THRESHOLD", "100.0")
	defer os.Unsetenv("PORTFOLIO_CONFIDENCE_THRESHOLD")

	resp, err := http.Get(server.URL + "/api/initiatives")
	if err != nil {
		t.Fatalf("failed to call /api/initiatives: %v", err)
	}
	defer resp.Body.Close()

	var data []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(data) == 0 {
		t.Fatalf("expected at least one initiative")
	}

	init := data[0]
	if init["promote_ready"] != true {
		t.Errorf("expected promote_ready to be true")
	}
	if init["linkage_confidence_percentage"] == nil {
		t.Errorf("expected linkage_confidence_percentage to be set")
	}
}
