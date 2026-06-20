package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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

func TestDispatch_AdmissionStressThrottled(t *testing.T) {
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

	// Create temporary directory for our fake transcripts
	tempDir := t.TempDir()
	projectsDir := filepath.Join(tempDir, "projects")
	_ = os.MkdirAll(projectsDir, 0755)
	stateFilePath := filepath.Join(tempDir, "state.json")

	// Set env vars
	os.Setenv("PORTFOLIO_PROJECTS_DIR", projectsDir)
	os.Setenv("PORTFOLIO_STATE_FILE_PATH", stateFilePath)
	os.Setenv("GOV_STRESS_429", "true")
	defer func() {
		os.Unsetenv("PORTFOLIO_PROJECTS_DIR")
		os.Unsetenv("PORTFOLIO_STATE_FILE_PATH")
		os.Unsetenv("GOV_STRESS_429")
	}()

	// Write a fake transcript with a 429 error for anthropic
	fakeLogPath := filepath.Join(projectsDir, "session_1.jsonl")
	err = os.WriteFile(fakeLogPath, []byte(`{"apiErrorStatus": 429}`+"\n"), 0644)
	if err != nil {
		t.Fatalf("failed to write fake log: %v", err)
	}

	testID := "sk-test-gov-stress-dispatch"
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testID)

	// Insert test initiative
	_, err = p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, description, primary_backend)
		VALUES ($1, 'stack', 'idea', 'Test Gov Stress Dispatch', 'Testing the capacity-governor blocking dispatch', 'plan_file')`, testID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testID)

	// Setup payload for 'plan' lane (triggers anthropic stress check)
	bodyMap := map[string]string{
		"id":   testID,
		"lane": "plan",
		"note": "A note about stress dispatch",
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	// Create request
	req := httptest.NewRequest(http.MethodPost, "/api/dispatch", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	// Call handleDispatch handler
	handler := handleDispatch(p)
	handler(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected status 429 (Too Many Requests), got %d", resp.StatusCode)
	}

	// Verify that the response body mentions "Admission stress"
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	respStr := buf.String()
	if !strings.Contains(respStr, "Admission stress") {
		t.Errorf("expected error message to contain 'Admission stress', got: %s", respStr)
	}
}
