package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	runCmd("init", "-b", "main")
	runCmd("config", "user.name", "Test User")
	runCmd("config", "user.email", "test@example.com")

	// Create initial file and commit
	file1 := filepath.Join(repoPath, "file1.txt")
	err = os.WriteFile(file1, []byte("hello"), 0644)
	if err != nil {
		t.Fatalf("failed to write file1: %v", err)
	}
	runCmd("add", "file1.txt")
	runCmd("commit", "-m", "initial commit")

	// Create branch origin/main at HEAD
	runCmd("branch", "origin/main")

	// Create a second commit ahead of origin/main
	file2 := filepath.Join(repoPath, "file2.txt")
	err = os.WriteFile(file2, []byte("world"), 0644)
	if err != nil {
		t.Fatalf("failed to write file2: %v", err)
	}
	runCmd("add", "file2.txt")
	runCmd("commit", "-m", "second commit")

	// Set VK_SESSIONS environment variable so hasPartialProgress finds our mock
	os.Setenv("VK_SESSIONS", tmpDir)
	defer os.Unsetenv("VK_SESSIONS")

	got := hasPartialProgress(sessionID)
	if !got {
		t.Errorf("expected hasPartialProgress to return true for repo with commits ahead of origin/main")
	}
}

func TestSage_CounterResetAndDiagnosis(t *testing.T) {
	dsn := os.Getenv("PORTFOLIO_DSN")
	if dsn == "" {
		dsn = "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable"
	}

	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skip("skipping integration test; db not reachable:", err)
		return
	}
	defer p.Close()

	if err := p.Ping(ctx); err != nil {
		t.Skip("skipping integration test; db ping failed:", err)
		return
	}

	// 1. Setup a test initiative and clean up
	testBeadID := "sa-test-reset-bead"
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id=$1", testBeadID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id=$1", testBeadID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id=$1", testBeadID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_heal_count WHERE bead_id=$1", testBeadID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_lease WHERE bead_id=$1", testBeadID)

	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, description, heal_count)
		VALUES ($1, 'stayawesome', 'idea', 'Test Reset Bead', 'This is a test description.', 1)
	`, testBeadID)
	if err != nil {
		t.Fatalf("failed to create test initiative: %v", err)
	}
	defer func() {
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id=$1", testBeadID)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id=$1", testBeadID)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id=$1", testBeadID)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_heal_count WHERE bead_id=$1", testBeadID)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_lease WHERE bead_id=$1", testBeadID)
	}()

	// 2. Setup a temporary SQLite database for vibe-kanban
	tmpSqlite, err := os.CreateTemp("", "vk-sqlite-test-*.sqlite")
	if err != nil {
		t.Fatalf("failed to create temp sqlite file: %v", err)
	}
	defer os.Remove(tmpSqlite.Name())
	tmpSqlite.Close()

	schema := `
	CREATE TABLE workspaces (
		id         BLOB PRIMARY KEY,
		name       TEXT,
		created_at TEXT,
		archived   INTEGER DEFAULT 0
	);
	CREATE TABLE sessions (
		id           BLOB PRIMARY KEY,
		workspace_id BLOB,
		created_at   TEXT
	);
	`
	if err := exec.Command("sqlite3", tmpSqlite.Name(), schema).Run(); err != nil {
		t.Fatalf("failed to init temp sqlite schema: %v", err)
	}

	sessionID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sessionUUID := formatUUID(sessionID)

	fixtures := fmt.Sprintf(`
	INSERT INTO workspaces (id, name, created_at, archived) VALUES (x'88888888', 'workspace-for-%s', '2026-06-20 12:00:00', 0);
	INSERT INTO sessions (id, workspace_id, created_at) VALUES (x'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', x'88888888', '2026-06-20 12:01:00');
	`, testBeadID)
	if err := exec.Command("sqlite3", tmpSqlite.Name(), fixtures).Run(); err != nil {
		t.Fatalf("failed to insert temp sqlite fixtures: %v", err)
	}

	// 3. Setup temporary sessions folder and mock a git repo with a commit ahead of origin/main
	tmpSessions, err := ioutil.TempDir("", "vk-sessions-reset-*")
	if err != nil {
		t.Fatalf("failed to create temp sessions dir: %v", err)
	}
	defer os.RemoveAll(tmpSessions)

	prefix := sessionUUID[:2]
	sessionPath := filepath.Join(tmpSessions, prefix, sessionUUID)
	if err := os.MkdirAll(sessionPath, 0755); err != nil {
		t.Fatalf("failed to mkdir: %v", err)
	}

	repoPath := filepath.Join(sessionPath, "repo")
	if err := os.MkdirAll(repoPath, 0755); err != nil {
		t.Fatalf("failed to mkdir repo: %v", err)
	}

	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoPath
		if err := cmd.Run(); err != nil {
			t.Fatalf("git command failed: %v", err)
		}
	}

	runGit("init", "-b", "main")
	runGit("config", "user.name", "Tester")
	runGit("config", "user.email", "tester@example.com")

	f1 := filepath.Join(repoPath, "f1.txt")
	_ = os.WriteFile(f1, []byte("init"), 0644)
	runGit("add", "f1.txt")
	runGit("commit", "-m", "init")
	runGit("branch", "origin/main")

	f2 := filepath.Join(repoPath, "f2.txt")
	_ = os.WriteFile(f2, []byte("commit2"), 0644)
	runGit("add", "f2.txt")
	runGit("commit", "-m", "commit2")

	// Set environmental variables
	origVKDB := os.Getenv("VK_DB")
	origVKSessions := os.Getenv("VK_SESSIONS")
	defer func() {
		os.Setenv("VK_DB", origVKDB)
		os.Setenv("VK_SESSIONS", origVKSessions)
	}()

	os.Setenv("VK_DB", tmpSqlite.Name())
	os.Setenv("VK_SESSIONS", tmpSessions)

	// 4. Run ProcessFailure and verify reset-semantics (SC2 / Newman-Note)
	engine := NewSageDecisionEngine(p, 2)
	res, err := engine.ProcessFailure(ctx, testBeadID)
	if err != nil {
		t.Fatalf("ProcessFailure failed: %v", err)
	}
	if res != "healed" {
		t.Errorf("expected 'healed', got %q", res)
	}

	// Verify heal_count is reset to 0, and then incremented to 1
	var dbHealCount int
	err = p.QueryRow(ctx, "SELECT heal_count FROM portfolio.initiative WHERE id=$1", testBeadID).Scan(&dbHealCount)
	if err != nil {
		t.Fatalf("failed to query heal_count: %v", err)
	}
	if dbHealCount != 1 {
		t.Errorf("expected heal_count to be 1 after reset and heal, got %d", dbHealCount)
	}

	// Verify description contains diagnosis suffix
	var dbDesc string
	err = p.QueryRow(ctx, "SELECT description FROM portfolio.initiative WHERE id=$1", testBeadID).Scan(&dbDesc)
	if err != nil {
		t.Fatalf("failed to query description: %v", err)
	}
	if !strings.Contains(dbDesc, "[Sage Diagnose & Re-Scope (Versuch 1)]") {
		t.Errorf("expected description to contain Sage Diagnose suffix, got: %s", dbDesc)
	}

	// 5. Run ProcessFailure with NO partial progress
	// We do this by pointing to a nonexistent session in SQLite so it finds no commits
	_, _ = p.Exec(ctx, "UPDATE portfolio.initiative SET heal_count = 1 WHERE id=$1", testBeadID)
	// Clear the database with a nonexistent workspace in temp SQLite
	_ = exec.Command("sqlite3", tmpSqlite.Name(), "DELETE FROM workspaces;").Run()

	res2, err := engine.ProcessFailure(ctx, testBeadID)
	if err != nil {
		t.Fatalf("second ProcessFailure failed: %v", err)
	}
	if res2 != "healed" {
		t.Errorf("expected 'healed' for second run, got %q", res2)
	}

	// Verify heal_count is NOT reset, but instead incremented from 1 to 2
	err = p.QueryRow(ctx, "SELECT heal_count FROM portfolio.initiative WHERE id=$1", testBeadID).Scan(&dbHealCount)
	if err != nil {
		t.Fatalf("failed to query second heal_count: %v", err)
	}
	if dbHealCount != 2 {
		t.Errorf("expected heal_count to be 2 with no partial progress, got %d", dbHealCount)
	}

	// Verify description contains Versuch 2 diagnosis suffix
	err = p.QueryRow(ctx, "SELECT description FROM portfolio.initiative WHERE id=$1", testBeadID).Scan(&dbDesc)
	if err != nil {
		t.Fatalf("failed to query description: %v", err)
	}
	if !strings.Contains(dbDesc, "[Sage Diagnose & Re-Scope (Versuch 2)]") {
		t.Errorf("expected description to contain Versuch 2 diagnosis suffix, got: %s", dbDesc)
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
