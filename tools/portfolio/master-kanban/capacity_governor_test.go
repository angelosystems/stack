package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCapacityGovernor_StressThrottled(t *testing.T) {
	// Create temporary directory for our fake transcripts
	tempDir := t.TempDir()
	projectsDir := filepath.Join(tempDir, "projects")
	err := os.MkdirAll(projectsDir, 0755)
	if err != nil {
		t.Fatalf("failed to create temp projects dir: %v", err)
	}

	stateFilePath := filepath.Join(tempDir, "state.json")

	// Set environment variables for our governor
	os.Setenv("PORTFOLIO_PROJECTS_DIR", projectsDir)
	os.Setenv("PORTFOLIO_STATE_FILE_PATH", stateFilePath)
	defer func() {
		os.Unsetenv("PORTFOLIO_PROJECTS_DIR")
		os.Unsetenv("PORTFOLIO_STATE_FILE_PATH")
		os.Unsetenv("GOV_STRESS_429")
	}()

	ctx := context.Background()

	// Case 1: Flag is NOT enabled. Even if there are 429 errors, it should NOT throttle.
	os.Setenv("GOV_STRESS_429", "false")
	// Let's create a fake transcript with a 429 error
	fakeLogPath := filepath.Join(projectsDir, "session_1.jsonl")
	err = os.WriteFile(fakeLogPath, []byte(`{"apiErrorStatus": 429}`+"\n"), 0644)
	if err != nil {
		t.Fatalf("failed to write fake log: %v", err)
	}

	throttled, _, err := IsProviderStressThrottled(ctx, "anthropic")
	if err != nil {
		t.Fatalf("IsProviderStressThrottled failed: %v", err)
	}
	if throttled {
		t.Errorf("expected no throttling when flag GOV_STRESS_429 is false")
	}

	// Case 2: Flag is enabled, and there are 429 errors. It should throttle.
	os.Setenv("GOV_STRESS_429", "true")
	// Force-remove state file to make sure it parses the log file
	_ = os.Remove(stateFilePath)

	throttled, reason, err := IsProviderStressThrottled(ctx, "anthropic")
	if err != nil {
		t.Fatalf("IsProviderStressThrottled failed: %v", err)
	}
	if !throttled {
		t.Errorf("expected throttling when flag GOV_STRESS_429 is true and 429 exists")
	}
	if !strings.Contains(reason, "throttled due to 429/overload stress") {
		t.Errorf("expected reason to mention throttling, got: %s", reason)
	}

	// Case 3: Flag is enabled, but we clear transcripts (using a new directory). It should NOT throttle.
	emptyProjectsDir := filepath.Join(tempDir, "empty_projects")
	_ = os.MkdirAll(emptyProjectsDir, 0755)
	os.Setenv("PORTFOLIO_PROJECTS_DIR", emptyProjectsDir)
	_ = os.Remove(stateFilePath)

	throttled, _, err = IsProviderStressThrottled(ctx, "anthropic")
	if err != nil {
		t.Fatalf("IsProviderStressThrottled failed: %v", err)
	}
	if throttled {
		t.Errorf("expected no throttling when there are no 429 errors")
	}
}
