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
			"last_run":            lastRun,
			"status":              status,
			"error_message":       errMsg,
			"dangling_count":      len(dangling),
			"dangling_baseline":   4,
			"outage_simulated":    sageOutageSimulated,
			"dangling_workspaces": dangling,
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
	if _, ok := initialData["dangling_count"]; !ok {
		t.Errorf("expected 'dangling_count' to be present in status response")
	}
	if baseline, ok := initialData["dangling_baseline"]; !ok || baseline != float64(4) {
		t.Errorf("expected 'dangling_baseline' to be 4, got %v", baseline)
	}
	if _, ok := initialData["dangling_workspaces"]; !ok {
		t.Errorf("expected 'dangling_workspaces' to be present in status response")
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

func TestSageStopAndEscalate(t *testing.T) {
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

	// 1. Self-healing/Ensure schema migrations for Sage are applied in the test DB
	_, _ = p.Exec(ctx, "ALTER TABLE portfolio.initiative ADD COLUMN IF NOT EXISTS heal_count integer NOT NULL DEFAULT 0")
	_, _ = p.Exec(ctx, "ALTER TABLE portfolio.initiative_event DROP CONSTRAINT IF EXISTS initiative_event_kind_check")
	_, _ = p.Exec(ctx, `ALTER TABLE portfolio.initiative_event ADD CONSTRAINT initiative_event_kind_check
		CHECK (kind = ANY (ARRAY[
			'created', 'moved', 'edited', 'linked', 'unlinked', 'activity',
			'stage_proposed', 'completed', 'commented', 'archived', 'dispatched',
			'deployed', 'workspace_started', 'ai_message', 'ai_action', 'sage_action'
		]))`)
	_, _ = p.Exec(ctx, `CREATE OR REPLACE VIEW portfolio.sage_escalation_view AS
		SELECT DISTINCT ON (initiative_id)
			id,
			initiative_id,
			kind,
			source_backend,
			payload,
			actor,
			at
		FROM portfolio.initiative_event
		WHERE kind = 'sage_action' AND (payload->>'action') = 'escalate'
		ORDER BY initiative_id, at DESC`)

	// 2. Setup clean test initiatives
	testBeadID := "sa-test-sage-bead"
	testLiveGeldID := "qb-test-sage-livegeld-bead"

	// Clean any old test records
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id IN ($1, $2)", testBeadID, testLiveGeldID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id IN ($1, $2)", testBeadID, testLiveGeldID)

	// Insert test initiative (regular)
	_, err = p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend, heal_count)
		VALUES ($1, 'stayawesome', 'idea', 'Test Regular Bead', 'plan_file', 0)`, testBeadID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}

	// Insert test initiative (live geld / quantbot)
	_, err = p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend, heal_count)
		VALUES ($1, 'quantbot', 'idea', 'Test Live Geld Bead', 'plan_file', 0)`, testLiveGeldID)
	if err != nil {
		t.Fatalf("failed to insert test live-geld initiative: %v", err)
	}

	defer func() {
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id IN ($1, $2)", testBeadID, testLiveGeldID)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id IN ($1, $2)", testBeadID, testLiveGeldID)
	}()

	engine := NewSageDecisionEngine(p, 2) // Default N = 2

	// --- TEST 1: Regular Bead (N=2 retry budget) ---
	// First Failure (Retry 1/2) -> Should heal
	res1, err := engine.ProcessFailure(ctx, testBeadID)
	if err != nil {
		t.Fatalf("ProcessFailure 1 failed: %v", err)
	}
	if res1 != "healed" {
		t.Errorf("expected res1 to be 'healed', got %q", res1)
	}

	var healCount1 int
	_ = p.QueryRow(ctx, "SELECT heal_count FROM portfolio.initiative WHERE id = $1", testBeadID).Scan(&healCount1)
	if healCount1 != 1 {
		t.Errorf("expected heal_count after first failure to be 1, got %d", healCount1)
	}

	// Second Failure (Retry 2/2) -> Should heal
	res2, err := engine.ProcessFailure(ctx, testBeadID)
	if err != nil {
		t.Fatalf("ProcessFailure 2 failed: %v", err)
	}
	if res2 != "healed" {
		t.Errorf("expected res2 to be 'healed', got %q", res2)
	}

	var healCount2 int
	_ = p.QueryRow(ctx, "SELECT heal_count FROM portfolio.initiative WHERE id = $1", testBeadID).Scan(&healCount2)
	if healCount2 != 2 {
		t.Errorf("expected heal_count after second failure to be 2, got %d", healCount2)
	}

	// Third Failure (Retry 3/2 -> Budget Exhausted!) -> Should stop and escalate (SC3)
	res3, err := engine.ProcessFailure(ctx, testBeadID)
	if err != nil {
		t.Fatalf("ProcessFailure 3 failed: %v", err)
	}
	if res3 != "escalated (budget-exhausted)" {
		t.Errorf("expected res3 to be 'escalated (budget-exhausted)', got %q", res3)
	}

	var healCount3 int
	_ = p.QueryRow(ctx, "SELECT heal_count FROM portfolio.initiative WHERE id = $1", testBeadID).Scan(&healCount3)
	if healCount3 != 2 {
		t.Errorf("expected heal_count after third failure to remain 2, got %d", healCount3)
	}

	// Verify events logged
	var eventsCount int
	err = p.QueryRow(ctx, "SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'sage_action'", testBeadID).Scan(&eventsCount)
	if err != nil {
		t.Fatalf("failed to query events count: %v", err)
	}
	if eventsCount != 3 {
		t.Errorf("expected 3 events logged, got %d", eventsCount)
	}

	// Verify latest event was escalation
	var payloadStr string
	err = p.QueryRow(ctx, "SELECT payload::text FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'sage_action' ORDER BY at DESC LIMIT 1", testBeadID).Scan(&payloadStr)
	if err != nil {
		t.Fatalf("failed to query latest event payload: %v", err)
	}

	var action SageAction
	if err := json.Unmarshal([]byte(payloadStr), &action); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if action.Action != "escalate" {
		t.Errorf("expected action to be 'escalate', got %q", action.Action)
	}
	if action.HealCount != 2 {
		t.Errorf("expected action heal count to be 2, got %d", action.HealCount)
	}

	// --- TEST 2: Live Geld Bead (quantbot) ---
	// First Failure -> Should immediately escalate with Live-Geld-Konvention exception
	resLG, err := engine.ProcessFailure(ctx, testLiveGeldID)
	if err != nil {
		t.Fatalf("ProcessFailure for Live-Geld failed: %v", err)
	}
	if resLG != "escalated (live-geld)" {
		t.Errorf("expected live-geld result to be 'escalated (live-geld)', got %q", resLG)
	}

	var healCountLG int
	_ = p.QueryRow(ctx, "SELECT heal_count FROM portfolio.initiative WHERE id = $1", testLiveGeldID).Scan(&healCountLG)
	if healCountLG != 0 {
		t.Errorf("expected live-geld heal_count to remain 0, got %d", healCountLG)
	}

	// Verify latest event is escalation
	var payloadLGStr string
	err = p.QueryRow(ctx, "SELECT payload::text FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'sage_action' ORDER BY at DESC LIMIT 1", testLiveGeldID).Scan(&payloadLGStr)
	if err != nil {
		t.Fatalf("failed to query live-geld event: %v", err)
	}

	var actionLG SageAction
	_ = json.Unmarshal([]byte(payloadLGStr), &actionLG)
	if actionLG.Action != "escalate" || !actionLG.IsLiveGeld {
		t.Errorf("expected action 'escalate' and is_live_geld=true, got %q, %v", actionLG.Action, actionLG.IsLiveGeld)
	}

	// --- TEST 3: Deduplication in sage_escalation_view (R-C) ---
	// Let's log a second escalation for the regular bead to verify deduplication (one per bead)
	err = engine.Escalate(ctx, testBeadID, "Simuliertes Folgeproblem", 2, false)
	if err != nil {
		t.Fatalf("failed to escalate regular bead again: %v", err)
	}

	// Query sage_escalation_view and count entries
	rows, err := p.Query(ctx, "SELECT id, initiative_id FROM portfolio.sage_escalation_view WHERE initiative_id IN ($1, $2)", testBeadID, testLiveGeldID)
	if err != nil {
		t.Fatalf("failed to query sage_escalation_view: %v", err)
	}
	defer rows.Close()

	viewCount := 0
	for rows.Next() {
		var id int64
		var initID string
		if rows.Scan(&id, &initID) == nil {
			viewCount++
		}
	}

	// We expect exactly 2 rows in the view, one for testBeadID and one for testLiveGeldID,
	// because duplicate escalations are grouped/deduplicated per bead (DISTINCT ON initiative_id).
	if viewCount != 2 {
		t.Errorf("expected exactly 2 deduplicated escalation entries in the view, got %d", viewCount)
	}
}

func TestSageSteward_Sweep(t *testing.T) {
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

	// Setup mock initiative and link for testing the sweep
	testBeadID := "st-ib5e"
	testInitiativeID := "init-sage-test-sweep"

	// Clean up any old test data
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitiativeID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitiativeID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	// Clean up any existing sage_action events for the target workspace ID so the exists check doesn't skip logging
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE kind = 'sage_action' AND (payload->>'workspace_id') = 'B842765043A04994B61AACF51E019956'")

	// Save existing links for st-ib5e to restore them later
	rows, err := p.Query(ctx, "SELECT initiative_id, kind FROM portfolio.initiative_link WHERE ref = $1", testBeadID)
	type savedLink struct {
		initID string
		kind   string
	}
	var saved []savedLink
	if err == nil {
		for rows.Next() {
			var sl savedLink
			if rows.Scan(&sl.initID, &sl.kind) == nil {
				saved = append(saved, sl)
			}
		}
		rows.Close()
	}
	// Delete other links temporarily
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE ref = $1", testBeadID)

	defer func() {
		// Restore saved links
		for _, sl := range saved {
			_, _ = p.Exec(ctx, "INSERT INTO portfolio.initiative_link (initiative_id, kind, ref) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING", sl.initID, sl.kind, testBeadID)
		}
	}()

	// Create test initiative
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, title, stage, stage_locked_by_human, firma, primary_backend)
		VALUES ($1, 'Sage Test Initiative', 'idea', false, 'stayawesome', 'plan_file')
	`, testInitiativeID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitiativeID)
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitiativeID)
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	// Create test link to the bead
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		VALUES ($1, 'bead', $2)
	`, testInitiativeID, testBeadID)
	if err != nil {
		t.Fatalf("failed to insert test initiative link: %v", err)
	}

	// Trigger runSageSweepEx with onlyStuck = false
	runSageSweepEx(ctx, p, false)

	// Verify that the sage_action event was logged!
	var exists bool
	err = p.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM portfolio.initiative_event 
			WHERE initiative_id = $1 AND kind = 'sage_action'
		)
	`, testInitiativeID).Scan(&exists)
	if err != nil {
		t.Fatalf("failed to query logged event: %v", err)
	}

	if !exists {
		t.Errorf("expected sage_action event to be logged for initiative %s after runSageSweep, but it was not", testInitiativeID)
	}
}

func TestSageSteward_Sweep_OnlyStuck(t *testing.T) {
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

	testBeadID := "st-ib5e"
	testInitiativeID := "init-sage-test-sweep-stuck"

	// Clean up any old test data
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_lease WHERE bead_id = $1", testBeadID)
	defer p.Exec(ctx, "DELETE FROM portfolio.sage_lease WHERE bead_id = $1", testBeadID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitiativeID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitiativeID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	// Clean up any existing sage_action events for the target workspace ID so the exists check doesn't skip logging
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE kind = 'sage_action' AND (payload->>'workspace_id') = 'B842765043A04994B61AACF51E019956'")

	// Save existing links for st-ib5e to restore them later
	rows, err := p.Query(ctx, "SELECT initiative_id, kind FROM portfolio.initiative_link WHERE ref = $1", testBeadID)
	type savedLink struct {
		initID string
		kind   string
	}
	var saved []savedLink
	if err == nil {
		for rows.Next() {
			var sl savedLink
			if rows.Scan(&sl.initID, &sl.kind) == nil {
				saved = append(saved, sl)
			}
		}
		rows.Close()
	}
	// Delete other links temporarily
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE ref = $1", testBeadID)

	defer func() {
		// Restore saved links
		for _, sl := range saved {
			_, _ = p.Exec(ctx, "INSERT INTO portfolio.initiative_link (initiative_id, kind, ref) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING", sl.initID, sl.kind, testBeadID)
		}
	}()

	// Create test initiative
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, title, stage, stage_locked_by_human, firma, primary_backend)
		VALUES ($1, 'Sage Test Initiative Stuck', 'idea', false, 'stayawesome', 'plan_file')
	`, testInitiativeID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitiativeID)
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitiativeID)
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	// Create test link to the bead
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		VALUES ($1, 'bead', $2)
	`, testInitiativeID, testBeadID)
	if err != nil {
		t.Fatalf("failed to insert test initiative link: %v", err)
	}

	// Trigger runSageSweep with onlyStuck = true.
	// Since st-ib5e is failed (not running-and-stuck), no event should be logged.
	_ = runSageSweep(p, false, true)

	var exists bool
	err = p.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM portfolio.initiative_event 
			WHERE initiative_id = $1 AND kind = 'sage_action'
		)
	`, testInitiativeID).Scan(&exists)
	if err != nil {
		t.Fatalf("failed to query logged event: %v", err)
	}

	if exists {
		t.Errorf("expected NO sage_action event to be logged for initiative %s after runSageSweepEx(onlyStuck=true) on a failed workspace, but one was logged", testInitiativeID)
	}

	// Trigger runSageSweep with onlyStuck = false.
	// This should log the event since it processes everything.
	_ = runSageSweep(p, false, false)

	err = p.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM portfolio.initiative_event 
			WHERE initiative_id = $1 AND kind = 'sage_action'
		)
	`, testInitiativeID).Scan(&exists)
	if err != nil {
		t.Fatalf("failed to query logged event: %v", err)
	}

	if !exists {
		t.Errorf("expected sage_action event to be logged for initiative %s after runSageSweepEx(onlyStuck=false), but it was not", testInitiativeID)
	}
}

func TestInitiativeChecks(t *testing.T) {
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

	// 1. Setup mock initiative and link for testing the all-beads-closed promotion proposal
	testBeadID := "st-ib5e" // st-ib5e is already closed in beads DB!
	testInitiativeID := "init-test-all-beads-closed"

	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitiativeID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitiativeID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, title, stage, stage_locked_by_human, firma, primary_backend)
		VALUES ($1, 'Test All Beads Closed Initiative', 'now', false, 'stayawesome', 'plan_file')
	`, testInitiativeID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitiativeID)
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitiativeID)
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	// Create test link to the bead
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		VALUES ($1, 'bead', $2)
	`, testInitiativeID, testBeadID)
	if err != nil {
		t.Fatalf("failed to insert test initiative link: %v", err)
	}

	// 2. Setup mock initiative for backlog-faeule (rot) check
	rotInitiativeID := "init-test-backlog-rot"
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", rotInitiativeID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", rotInitiativeID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", rotInitiativeID)

	// Create backlog-rot initiative with updated_at/created_at set to 15 days ago
	oldTime := time.Now().Add(-15 * 24 * time.Hour)
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, title, stage, stage_locked_by_human, firma, primary_backend, created_at, updated_at)
		VALUES ($1, 'Test Backlog Rot Initiative', 'idea', false, 'stayawesome', 'plan_file', $2, $2)
	`, rotInitiativeID, oldTime)
	if err != nil {
		t.Fatalf("failed to insert test backlog rot initiative: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", rotInitiativeID)
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", rotInitiativeID)
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", rotInitiativeID)

	// 3. Trigger runInitiativeChecks
	runInitiativeChecks(ctx, p, false)

	// Verify all-beads-closed proposed stage-promotion event was logged!
	var exists bool
	err = p.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM portfolio.initiative_event 
			WHERE initiative_id = $1 AND kind = 'sage_action' AND payload->>'classification' = 'all-beads-closed'
		)
	`, testInitiativeID).Scan(&exists)
	if err != nil {
		t.Fatalf("failed to query all-beads-closed event: %v", err)
	}
	if !exists {
		t.Errorf("expected sage_action (classification=all-beads-closed) event to be logged for initiative %s, but it was not", testInitiativeID)
	}

	// Verify backlog-faeule proposed archive event was logged!
	var rotExists bool
	err = p.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM portfolio.initiative_event 
			WHERE initiative_id = $1 AND kind = 'sage_action' AND payload->>'classification' = 'backlog-faeule'
		)
	`, rotInitiativeID).Scan(&rotExists)
	if err != nil {
		t.Fatalf("failed to query backlog-faeule event: %v", err)
	}
	if !rotExists {
		t.Errorf("expected sage_action (classification=backlog-faeule) event to be logged for initiative %s, but it was not", rotInitiativeID)
	}

	// Verify backlog-faeule commented event was logged!
	var commentExists bool
	err = p.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM portfolio.initiative_event 
			WHERE initiative_id = $1 AND kind = 'commented' AND payload->>'title' LIKE '%Backlog-Fäule%'
		)
	`, rotInitiativeID).Scan(&commentExists)
	if err != nil {
		t.Fatalf("failed to query backlog-faeule comment: %v", err)
	}
	if !commentExists {
		t.Errorf("expected commented event to be logged for initiative %s, but it was not", rotInitiativeID)
	}

	// 4. Cooldown Check
	// A second run of runInitiativeChecks should NOT create duplicate events due to the cooldown mechanism
	runInitiativeChecks(ctx, p, false)

	var allBeadsClosedEventCount int
	err = p.QueryRow(ctx, `
		SELECT count(*) FROM portfolio.initiative_event 
		WHERE initiative_id = $1 AND kind = 'sage_action' AND payload->>'classification' = 'all-beads-closed'
	`, testInitiativeID).Scan(&allBeadsClosedEventCount)
	if err != nil {
		t.Fatalf("failed to query all-beads-closed event count: %v", err)
	}
	if allBeadsClosedEventCount != 1 {
		t.Errorf("expected exactly 1 sage_action event due to cooldown, got %d", allBeadsClosedEventCount)
	}

	var backlogRotEventCount int
	err = p.QueryRow(ctx, `
		SELECT count(*) FROM portfolio.initiative_event 
		WHERE initiative_id = $1 AND kind = 'sage_action' AND payload->>'classification' = 'backlog-faeule'
	`, rotInitiativeID).Scan(&backlogRotEventCount)
	if err != nil {
		t.Fatalf("failed to query backlog-faeule event count: %v", err)
	}
	if backlogRotEventCount != 1 {
		t.Errorf("expected exactly 1 backlog-faeule sage_action event due to cooldown, got %d", backlogRotEventCount)
	}
}

func TestSageSteward_Handover(t *testing.T) {
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

	testInitiativeID := "init-sage-test-handover"
	testWorkspaceID := "99999999999999999999999999999999"

	// Clean up old test data
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitiativeID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	// Create test initiative
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, title, stage, stage_locked_by_human, firma, primary_backend)
		VALUES ($1, 'Sage Handover Test Initiative', 'idea', false, 'stayawesome', 'plan_file')
	`, testInitiativeID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	// Create test server mux and register Handover handler
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sage/handover", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			InitiativeID string `json:"initiative_id"`
			WorkspaceID  string `json:"workspace_id"`
			Reason       string `json:"reason"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}

		if body.InitiativeID == "" || body.WorkspaceID == "" {
			http.Error(w, "missing initiative_id or workspace_id", 400)
			return
		}

		payloadMap := map[string]any{
			"workspace_id":    body.WorkspaceID,
			"action":          "handover",
			"reason":          body.Reason,
			"source":          "manager",
		}
		payloadBytes, _ := json.Marshal(payloadMap)

		_, err = p.Exec(r.Context(), `
			INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
			VALUES ($1, 'sage_action', 'sage', $2, 'flow-manager')
		`, body.InitiativeID, string(payloadBytes))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		select {
		case sageSweepChan <- struct{}{}:
		default:
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"handover_status":"received"}`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	// Perform POST request to the handover API
	postBody, _ := json.Marshal(map[string]string{
		"initiative_id": testInitiativeID,
		"workspace_id":  testWorkspaceID,
		"reason":        "Simulierte Workspace-Stagnation im NOW-Stage",
	})
	resp, err := http.Post(server.URL+"/api/sage/handover", "application/json", bytes.NewReader(postBody))
	if err != nil {
		t.Fatalf("failed to send POST handover request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	// Verify that the handover event was logged in the database
	var count int
	err = p.QueryRow(ctx, `
		SELECT count(*) FROM portfolio.initiative_event 
		WHERE initiative_id = $1 AND kind = 'sage_action' 
		  AND payload->>'action' = 'handover' 
		  AND payload->>'workspace_id' = $2
	`, testInitiativeID, testWorkspaceID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query logged handover event: %v", err)
	}

	if count != 1 {
		t.Errorf("expected exactly 1 handover event to be logged, got %d", count)
	}
}
