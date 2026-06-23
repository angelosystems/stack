package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

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
			t.Errorf("status %q: expected %v, got %v", tc.status, tc.expected, res)
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
