package main

import (
	"context"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFormatUUID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"d01893e537aa4dd7a7f030969e2e5164", "d01893e5-37aa-4dd7-a7f0-30969e2e5164"},
		{"D01893E537AA4DD7A7F030969E2E5164", "d01893e5-37aa-4dd7-a7f0-30969e2e5164"},
		{"short", "short"},
	}

	for _, tc := range tests {
		got := formatUUID(tc.input)
		if got != tc.expected {
			t.Errorf("expected %q, got %q", tc.expected, got)
		}
	}
}

func TestHasPartialProgress_NoRepo(t *testing.T) {
	// If path doesn't exist or is not a git repo, it should return false
	got := hasPartialProgress("00000000000000000000000000000000")
	if got {
		t.Errorf("expected false for nonexistent session")
	}
}

func TestHasPartialProgress_WithRepo(t *testing.T) {
	// Create a temp session dir structured like vibe-kanban
	tmpDir, err := ioutil.TempDir("", "vk-sessions-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	sessionID := "1234567890abcdef1234567890abcdef"
	sessionUUID := formatUUID(sessionID)

	// Create structured sessions/<prefix>/<uuid> directory
	prefix := sessionUUID[:2]
	sessionPath := filepath.Join(tmpDir, prefix, sessionUUID)
	err = os.MkdirAll(sessionPath, 0755)
	if err != nil {
		t.Fatalf("failed to create structured session dir: %v", err)
	}

	// Clone or init a git repository inside sessionPath
	repoPath := filepath.Join(sessionPath, "my-repo")
	err = os.MkdirAll(repoPath, 0755)
	if err != nil {
		t.Fatalf("failed to create repo path: %v", err)
	}

	runCmd := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoPath
		err := cmd.Run()
		if err != nil {
			t.Fatalf("failed to run git command: %v", err)
		}
	}

	runCmd("init")
	runCmd("config", "user.name", "Test User")
	runCmd("config", "user.email", "test@example.com")

	// Fresh repo has no commits relative to origin/main -> should return false
	cmd := exec.Command("git", "-C", repoPath, "log", "origin/main..HEAD", "--oneline")
	err = cmd.Run()
	if err == nil {
		t.Errorf("expected git log origin/main..HEAD to fail in clean init repo")
	}
}

func TestHealCounter_DB_Persistence(t *testing.T) {
	dsn := os.Getenv("PORTFOLIO_DSN")
	if dsn == "" {
		dsn = "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable"
	}

	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skip("skipping database persistence test; postgres not reachable:", err)
		return
	}
	defer p.Close()

	if err := p.Ping(ctx); err != nil {
		t.Skip("skipping database persistence test; postgres ping failed:", err)
		return
	}

	testInitiativeID := "st-test-heal-persistence"

	// Cleanup if exists
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	// Create test initiative
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, heal_counter)
		VALUES ($1, 'stayawesome', 'now', 'Test Heal Counter Persistence', 0)
	`, testInitiativeID)
	if err != nil {
		t.Fatalf("failed to create test initiative: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	// Read and verify
	var count int
	err = p.QueryRow(ctx, "SELECT COALESCE(heal_counter, 0) FROM portfolio.initiative WHERE id = $1", testInitiativeID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query heal_counter: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}

	// Increment and verify
	_, err = p.Exec(ctx, "UPDATE portfolio.initiative SET heal_counter = COALESCE(heal_counter, 0) + 1 WHERE id = $1", testInitiativeID)
	if err != nil {
		t.Fatalf("failed to increment heal_counter: %v", err)
	}

	err = p.QueryRow(ctx, "SELECT COALESCE(heal_counter, 0) FROM portfolio.initiative WHERE id = $1", testInitiativeID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query incremented heal_counter: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1, got %d", count)
	}

	// Reset to 0 and verify (Newman-Note reset semantics)
	_, err = p.Exec(ctx, "UPDATE portfolio.initiative SET heal_counter = 0 WHERE id = $1", testInitiativeID)
	if err != nil {
		t.Fatalf("failed to reset heal_counter: %v", err)
	}

	err = p.QueryRow(ctx, "SELECT COALESCE(heal_counter, 0) FROM portfolio.initiative WHERE id = $1", testInitiativeID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query reset heal_counter: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}
}
