//go:build integration

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

func TestDispatch_AdmissionStressThrottled(t *testing.T) {
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
