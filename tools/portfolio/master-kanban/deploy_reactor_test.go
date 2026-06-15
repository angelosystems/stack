package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runCmd(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("failed to run cmd %s %v in %s: %v, output: %s", name, args, dir, err, string(out))
	}
	return string(out)
}

func TestGithubWebhookHandler_SuccessAndBlock(t *testing.T) {
	// 1. Setup temporary directories and mock systemctl
	tempDir := t.TempDir()
	binDir := t.TempDir()

	// Write a mock systemctl script
	mockSystemctlPath := filepath.Join(binDir, "systemctl")
	err := os.WriteFile(mockSystemctlPath, []byte("#!/bin/sh\nexit 0\n"), 0755)
	if err != nil {
		t.Fatalf("failed to write mock systemctl: %v", err)
	}

	// Prepend binDir to PATH
	origPath := os.Getenv("PATH")
	defer os.Setenv("PATH", origPath)
	os.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)

	// 2. Setup mock git repository for the repo to deploy
	repoDir := filepath.Join(tempDir, "stack")
	err = os.MkdirAll(repoDir, 0755)
	if err != nil {
		t.Fatalf("failed to create repo dir: %v", err)
	}

	runCmd(t, repoDir, "git", "init")
	runCmd(t, repoDir, "git", "config", "user.name", "Test User")
	runCmd(t, repoDir, "git", "config", "user.email", "test@example.com")
	runCmd(t, repoDir, "git", "config", "commit.gpgsign", "false")
	runCmd(t, repoDir, "git", "remote", "add", "origin", repoDir)

	// Create initial file
	dummyPath := filepath.Join(repoDir, "dummy.txt")
	err = os.WriteFile(dummyPath, []byte("hello"), 0644)
	if err != nil {
		t.Fatalf("failed to write dummy.txt: %v", err)
	}
	runCmd(t, repoDir, "git", "add", "dummy.txt")
	runCmd(t, repoDir, "git", "commit", "-m", "initial commit")

	// Create the deploy script in the repo
	scriptSubdir := filepath.Join(repoDir, "tools/portfolio/master-kanban")
	err = os.MkdirAll(scriptSubdir, 0755)
	if err != nil {
		t.Fatalf("failed to create script subdir: %v", err)
	}
	deployScriptPath := filepath.Join(scriptSubdir, "deploy.sh")
	err = os.WriteFile(deployScriptPath, []byte("#!/bin/sh\necho \"deploying SHA $1\"\nexit 0\n"), 0755)
	if err != nil {
		t.Fatalf("failed to write deploy.sh: %v", err)
	}
	runCmd(t, repoDir, "git", "add", "tools/portfolio/master-kanban/deploy.sh")
	runCmd(t, repoDir, "git", "commit", "-m", "add deploy.sh")

	// Get HEAD SHA (the one we want to deploy)
	headSha := strings.TrimSpace(runCmd(t, repoDir, "git", "rev-parse", "HEAD"))

	// 3. Set up the mock HTTP server for the health check
	var mockServerSha string
	healthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"version": mockServerSha,
		})
	}))
	defer healthSrv.Close()

	// 4. Create the deploy manifest file
	manifestData := `
repos:
  "angelosystems/stack":
    path: "` + repoDir + `"
    services:
      - name: "master-kanban-serve"
        deploy_script: "tools/portfolio/master-kanban/deploy.sh"
        health_probe: "` + healthSrv.URL + `"
`
	manifestPath := filepath.Join(tempDir, "deploy-manifest.yaml")
	err = os.WriteFile(manifestPath, []byte(manifestData), 0644)
	if err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	// 5. Setup GITHUB_WEBHOOK_SECRET
	secretKey := "mysecret"
	origSecret := os.Getenv("GITHUB_WEBHOOK_SECRET")
	defer os.Setenv("GITHUB_WEBHOOK_SECRET", origSecret)
	os.Setenv("GITHUB_WEBHOOK_SECRET", secretKey)

	// --- TEST CASE 1: Successful Pull Request Merge ---
	mockServerSha = headSha

	payloadMap := map[string]any{
		"action": "closed",
		"pull_request": map[string]any{
			"merged":             true,
			"merge_commit_sha":   headSha,
			"base": map[string]any{
				"ref": "main",
			},
		},
		"repository": map[string]any{
			"full_name": "angelosystems/stack",
		},
	}
	payloadBytes, err := json.Marshal(payloadMap)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}

	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write(payloadBytes)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest("POST", "/api/github-webhook", strings.NewReader(string(payloadBytes)))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", signature)

	rec := httptest.NewRecorder()

	handler := makeGithubWebhookHandler(nil, manifestPath)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d. Body: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Ok     bool   `json:"ok"`
		Status string `json:"status"`
		Sha    string `json:"sha"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v, Body: %s", err, rec.Body.String())
	}

	if !resp.Ok || resp.Status != "deployed" || resp.Sha != headSha {
		t.Errorf("unexpected response: %+v", resp)
	}

	// --- TEST CASE 2: Blocked Migrations ---
	// Create a schema file and commit it
	schemaDir := filepath.Join(repoDir, "schema")
	err = os.MkdirAll(schemaDir, 0755)
	if err != nil {
		t.Fatalf("failed to create schema dir: %v", err)
	}
	err = os.WriteFile(filepath.Join(schemaDir, "portfolio-005.sql"), []byte("CREATE TABLE foo;"), 0644)
	if err != nil {
		t.Fatalf("failed to write schema file: %v", err)
	}
	runCmd(t, repoDir, "git", "add", "schema/portfolio-005.sql")
	runCmd(t, repoDir, "git", "commit", "-m", "add migration")

	newHeadSha := strings.TrimSpace(runCmd(t, repoDir, "git", "rev-parse", "HEAD"))

	payloadMap["pull_request"].(map[string]any)["merge_commit_sha"] = newHeadSha
	payloadBytes, err = json.Marshal(payloadMap)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}

	mac = hmac.New(sha256.New, []byte(secretKey))
	mac.Write(payloadBytes)
	signature = "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req = httptest.NewRequest("POST", "/api/github-webhook", strings.NewReader(string(payloadBytes)))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-Hub-Signature-256", signature)

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d. Body: %s", rec.Code, rec.Body.String())
	}

	var blockedResp struct {
		Ok     bool   `json:"ok"`
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &blockedResp); err != nil {
		t.Fatalf("failed to decode response: %v, Body: %s", err, rec.Body.String())
	}

	if !blockedResp.Ok || blockedResp.Status != "blocked" || blockedResp.Reason != "migrations" {
		t.Errorf("expected blocked migrations, got %+v", blockedResp)
	}
}

func TestGithubWebhookHandler_AuthFailure(t *testing.T) {
	tempDir := t.TempDir()
	manifestPath := filepath.Join(tempDir, "deploy-manifest.yaml")
	_ = os.WriteFile(manifestPath, []byte("repos: {}"), 0644)

	secretKey := "mysecret"
	origSecret := os.Getenv("GITHUB_WEBHOOK_SECRET")
	defer os.Setenv("GITHUB_WEBHOOK_SECRET", origSecret)
	os.Setenv("GITHUB_WEBHOOK_SECRET", secretKey)

	req := httptest.NewRequest("POST", "/api/github-webhook", strings.NewReader("some body"))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", "sha256=invalid")

	rec := httptest.NewRecorder()
	handler := makeGithubWebhookHandler(nil, manifestPath)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %d", rec.Code)
	}
}

func TestGithubWebhookHandler_InvalidMethod(t *testing.T) {
	tempDir := t.TempDir()
	manifestPath := filepath.Join(tempDir, "deploy-manifest.yaml")
	_ = os.WriteFile(manifestPath, []byte("repos: {}"), 0644)

	req := httptest.NewRequest("GET", "/api/github-webhook", nil)
	rec := httptest.NewRecorder()
	handler := makeGithubWebhookHandler(nil, manifestPath)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed, got %d", rec.Code)
	}
}
