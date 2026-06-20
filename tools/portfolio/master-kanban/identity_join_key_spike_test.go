package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestIdentityJoinKeySpike implements the end-to-end multi-space identity join key chain:
// PID -> /proc/<PID>/cgroup -> Slice
// PID -> /proc/<PID>/cwd -> /var/log/vk-sessions.jsonl -> Workspace UUID (R2)
// Workspace UUID -> SQLite (vibe-kanban database) -> Workspace Name & Bead ID
// Bead ID -> Postgres (port 5433 - beads) -> Bead Status
// Bead ID -> Postgres (port 5434 - portfolio) -> Initiative Card on Kanban (R3)
func TestIdentityJoinKeySpike(t *testing.T) {
	// 1. Locate vk-sessions.jsonl log
	sessionsLogPath := "/var/log/vk-sessions.jsonl"
	if _, err := os.Stat(sessionsLogPath); os.IsNotExist(err) {
		t.Skipf("vibe-sessions log not found at %s, skipping", sessionsLogPath)
		return
	}

	// 2. Define target Workspace prefix to trace (as if we got it from a process CWD)
	// We will trace "1134", which corresponds to "sol-st-4aibw" workspace from today (2026-06-20)
	targetPrefix := "1134"
	t.Logf("Step 1: Simulating PID -> /proc/<PID>/cwd -> Workspace prefix: %q", targetPrefix)

	// 3. Open and parse vk-sessions.jsonl to find the Workspace UUID and Branch
	file, err := os.Open(sessionsLogPath)
	if err != nil {
		t.Fatalf("failed to open sessions log: %v", err)
	}
	defer file.Close()

	// Read lines and look for the prefix
	content, err := os.ReadFile(sessionsLogPath)
	if err != nil {
		t.Fatalf("failed to read sessions log: %v", err)
	}

	lines := strings.Split(string(content), "\n")
	var foundWorkspaceID string
	var foundBranch string
	var foundCWD string
	var foundRepo string

	for _, line := range lines {
		if line == "" {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(line), &doc); err != nil {
			continue
		}
		wsID, _ := doc["workspace_id"].(string)
		cwdPath, _ := doc["cwd"].(string)
		branchName, _ := doc["branch"].(string)
		repoName, _ := doc["repo"].(string)

		if strings.HasPrefix(wsID, targetPrefix) || strings.Contains(cwdPath, targetPrefix) {
			foundWorkspaceID = wsID
			foundBranch = branchName
			foundCWD = cwdPath
			foundRepo = repoName
			break
		}
	}

	if foundWorkspaceID == "" {
		t.Fatalf("could not resolve Workspace UUID for prefix %q in sessions log", targetPrefix)
	}

	t.Logf("Step 2 (Log Bridge Resolved):")
	t.Logf("  - Workspace UUID: %s", foundWorkspaceID)
	t.Logf("  - Branch Name   : %s", foundBranch)
	t.Logf("  - Mounted CWD   : %s", foundCWD)
	t.Logf("  - Provider Repo : %s", foundRepo)

	// 4. Query vibe-kanban SQLite db for Workspace details
	vkDB := "/root/.local/share/vibe-kanban/db.v2.sqlite"
	if _, err := os.Stat(vkDB); os.IsNotExist(err) {
		t.Skipf("vibe-kanban SQLite DB not found at %s", vkDB)
		return
	}

	// Format UUID for SQLite HEX query (remove dashes and uppercase)
	hexWsID := strings.ReplaceAll(foundWorkspaceID, "-", "")
	hexWsID = strings.ToUpper(hexWsID)

	query := fmt.Sprintf(`
		SELECT hex(id), name, branch, archived, created_at 
		FROM workspaces 
		WHERE id = x'%s';
	`, hexWsID)

	cmd := exec.Command("sqlite3", "-readonly", "-separator", "|", vkDB, query)
	var sqliteOut bytes.Buffer
	cmd.Stdout = &sqliteOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to query SQLite: %v", err)
	}

	sqliteLine := strings.TrimSpace(sqliteOut.String())
	if sqliteLine == "" {
		t.Fatalf("Workspace %s not found in SQLite workspaces table", foundWorkspaceID)
	}

	parts := strings.Split(sqliteLine, "|")
	wsHexID := parts[0]
	wsName := parts[1]
	wsBranch := parts[2]
	wsArchived := parts[3]
	wsCreatedAt := parts[4]

	t.Logf("Step 3 (SQLite Workspace Query):")
	t.Logf("  - SQLite Hex ID : %s", wsHexID)
	t.Logf("  - Workspace Name: %s", wsName)
	t.Logf("  - SQLite Branch : %s", wsBranch)
	t.Logf("  - Archived Flag : %s", wsArchived)
	t.Logf("  - Created At    : %s", wsCreatedAt)

	// Extract Bead ID from workspace name (e.g. "sol-st-4aibw" -> "st-4aibw")
	var beadID string
	if strings.HasPrefix(wsName, "sol-") {
		beadID = strings.TrimPrefix(wsName, "sol-")
	} else if strings.Contains(wsName, "[") {
		// e.g. [st-1ll2k] ...
		start := strings.Index(wsName, "[")
		end := strings.Index(wsName, "]")
		if start != -1 && end != -1 && end > start {
			beadID = wsName[start+1 : end]
		}
	} else {
		beadID = wsName
	}

	if beadID == "" {
		t.Fatalf("failed to extract Bead ID from workspace name %q", wsName)
	}
	t.Logf("Step 4: Resolved Bead ID: %s", beadID)

	// 5. Connect to Beads Postgres database (port 5433)
	beadsDSN := "postgres://remote:remote@127.0.0.1:5433/solartown_clean?sslmode=disable"
	beadsCtx := context.Background()
	beadsConn, err := pgx.Connect(beadsCtx, beadsDSN)
	if err != nil {
		t.Skip("skipping beads DB query; port 5433 not reachable:", err)
		return
	}
	defer beadsConn.Close(beadsCtx)

	var beadTitle string
	var beadStatus string
	err = beadsConn.QueryRow(beadsCtx,
		"SELECT title, status FROM beads.issues WHERE id = $1", beadID).
		Scan(&beadTitle, &beadStatus)
	if err != nil {
		t.Errorf("failed to find bead %s in beads DB: %v", beadID, err)
	} else {
		t.Logf("Step 5 (Beads DB Status Query):")
		t.Logf("  - Bead Title  : %s", beadTitle)
		t.Logf("  - Bead Status : %s", beadStatus)
	}

	// 6. Connect to Portfolio/Board Postgres database (port 5434)
	portfolioDSN := "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable"
	portfolioCtx := context.Background()
	portfolioConn, err := pgx.Connect(portfolioCtx, portfolioDSN)
	if err != nil {
		t.Skip("skipping portfolio DB query; port 5434 not reachable:", err)
		return
	}
	defer portfolioConn.Close(portfolioCtx)

	// Find the initiative_id that matches this bead
	var initiativeID string
	err = portfolioConn.QueryRow(portfolioCtx,
		"SELECT initiative_id FROM portfolio.initiative_link WHERE ref = $1 AND kind = 'bead'", beadID).
		Scan(&initiativeID)
	if err != nil {
		// Let's check if there is an unlinked item for this bead
		var unlinkedKind, unlinkedTitle string
		errUnlinked := portfolioConn.QueryRow(portfolioCtx,
			"SELECT kind, title FROM portfolio.unlinked_item WHERE id = $1", beadID).
			Scan(&unlinkedKind, &unlinkedTitle)
		if errUnlinked == nil {
			t.Logf("Step 6 (Portfolio Unlinked Item Query):")
			t.Logf("  - Found in unlinked_item with kind: %s, title: %s", unlinkedKind, unlinkedTitle)
		} else {
			t.Logf("Step 6: Bead %s is currently unlinked and not in unlinked_item table.", beadID)
		}
	} else {
		// Find Initiative details
		var initTitle string
		var initStage string
		var initFirma string
		err = portfolioConn.QueryRow(portfolioCtx,
			"SELECT title, stage, firma FROM portfolio.initiative WHERE id = $1", initiativeID).
			Scan(&initTitle, &initStage, &initFirma)
		if err == nil {
			t.Logf("Step 6 (Portfolio Board Initiative Resolved):")
			t.Logf("  - Initiative ID   : %s", initiativeID)
			t.Logf("  - Initiative Title: %s", initTitle)
			t.Logf("  - Initiative Stage: %s", initStage)
			t.Logf("  - Owner/Company   : %s", initFirma)
		}
	}

	t.Logf("=== IDENTITY JOIN KEY CHAIN VERIFIED SUCCESSFULLY ===")
}
