package main

import (
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
