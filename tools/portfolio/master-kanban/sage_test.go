package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSageSteward_API(t *testing.T) {
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

	// 1. Initial health clean up
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_status WHERE id = 'sage-steward'")
	_, err = p.Exec(ctx,
		`INSERT INTO portfolio.sage_status (id, last_run, status, error_message)
		 VALUES ('sage-steward', $1, 'healthy', NULL)`,
		time.Now())
	if err != nil {
		t.Fatalf("failed to insert initial sage status: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.sage_status WHERE id = 'sage-steward'")

	// Create test server mux and register handlers
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sage/status", func(w http.ResponseWriter, r *http.Request) {
		var lastRun time.Time
		var status string
		var errMsg *string
		err := p.QueryRow(r.Context(),
			`SELECT last_run, status, error_message FROM portfolio.sage_status WHERE id = 'sage-steward'`).
			Scan(&lastRun, &status, &errMsg)
		if err != nil {
			lastRun = time.Now()
			status = "unknown"
		}

		if time.Since(lastRun) > 30*time.Second {
			status = "alarm"
		}

		dangling, _ := getDanglingWorkspaces()

		resp := map[string]any{
			"last_run":          lastRun,
			"status":            status,
			"error_message":     errMsg,
			"dangling_count":    len(dangling),
			"dangling_baseline": 4,
			"outage_simulated":  sageOutageSimulated,
		}
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/api/sage/simulate-outage", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Simulate bool `json:"simulate"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}

		if body.Simulate {
			sageOutageSimulated = true
			_, _ = p.Exec(r.Context(),
				`UPDATE portfolio.sage_status 
				 SET last_run = now() - interval '10 minutes', status = 'alarm' 
				 WHERE id = 'sage-steward'`)
		} else {
			sageOutageSimulated = false
			_, _ = p.Exec(r.Context(),
				`UPDATE portfolio.sage_status 
				 SET last_run = now(), status = 'healthy', error_message = NULL 
				 WHERE id = 'sage-steward'`)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	// 2. Test GET /api/sage/status - initially healthy
	resp, err := http.Get(server.URL + "/api/sage/status")
	if err != nil {
		t.Fatalf("failed to send GET request: %v", err)
	}
	defer resp.Body.Close()

	var initialData map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&initialData); err != nil {
		t.Fatalf("failed to decode initial GET response: %v", err)
	}

	if initialData["status"] != "healthy" {
		t.Errorf("expected status 'healthy', got %q", initialData["status"])
	}
	if initialData["outage_simulated"] != false {
		t.Errorf("expected outage_simulated to be false")
	}
	if initialData["dangling_count"] == nil {
		t.Errorf("expected response to contain 'dangling_count'")
	}
	if initialData["dangling_baseline"] != 4.0 {
		t.Errorf("expected dangling_baseline to be 4, got %v", initialData["dangling_baseline"])
	}

	// 3. Test POST /api/sage/simulate-outage - activate simulation
	postBody, _ := json.Marshal(map[string]any{"simulate": true})
	postResp, err := http.Post(server.URL+"/api/sage/simulate-outage", "application/json", bytes.NewReader(postBody))
	if err != nil {
		t.Fatalf("failed to send POST simulate-outage request: %v", err)
	}
	defer postResp.Body.Close()

	if postResp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", postResp.StatusCode)
	}

	// 4. Test GET /api/sage/status - should now be in alarm instantly!
	resp2, err := http.Get(server.URL + "/api/sage/status")
	if err != nil {
		t.Fatalf("failed to send second GET request: %v", err)
	}
	defer resp2.Body.Close()

	var simulatedData map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&simulatedData); err != nil {
		t.Fatalf("failed to decode simulated GET response: %v", err)
	}

	if simulatedData["status"] != "alarm" {
		t.Errorf("expected status 'alarm' after simulating outage, got %q", simulatedData["status"])
	}
	if simulatedData["outage_simulated"] != true {
		t.Errorf("expected outage_simulated to be true")
	}

	// 5. Test POST /api/sage/simulate-outage - stop simulation
	postBodyReset, _ := json.Marshal(map[string]any{"simulate": false})
	postRespReset, err := http.Post(server.URL+"/api/sage/simulate-outage", "application/json", bytes.NewReader(postBodyReset))
	if err != nil {
		t.Fatalf("failed to send POST reset request: %v", err)
	}
	defer postRespReset.Body.Close()

	// 6. Test GET /api/sage/status - should be healthy again
	resp3, err := http.Get(server.URL + "/api/sage/status")
	if err != nil {
		t.Fatalf("failed to send third GET request: %v", err)
	}
	defer resp3.Body.Close()

	var recoveredData map[string]any
	if err := json.NewDecoder(resp3.Body).Decode(&recoveredData); err != nil {
		t.Fatalf("failed to decode recovered GET response: %v", err)
	}

	if recoveredData["status"] != "healthy" {
		t.Errorf("expected status 'healthy' after resetting simulate-outage, got %q", recoveredData["status"])
	}
	if recoveredData["outage_simulated"] != false {
		t.Errorf("expected outage_simulated to be false after reset")
	}
}

func TestGetDanglingWorkspaces(t *testing.T) {
	// Create a temporary file for sqlite DB
	tmpFile, err := os.CreateTemp("", "test-dangling-*.sqlite")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpFilePath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpFilePath)

	// Set VK_DB to point to this temp DB
	oldVkDB := os.Getenv("VK_DB")
	os.Setenv("VK_DB", tmpFilePath)
	defer func() {
		if oldVkDB != "" {
			os.Setenv("VK_DB", oldVkDB)
		} else {
			os.Unsetenv("VK_DB")
		}
	}()

	// Initialize the schema inside sqlite3 using command execution
	initSchema := `
		CREATE TABLE workspaces (
			id BLOB PRIMARY KEY,
			name TEXT,
			created_at TEXT,
			archived INTEGER DEFAULT 0
		);
		CREATE TABLE sessions (
			id BLOB PRIMARY KEY,
			workspace_id BLOB
		);
		CREATE TABLE execution_processes (
			session_id BLOB,
			status TEXT,
			exit_code INTEGER,
			run_reason TEXT,
			created_at TEXT
		);
		CREATE TABLE pull_requests (
			workspace_id BLOB,
			pr_status TEXT
		);
	`
	cmdInit := exec.Command("sqlite3", tmpFilePath, initSchema)
	if err := cmdInit.Run(); err != nil {
		t.Fatalf("failed to initialize sqlite schema: %v", err)
	}

	// Insert mock data
	// 1. A dangling workspace:
	// - archived = 0
	// - created_at = 20 hours ago
	// - EP status = 'failed' (terminal)
	// - run_reason = 'codingagent'
	// - no open PR
	tAgo := time.Now().Add(-20 * time.Hour).Format("2006-01-02 15:04:05")
	tNow := time.Now().Format("2006-01-02 15:04:05")

	wsDanglingID := "AABBCCDDEEFF00112233445566778899"
	insertData := fmt.Sprintf(`
		INSERT INTO workspaces (id, name, created_at, archived) VALUES (x'%s', 'dangling-ws', '%s', 0);
		INSERT INTO sessions (id, workspace_id) VALUES (x'11111111111111111111111111111111', x'%s');
		INSERT INTO execution_processes (session_id, status, exit_code, run_reason, created_at) VALUES (x'11111111111111111111111111111111', 'failed', 1, 'codingagent', '%s');
	`, wsDanglingID, tAgo, wsDanglingID, tAgo)

	// 2. A non-dangling workspace (active / running):
	// - archived = 0
	// - created_at = 1 hour ago (too young)
	wsActiveID := "112233445566778899AABBCCDDEEFF00"
	insertData += fmt.Sprintf(`
		INSERT INTO workspaces (id, name, created_at, archived) VALUES (x'%s', 'active-ws', '%s', 0);
		INSERT INTO sessions (id, workspace_id) VALUES (x'22222222222222222222222222222222', x'%s');
		INSERT INTO execution_processes (session_id, status, exit_code, run_reason, created_at) VALUES (x'22222222222222222222222222222222', 'inprogress', NULL, 'codingagent', '%s');
	`, wsActiveID, tNow, wsActiveID, tNow)

	// 3. An archived workspace
	// - archived = 1
	// - created_at = 20 hours ago
	wsArchivedID := "2233445566778899AABBCCDDEEFF0011"
	insertData += fmt.Sprintf(`
		INSERT INTO workspaces (id, name, created_at, archived) VALUES (x'%s', 'archived-ws', '%s', 1);
		INSERT INTO sessions (id, workspace_id) VALUES (x'33333333333333333333333333333333', x'%s');
		INSERT INTO execution_processes (session_id, status, exit_code, run_reason, created_at) VALUES (x'33333333333333333333333333333333', 'completed', 0, 'codingagent', '%s');
	`, wsArchivedID, tAgo, wsArchivedID, tAgo)

	// 4. A workspace with an open PR
	// - archived = 0
	// - created_at = 20 hours ago
	// - open PR
	wsWithPRID := "33445566778899AABBCCDDEEFF001122"
	insertData += fmt.Sprintf(`
		INSERT INTO workspaces (id, name, created_at, archived) VALUES (x'%s', 'pr-ws', '%s', 0);
		INSERT INTO sessions (id, workspace_id) VALUES (x'44444444444444444444444444444444', x'%s');
		INSERT INTO execution_processes (session_id, status, exit_code, run_reason, created_at) VALUES (x'44444444444444444444444444444444', 'completed', 0, 'codingagent', '%s');
		INSERT INTO pull_requests (workspace_id, pr_status) VALUES (x'%s', 'open');
	`, wsWithPRID, tAgo, wsWithPRID, tAgo, wsWithPRID)

	cmdInsert := exec.Command("sqlite3", tmpFilePath, insertData)
	if err := cmdInsert.Run(); err != nil {
		t.Fatalf("failed to insert test data: %v", err)
	}

	// Call the function under test
	dangling, err := getDanglingWorkspaces()
	if err != nil {
		t.Fatalf("getDanglingWorkspaces failed: %v", err)
	}

	// Verify results
	if len(dangling) != 1 {
		t.Errorf("expected exactly 1 dangling workspace, got %d", len(dangling))
	} else {
		ws := dangling[0]
		if strings.ToLower(ws.ID) != strings.ToLower(wsDanglingID) {
			t.Errorf("expected dangling workspace ID to be %s (case-insensitive), got %s", wsDanglingID, ws.ID)
		}
		if ws.Name != "dangling-ws" {
			t.Errorf("expected dangling workspace Name to be 'dangling-ws', got %s", ws.Name)
		}
		if ws.EPStatus != "failed" {
			t.Errorf("expected EPStatus to be 'failed', got %s", ws.EPStatus)
		}
		if ws.ExitCode == nil || *ws.ExitCode != 1 {
			t.Errorf("expected ExitCode to be 1, got %v", ws.ExitCode)
		}
	}
}
