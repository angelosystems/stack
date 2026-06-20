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
	"path/filepath"
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
	tmpDir, err := os.MkdirTemp("", "sage-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test-vk.sqlite")

	schema := `
CREATE TABLE workspaces (
    id                 BLOB PRIMARY KEY,
    name               TEXT,
    created_at         TEXT NOT NULL,
    archived           INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE sessions (
    id              BLOB PRIMARY KEY,
    workspace_id    BLOB NOT NULL
);
CREATE TABLE execution_processes (
    id              BLOB PRIMARY KEY,
    session_id      BLOB NOT NULL,
    status          TEXT NOT NULL,
    exit_code       INTEGER,
    run_reason      TEXT NOT NULL,
    created_at      TEXT NOT NULL
);
CREATE TABLE pull_requests (
    id TEXT PRIMARY KEY NOT NULL,
    workspace_id BLOB,
    pr_status TEXT NOT NULL
);
`

	cmd := exec.Command("sqlite3", dbPath, schema)
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to initialize sqlite DB schema: %v", err)
	}

	now := time.Now()
	olderThan12h := now.Add(-13 * time.Hour).Format("2006-01-02 15:04:05")
	recent := now.Add(-1 * time.Hour).Format("2006-01-02 15:04:05")

	dataInserts := fmt.Sprintf(`
-- WS1 (Dangling)
INSERT INTO workspaces (id, name, created_at, archived) VALUES (x'00000000000000000000000000000001', 'ws1-dangling', '%[1]s', 0);
INSERT INTO sessions (id, workspace_id) VALUES (x'10000000000000000000000000000001', x'00000000000000000000000000000001');
INSERT INTO execution_processes (id, session_id, status, exit_code, run_reason, created_at) 
VALUES (x'20000000000000000000000000000001', x'10000000000000000000000000000001', 'completed', 0, 'codingagent', '%[1]s');

-- WS2 (Not dangling: archived)
INSERT INTO workspaces (id, name, created_at, archived) VALUES (x'00000000000000000000000000000002', 'ws2-archived', '%[1]s', 1);
INSERT INTO sessions (id, workspace_id) VALUES (x'10000000000000000000000000000002', x'00000000000000000000000000000002');
INSERT INTO execution_processes (id, session_id, status, exit_code, run_reason, created_at) 
VALUES (x'20000000000000000000000000000002', x'10000000000000000000000000000002', 'completed', 0, 'codingagent', '%[1]s');

-- WS3 (Not dangling: has open PR)
INSERT INTO workspaces (id, name, created_at, archived) VALUES (x'00000000000000000000000000000003', 'ws3-open-pr', '%[1]s', 0);
INSERT INTO sessions (id, workspace_id) VALUES (x'10000000000000000000000000000003', x'00000000000000000000000000000003');
INSERT INTO execution_processes (id, session_id, status, exit_code, run_reason, created_at) 
VALUES (x'20000000000000000000000000000003', x'10000000000000000000000000000003', 'completed', 0, 'codingagent', '%[1]s');
INSERT INTO pull_requests (id, workspace_id, pr_status) VALUES ('pr3', x'00000000000000000000000000000003', 'open');

-- WS4 (Not dangling: running execution process)
INSERT INTO workspaces (id, name, created_at, archived) VALUES (x'00000000000000000000000000000004', 'ws4-running-ep', '%[1]s', 0);
INSERT INTO sessions (id, workspace_id) VALUES (x'10000000000000000000000000000004', x'00000000000000000000000000000004');
INSERT INTO execution_processes (id, session_id, status, exit_code, run_reason, created_at) 
VALUES (x'20000000000000000000000000000004', x'10000000000000000000000000000004', 'running', NULL, 'codingagent', '%[1]s');

-- WS5 (Not dangling: too young)
INSERT INTO workspaces (id, name, created_at, archived) VALUES (x'00000000000000000000000000000005', 'ws5-young', '%[2]s', 0);
INSERT INTO sessions (id, workspace_id) VALUES (x'10000000000000000000000000000005', x'00000000000000000000000000000005');
INSERT INTO execution_processes (id, session_id, status, exit_code, run_reason, created_at) 
VALUES (x'20000000000000000000000000000005', x'10000000000000000000000000000005', 'completed', 0, 'codingagent', '%[2]s');
`, olderThan12h, recent)

	cmd = exec.Command("sqlite3", dbPath, dataInserts)
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to insert test data into sqlite DB: %v", err)
	}

	origVkDb := os.Getenv("VK_DB")
	os.Setenv("VK_DB", dbPath)
	defer os.Setenv("VK_DB", origVkDb)

	dangling, err := getDanglingWorkspaces()
	if err != nil {
		t.Fatalf("getDanglingWorkspaces failed: %v", err)
	}

	if len(dangling) != 1 {
		t.Errorf("expected 1 dangling workspace, got %d", len(dangling))
	} else {
		ws := dangling[0]
		if ws.ID != "00000000000000000000000000000001" {
			t.Errorf("expected dangling workspace ID '00000000000000000000000000000001', got %q", ws.ID)
		}
		if ws.Name != "ws1-dangling" {
			t.Errorf("expected name 'ws1-dangling', got %q", ws.Name)
		}
		if ws.EPStatus != "completed" {
			t.Errorf("expected ep_status 'completed', got %q", ws.EPStatus)
		}
		if ws.ExitCode == nil || *ws.ExitCode != 0 {
			t.Errorf("expected exit_code 0, got %v", ws.ExitCode)
		}
	}
}
