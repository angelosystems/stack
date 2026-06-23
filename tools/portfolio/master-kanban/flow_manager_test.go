package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestDiagnoseFlaggedCard(t *testing.T) {
	type TextContent struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type MockResponse struct {
		Content []TextContent `json:"content"`
	}

	var mockText string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := MockResponse{
			Content: []TextContent{
				{
					Type: "text",
					Text: mockText,
				},
			},
		}
		b, _ := json.Marshal(resp)
		w.Write(b)
	}))
	defer server.Close()

	os.Setenv("ZAI_KEY", "test-key-123")
	os.Setenv("REVIEWER_BASE_URL", server.URL)
	defer func() {
		os.Unsetenv("ZAI_KEY")
		os.Unsetenv("REVIEWER_BASE_URL")
	}()

	init := FlowInitiative{
		ID:          "init-1",
		Firma:       "stayawesome",
		Stage:       "now",
		Title:       "Test Initiative",
		Description: "A stagnant test initiative",
		CreatedAt:   time.Now().Add(-10 * 24 * time.Hour),
		UpdatedAt:   time.Now().Add(-5 * 24 * time.Hour),
	}
	beads := []LinkedBead{{Ref: "bead-1", Status: "open"}}
	workspaces := []LinkedWorkspace{{ID: "ws-1", Status: "completed"}}
	events := []FlowEvent{}
	flaggedReasons := []string{"Stagnation: 5 tage stille"}

	// Case 1: High confidence
	mockText = `{"category": "wartet-auf-Mensch", "confidence": "High", "reasoning": "Needs manual triage as it has been stale for 5 days.", "proposed_action": "ask owner for input"}`
	diag, err := diagnoseFlaggedCard(init, beads, workspaces, events, flaggedReasons)
	if err != nil {
		t.Fatalf("unexpected error in Case 1: %v", err)
	}
	if diag.Category != "wartet-auf-Mensch" {
		t.Errorf("expected Category 'wartet-auf-Mensch', got '%s'", diag.Category)
	}
	if diag.Confidence != "High" {
		t.Errorf("expected Confidence 'High', got '%s'", diag.Confidence)
	}
	if diag.ProposedAction != "ask owner for input" {
		t.Errorf("expected ProposedAction 'ask owner for input', got '%s'", diag.ProposedAction)
	}

	// Case 2: Low confidence (suppress proposed action)
	mockText = `{"category": "Workspace-gescheitert", "confidence": "Low", "reasoning": "Unclear status.", "proposed_action": "do something"}`
	diag, err = diagnoseFlaggedCard(init, beads, workspaces, events, flaggedReasons)
	if err != nil {
		t.Fatalf("unexpected error in Case 2: %v", err)
	}
	if diag.Category != "Workspace-gescheitert" {
		t.Errorf("expected Category 'Workspace-gescheitert', got '%s'", diag.Category)
	}
	if diag.Confidence != "Low" {
		t.Errorf("expected Confidence 'Low', got '%s'", diag.Confidence)
	}
	if diag.ProposedAction != "" {
		t.Errorf("expected ProposedAction to be empty for Low confidence, got '%s'", diag.ProposedAction)
	}
}
