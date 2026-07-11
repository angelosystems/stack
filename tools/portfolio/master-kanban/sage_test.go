package main

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestGetDanglingWorkspaces(t *testing.T) {
	// 1. Create a temporary SQLite database
	tmpFile, err := os.CreateTemp("", "vibe-kanban-test-*.sqlite")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	dbPath := tmpFile.Name()

	// 2. Open / initialize schema using sqlite3 command-line
	schema := `
	CREATE TABLE workspaces (
		id         BLOB PRIMARY KEY,
		name       TEXT,
		created_at TEXT,
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
		created_at TEXT,
		run_reason TEXT
	);
	CREATE TABLE pull_requests (
		id           TEXT PRIMARY KEY,
		workspace_id BLOB,
		pr_status    TEXT
	);
	`
	cmdSchema := exec.Command("sqlite3", dbPath, schema)
	if err := cmdSchema.Run(); err != nil {
		t.Fatalf("failed to initialize sqlite schema: %v", err)
	}

	// 3. Insert test fixtures using sqlite3 command-line
	now := time.Now()
	yesterdayStr := now.Add(-24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	recentStr := now.Add(-2 * time.Hour).UTC().Format("2006-01-02 15:04:05")

	fixtures := fmt.Sprintf(`
	-- WS1: Dangling
	INSERT INTO workspaces (id, name, created_at, archived) VALUES (x'01010101', 'ws1-dangling', '%[1]s', 0);
	INSERT INTO sessions (id, workspace_id) VALUES (x'11111111', x'01010101');
	INSERT INTO execution_processes (id, session_id, status, exit_code, created_at, run_reason) VALUES (x'21212121', x'11111111', 'failed', 1, '%[1]s', 'codingagent');

	-- WS2: Archived
	INSERT INTO workspaces (id, name, created_at, archived) VALUES (x'02020202', 'ws2-archived', '%[1]s', 1);
	INSERT INTO sessions (id, workspace_id) VALUES (x'12121212', x'02020202');
	INSERT INTO execution_processes (id, session_id, status, exit_code, created_at, run_reason) VALUES (x'22222222', x'12121212', 'failed', 1, '%[1]s', 'codingagent');

	-- WS3: Too new
	INSERT INTO workspaces (id, name, created_at, archived) VALUES (x'03030303', 'ws3-new', '%[2]s', 0);
	INSERT INTO sessions (id, workspace_id) VALUES (x'13131313', x'03030303');
	INSERT INTO execution_processes (id, session_id, status, exit_code, created_at, run_reason) VALUES (x'23232323', x'13131313', 'failed', 1, '%[2]s', 'codingagent');

	-- WS4: Still running
	INSERT INTO workspaces (id, name, created_at, archived) VALUES (x'04040404', 'ws4-running', '%[1]s', 0);
	INSERT INTO sessions (id, workspace_id) VALUES (x'14141414', x'04040404');
	INSERT INTO execution_processes (id, session_id, status, exit_code, created_at, run_reason) VALUES (x'24242424', x'14141414', 'running', NULL, '%[1]s', 'codingagent');

	-- WS5: Has open PR
	INSERT INTO workspaces (id, name, created_at, archived) VALUES (x'05050505', 'ws5-has-pr', '%[1]s', 0);
	INSERT INTO sessions (id, workspace_id) VALUES (x'15151515', x'05050505');
	INSERT INTO execution_processes (id, session_id, status, exit_code, created_at, run_reason) VALUES (x'25252525', x'15151515', 'completed', 0, '%[1]s', 'codingagent');
	INSERT INTO pull_requests (id, workspace_id, pr_status) VALUES ('pr-5', x'05050505', 'open');
	`, yesterdayStr, recentStr)

	cmdFixtures := exec.Command("sqlite3", dbPath, fixtures)
	if err := cmdFixtures.Run(); err != nil {
		t.Fatalf("failed to insert test fixtures: %v", err)
	}

	// 4. Temporarily set VK_DB and run getDanglingWorkspaces()
	origVkDB := os.Getenv("VK_DB")
	defer os.Setenv("VK_DB", origVkDB)
	os.Setenv("VK_DB", dbPath)

	dangling, err := getDanglingWorkspaces()
	if err != nil {
		t.Fatalf("getDanglingWorkspaces() failed: %v", err)
	}

	// 5. Verify the results
	if len(dangling) != 1 {
		t.Errorf("expected 1 dangling workspace, got %d: %+v", len(dangling), dangling)
	} else {
		ws := dangling[0]
		if ws.ID != "01010101" {
			t.Errorf("expected dangling workspace ID '01010101', got %q", ws.ID)
		}
		if ws.Name != "ws1-dangling" {
			t.Errorf("expected dangling workspace name 'ws1-dangling', got %q", ws.Name)
		}
		if ws.EPStatus != "failed" {
			t.Errorf("expected dangling workspace ep_status 'failed', got %q", ws.EPStatus)
		}
		if ws.ExitCode == nil || *ws.ExitCode != 1 {
			t.Errorf("expected dangling workspace exit_code 1, got %+v", ws.ExitCode)
		}
	}
}
