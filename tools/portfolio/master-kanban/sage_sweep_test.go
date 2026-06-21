package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSageSweep_FilteringAndChannel(t *testing.T) {
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

	// Clean up any pre-existing test events to ensure clean state
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE payload->>'workspace_id' IN ('10101010', '20202020', '30303030')")

	// Look up actual initiative IDs linked to real test beads
	var initID1bpf string
	err = p.QueryRow(ctx, "SELECT initiative_id FROM portfolio.initiative_link WHERE ref='st-1bpf' AND kind='bead'").Scan(&initID1bpf)
	if err != nil {
		t.Fatalf("failed to find initiative_id for st-1bpf: %v", err)
	}

	var initIDYozd string
	err = p.QueryRow(ctx, "SELECT initiative_id FROM portfolio.initiative_link WHERE ref='st-yozd' AND kind='bead'").Scan(&initIDYozd)
	if err != nil {
		t.Fatalf("failed to find initiative_id for st-yozd: %v", err)
	}

	var initIDIb5e string
	err = p.QueryRow(ctx, "SELECT initiative_id FROM portfolio.initiative_link WHERE ref='st-ib5e' AND kind='bead'").Scan(&initIDIb5e)
	if err != nil {
		t.Fatalf("failed to find initiative_id for st-ib5e: %v", err)
	}

	// 1. Create a temporary SQLite database for vibe-kanban
	tmpFile, err := os.CreateTemp("", "vibe-kanban-sweep-test-*.sqlite")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	dbPath := tmpFile.Name()

	// 2. Initialize schema
	schema := `
	CREATE TABLE workspaces (
		id         BLOB PRIMARY KEY,
		name       TEXT,
		created_at TEXT,
		task_id    BLOB,
		archived   INTEGER DEFAULT 0
	);
	CREATE TABLE sessions (
		id           BLOB PRIMARY KEY,
		workspace_id BLOB
	);
	CREATE TABLE execution_processes (
		id         BLOB PRIMARY KEY,
		session_id BLOB,
		status     TEXT,
		exit_code  INTEGER,
		started_at TEXT,
		updated_at TEXT,
		created_at TEXT,
		run_reason TEXT
	);
	`
	if err := exec.Command("sqlite3", dbPath, schema).Run(); err != nil {
		t.Fatalf("failed to initialize sqlite schema: %v", err)
	}

	// 3. Insert SQLite fixtures
	now := time.Now()
	stuckTimeStr := now.Add(-2 * time.Hour).UTC().Format("2006-01-02T15:04:05Z")
	healthyTimeStr := now.UTC().Format("2006-01-02T15:04:05Z")

	fixtures := fmt.Sprintf(`
	-- WS1: Failed workspace (should NOT be detected under onlyStuckCheck)
	INSERT INTO workspaces (id, name, created_at, task_id, archived) VALUES (x'10101010', 'sol-st-yozd', '%[1]s', x'aaaaaaaa', 0);
	INSERT INTO sessions (id, workspace_id) VALUES (x'11111111', x'10101010');
	INSERT INTO execution_processes (id, session_id, status, exit_code, started_at, updated_at, created_at, run_reason)
	VALUES (x'12121212', x'11111111', 'failed', 1, '%[1]s', '%[1]s', '%[1]s', 'codingagent');

	-- WS2: Stuck running workspace (should be detected under onlyStuckCheck)
	INSERT INTO workspaces (id, name, created_at, task_id, archived) VALUES (x'20202020', 'sol-st-1bpf', '%[1]s', x'bbbbbbbb', 0);
	INSERT INTO sessions (id, workspace_id) VALUES (x'21212121', x'20202020');
	INSERT INTO execution_processes (id, session_id, status, exit_code, started_at, updated_at, created_at, run_reason)
	VALUES (x'22222222', x'21212121', 'running', NULL, '%[1]s', '%[1]s', '%[1]s', 'codingagent');

	-- WS3: Healthy running workspace (should NOT be detected under onlyStuckCheck)
	INSERT INTO workspaces (id, name, created_at, task_id, archived) VALUES (x'30303030', 'sol-st-ib5e', '%[2]s', x'cccccccc', 0);
	INSERT INTO sessions (id, workspace_id) VALUES (x'31313131', x'30303030');
	INSERT INTO execution_processes (id, session_id, status, exit_code, started_at, updated_at, created_at, run_reason)
	VALUES (x'32323232', x'31313131', 'running', NULL, '%[2]s', '%[2]s', '%[2]s', 'codingagent');
	`, stuckTimeStr, healthyTimeStr)

	if err := exec.Command("sqlite3", dbPath, fixtures).Run(); err != nil {
		t.Fatalf("failed to insert test fixtures: %v", err)
	}

	// Set VK_DB env variable
	origVkDB := os.Getenv("VIBE_KANBAN_DB")
	defer os.Setenv("VIBE_KANBAN_DB", origVkDB)
	os.Setenv("VIBE_KANBAN_DB", dbPath)

	// Set SAGE_STUCK_TIMEOUT to a low value for testing
	defer os.Unsetenv("SAGE_STUCK_TIMEOUT")
	os.Setenv("SAGE_STUCK_TIMEOUT", "5m")

	// --- STEP 1: Run Stuck-Only Sweep ---
	err = runSageSweep(p, true, true)
	if err != nil {
		t.Fatalf("runSageSweep(onlyStuckCheck=true) failed: %v", err)
	}

	// Verify that ONLY WS2 (st-1bpf) got an event logged
	var count1bpf int
	err = p.QueryRow(ctx, "SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'sage_action' AND payload->>'workspace_id' = '20202020'", initID1bpf).Scan(&count1bpf)
	if err != nil {
		t.Fatalf("failed to query events for st-1bpf: %v", err)
	}
	if count1bpf != 1 {
		t.Errorf("expected exactly 1 event for stuck workspace st-1bpf, got %d", count1bpf)
	}

	var countYozd int
	err = p.QueryRow(ctx, "SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'sage_action' AND payload->>'workspace_id' = '10101010'", initIDYozd).Scan(&countYozd)
	if err != nil {
		t.Fatalf("failed to query events for st-yozd: %v", err)
	}
	if countYozd != 0 {
		t.Errorf("expected 0 events for failed workspace st-yozd under stuck-only check, got %d", countYozd)
	}

	var countIb5e int
	err = p.QueryRow(ctx, "SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'sage_action' AND payload->>'workspace_id' = '30303030'", initIDIb5e).Scan(&countIb5e)
	if err != nil {
		t.Fatalf("failed to query events for st-ib5e: %v", err)
	}
	if countIb5e != 0 {
		t.Errorf("expected 0 events for healthy running workspace st-ib5e, got %d", countIb5e)
	}

	// --- STEP 2: Run Full Sweep ---
	err = runSageSweep(p, true, false)
	if err != nil {
		t.Fatalf("runSageSweep(onlyStuckCheck=false) failed: %v", err)
	}

	// Now, WS1 (st-yozd) should ALSO have an event logged!
	err = p.QueryRow(ctx, "SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'sage_action' AND payload->>'workspace_id' = '10101010'", initIDYozd).Scan(&countYozd)
	if err != nil {
		t.Fatalf("failed to query events for st-yozd: %v", err)
	}
	if countYozd != 1 {
		t.Errorf("expected exactly 1 event for failed workspace st-yozd after full sweep, got %d", countYozd)
	}

	// Healthy running workspace should STILL have 0 events because it is healthy!
	err = p.QueryRow(ctx, "SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'sage_action' AND payload->>'workspace_id' = '30303030'", initIDIb5e).Scan(&countIb5e)
	if err != nil {
		t.Fatalf("failed to query events for st-ib5e: %v", err)
	}
	if countIb5e != 0 {
		t.Errorf("expected healthy workspace st-ib5e to remain untouched with 0 events, got %d", countIb5e)
	}
}
