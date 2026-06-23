package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGetReposMap(t *testing.T) {
	// Set custom environment
	os.Setenv("PLANFILE_REPOS", "/root/foo=stayawesome,/root/bar=quantbot")
	defer os.Unsetenv("PLANFILE_REPOS")

	repos, paths := getReposMap()

	// Verify stayawesome mapping
	if repos["stayawesome"] != "/root/foo" {
		t.Errorf("expected /root/foo, got %s", repos["stayawesome"])
	}

	// Verify quantbot mapping
	if repos["quantbot"] != "/root/bar" {
		t.Errorf("expected /root/bar, got %s", repos["quantbot"])
	}

	// Verify angeloos still falls back to default
	if repos["angeloos"] != "/opt/stack" {
		t.Errorf("expected /opt/stack, got %s", repos["angeloos"])
	}

	// Paths should be sorted by length descending
	if len(paths) < 2 {
		t.Errorf("expected multiple paths, got %d", len(paths))
	}
	for i := 0; i < len(paths)-1; i++ {
		if len(paths[i]) < len(paths[i+1]) {
			t.Errorf("paths not sorted by length descending: %v", paths)
		}
	}
}

func TestResolveTargetRepo(t *testing.T) {
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

	// Verify connection
	if err := p.Ping(ctx); err != nil {
		t.Skip("skipping integration test; db ping failed:", err)
	}

	// Clean up any leftovers first
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id IN ('test-init-plan', 'test-init-fallback')")

	// Insert test-init-plan (stayawesome)
	_, err = p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend)
		VALUES ('test-init-plan', 'stayawesome', 'idea', 'Test Plan Initiative', 'plan_file')`)
	if err != nil {
		t.Fatalf("failed to insert test-init-plan: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = 'test-init-plan'")

	// Insert plan_file link for test-init-plan
	_, err = p.Exec(ctx, `INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		VALUES ('test-init-plan', 'plan_file', '/root/stayawesomeOS/docs/plans/test-plan.md')`)
	if err != nil {
		t.Fatalf("failed to insert initiative_link: %v", err)
	}

	// Insert test-init-fallback (solartown, no plan_file links)
	_, err = p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend)
		VALUES ('test-init-fallback', 'solartown', 'idea', 'Test Fallback Initiative', 'vk')`)
	if err != nil {
		t.Fatalf("failed to insert test-init-fallback: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = 'test-init-fallback'")

	// Test case 1: test-init-plan should resolve to /root/stayawesomeOS via linked plan file
	repo, err := resolveTargetRepo(p, "test-init-plan")
	if err != nil {
		t.Errorf("resolveTargetRepo failed for test-init-plan: %v", err)
	}
	expectedRepo := "/root/stayawesomeOS"
	if repo != expectedRepo {
		t.Errorf("expected %s, got %s", expectedRepo, repo)
	}

	// Test case 2: test-init-fallback should fallback to /root/solartown via firma→repo map
	repo, err = resolveTargetRepo(p, "test-init-fallback")
	if err != nil {
		t.Errorf("resolveTargetRepo failed for test-init-fallback: %v", err)
	}
	expectedRepo = "/root/solartown"
	if repo != expectedRepo {
		t.Errorf("expected %s, got %s", expectedRepo, repo)
	}
}

func TestDispatch(t *testing.T) {
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

	// Clean up any leftovers first
	testID := "sk-test-dispatch"
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testID)

	// Insert test initiative
	_, err = p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, description, primary_backend)
		VALUES ($1, 'stack', 'idea', 'Test Dispatching Card', 'Testing the dispatch endpoint scaffold generation', 'plan_file')`, testID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testID)

	// Setup payload
	bodyMap := map[string]string{
		"id":   testID,
		"lane": "plan-deep",
		"note": "A note about deep tech plan",
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
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var result struct {
		Ok   bool   `json:"ok"`
		Ref  string `json:"ref"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !result.Ok {
		t.Errorf("expected Ok to be true")
	}

	// Clean up generated file
	if result.Path != "" {
		defer os.Remove(result.Path)
	}

	// Verify file was written
	if _, err := os.Stat(result.Path); os.IsNotExist(err) {
		t.Errorf("scaffold file was not created: %s", result.Path)
	}

	// Verify content of the scaffold file
	contentBytes, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("failed to read created file: %v", err)
	}
	content := string(contentBytes)

	if !strings.Contains(content, "title: Test Dispatching Card") {
		t.Errorf("file frontmatter missing title")
	}
	if !strings.Contains(content, "slug: test-dispatch") {
		t.Errorf("file frontmatter missing slug or incorrect")
	}
	if !strings.Contains(content, "deep: spec-panel") {
		t.Errorf("file frontmatter missing deep: spec-panel for lane=plan-deep")
	}
	if !strings.Contains(content, "panel-mode: critique") {
		t.Errorf("file frontmatter missing panel-mode for lane=plan-deep")
	}

	// Verify link was inserted into DB
	var exists bool
	err = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM portfolio.initiative_link WHERE initiative_id = $1 AND kind = 'plan_file')`, testID).Scan(&exists)
	if err != nil {
		t.Fatalf("failed to check initiative_link: %v", err)
	}
	if !exists {
		t.Errorf("initiative_link for %s was not created", testID)
	}

	// Verify event was logged in DB
	err = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'dispatched')`, testID).Scan(&exists)
	if err != nil {
		t.Fatalf("failed to check initiative_event: %v", err)
	}
	if !exists {
		t.Errorf("initiative_event for %s was not created", testID)
	}
}

func TestDispatchHack(t *testing.T) {
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

	// Setup mock vk-delegate script
	mockScriptPath := filepath.Join(t.TempDir(), "vk-delegate")
	testWorkspaceID := "550e8400-e29b-41d4-a716-446655440000"
	scriptContent := fmt.Sprintf(`#!/bin/sh
echo "workspace_id:        %s"
echo "execution_process:   550e8400-e29b-41d4-a716-446655440001"
echo "workspace_url:       http://localhost:54682/workspaces/%s"
`, testWorkspaceID, testWorkspaceID)

	if err := os.WriteFile(mockScriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to write mock script: %v", err)
	}

	// Override vkDelegatePath
	oldVkPath := vkDelegatePath
	vkDelegatePath = mockScriptPath
	defer func() {
		vkDelegatePath = oldVkPath
	}()

	testID := "sk-test-dispatch-hack"
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testID)

	// Insert test initiative
	_, err = p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, description, primary_backend)
		VALUES ($1, 'stack', 'idea', 'Test Dispatching Card Hack', 'Testing the dispatch endpoint for direct hacking lane', 'plan_file')`, testID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testID)

	// Setup payload
	bodyMap := map[string]string{
		"id":   testID,
		"lane": "hack",
		"note": "A note about direct hack",
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
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var result struct {
		Ok   bool   `json:"ok"`
		Ref  string `json:"ref"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !result.Ok {
		t.Errorf("expected Ok to be true")
	}

	if result.Path != "" {
		t.Errorf("expected no file path to be returned for hack lane, got %s", result.Path)
	}
	if result.Ref != "" {
		t.Errorf("expected no canonical ref to be returned for hack lane, got %s", result.Ref)
	}

	// Verify NO link was inserted into DB
	var exists bool
	err = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM portfolio.initiative_link WHERE initiative_id = $1 AND kind = 'plan_file')`, testID).Scan(&exists)
	if err != nil {
		t.Fatalf("failed to check initiative_link: %v", err)
	}
	if exists {
		t.Errorf("initiative_link for %s should not have been created", testID)
	}

	// Verify event was logged in DB
	err = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'dispatched')`, testID).Scan(&exists)
	if err != nil {
		t.Fatalf("failed to check initiative_event: %v", err)
	}
	if !exists {
		t.Errorf("initiative_event for %s was not created", testID)
	}
}
