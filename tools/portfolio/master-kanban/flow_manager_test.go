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

func TestHasOpenReactorAttempt(t *testing.T) {
	// 1. Empty events
	if hasOpenReactorAttempt(nil) {
		t.Error("expected false for nil/empty events")
	}

	// 2. Terminal statuses for deployed events
	testsTerminal := []struct {
		status   string
		expected bool
	}{
		{"healthy", false},
		{"failed", false},
		{"rolled-back", false},
		{"blocked_migrations", false},
		{"", false},
		{"running", true},
		{"deploying", true},
		{"unknown_non_terminal", true},
	}

	for _, tc := range testsTerminal {
		payload := `{"status":"` + tc.status + `"}`
		events := []FlowEvent{
			{
				Kind:    "deployed",
				Payload: payload,
				At:      time.Now(),
			},
		}
		res := hasOpenReactorAttempt(events)
		if res != tc.expected {
			web_status := tc.status
			t.Errorf("status %q: expected %v, got %v", web_status, tc.expected, res)
		}
	}

	// 3. Dispatched recently with no newer activity -> open reactor attempt
	eventsRecentDispatch := []FlowEvent{
		{
			Kind: "dispatched",
			At:   time.Now().Add(-5 * time.Minute),
		},
	}
	if !hasOpenReactorAttempt(eventsRecentDispatch) {
		t.Error("expected true for recent dispatch with no newer events")
	}

	// 4. Dispatched recently but with newer terminal deployment -> no open reactor attempt
	eventsDispatchWithNewerDeploy := []FlowEvent{
		{
			Kind: "dispatched",
			At:   time.Now().Add(-10 * time.Minute),
		},
		{
			Kind:    "deployed",
			Payload: `{"status":"healthy"}`,
			At:      time.Now().Add(-5 * time.Minute),
		},
	}
	if hasOpenReactorAttempt(eventsDispatchWithNewerDeploy) {
		t.Error("expected false for recent dispatch followed by healthy deploy")
	}

	// 5. Dispatched recently but with newer workspace started -> no open reactor attempt
	eventsDispatchWithNewerWorkspace := []FlowEvent{
		{
			Kind: "dispatched",
			At:   time.Now().Add(-10 * time.Minute),
		},
		{
			Kind: "workspace_started",
			At:   time.Now().Add(-5 * time.Minute),
		},
	}
	if hasOpenReactorAttempt(eventsDispatchWithNewerWorkspace) {
		t.Error("expected false for recent dispatch followed by workspace_started")
	}

	// 6. Dispatched more than 15 minutes ago with no newer activity -> no open reactor attempt (not "recently")
	eventsOldDispatch := []FlowEvent{
		{
			Kind: "dispatched",
			At:   time.Now().Add(-20 * time.Minute),
		},
	}
	if hasOpenReactorAttempt(eventsOldDispatch) {
		t.Error("expected false for dispatched older than 15 minutes with no newer events")
	}
}

func TestDiagnoseFlaggedCard(t *testing.T) {
	// 1. Setup mock GLM server
	var mockGLMResponse string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Anthropic-compatible JSON envelope
		resp := map[string]any{
			"content": []map[string]string{
				{
					"type": "text",
					"text": mockGLMResponse,
				},
			},
		}
		b, _ := json.Marshal(resp)
		w.Write(b)
	}))
	defer server.Close()

	// Override environment variables for callGlm
	os.Setenv("ZAI_KEY", "test-key-abc")
	os.Setenv("REVIEWER_BASE_URL", server.URL)
	defer func() {
		os.Unsetenv("ZAI_KEY")
		os.Unsetenv("REVIEWER_BASE_URL")
	}()

	init := FlowInitiative{
		ID:          "st-test-card",
		Firma:       "stayawesome",
		Stage:       "now",
		Title:       "Test Card",
		Description: "Just a test card",
		CreatedAt:   time.Now().Add(-10 * 24 * time.Hour),
		UpdatedAt:   time.Now().Add(-5 * 24 * time.Hour),
	}

	beads := []LinkedBead{
		{Ref: "st-123", Status: "closed"},
	}
	workspaces := []LinkedWorkspace{
		{ID: "WS1", Status: "failed"},
	}
	events := []FlowEvent{
		{Kind: "dispatched", Actor: "reactor", At: time.Now().Add(-4 * 24 * time.Hour)},
	}
	flaggedReasons := []string{"Stagnation: 5 tage stille, keine aktive arbeit"}

	// Test Case A: High Confidence
	mockGLMResponse = `{
		"category": "Workspace-gescheitert",
		"confidence": "High",
		"reasoning": "The verlinked workspace WS1 has failed, and the card has been stale for 5 days.",
		"proposed_action": "re-dispatch bead st-123"
	}`

	diagnosis, err := diagnoseFlaggedCard(init, beads, workspaces, events, flaggedReasons)
	if err != nil {
		t.Fatalf("diagnoseFlaggedCard failed: %v", err)
	}

	if diagnosis.Category != "Workspace-gescheitert" {
		t.Errorf("expected Category Workspace-gescheitert, got %q", diagnosis.Category)
	}
	if diagnosis.Confidence != "High" {
		t.Errorf("expected Confidence High, got %q", diagnosis.Confidence)
	}
	if diagnosis.ProposedAction != "re-dispatch bead st-123" {
		t.Errorf("expected ProposedAction 're-dispatch bead st-123', got %q", diagnosis.ProposedAction)
	}

	// Test Case B: Low Confidence (should empty ProposedAction)
	mockGLMResponse = `{
		"category": "wartet-auf-Mensch",
		"confidence": "Low",
		"reasoning": "Unclear why it is stagnant. Needs human review.",
		"proposed_action": "manual review"
	}`

	diagnosis, err = diagnoseFlaggedCard(init, beads, workspaces, events, flaggedReasons)
	if err != nil {
		t.Fatalf("diagnoseFlaggedCard failed: %v", err)
	}

	if diagnosis.Category != "wartet-auf-Mensch" {
		t.Errorf("expected Category wartet-auf-Mensch, got %q", diagnosis.Category)
	}
	if diagnosis.Confidence != "Low" {
		t.Errorf("expected Confidence Low, got %q", diagnosis.Confidence)
	}
	if diagnosis.ProposedAction != "" {
		t.Errorf("expected ProposedAction to be emptied for Low confidence, got %q", diagnosis.ProposedAction)
	}
}
