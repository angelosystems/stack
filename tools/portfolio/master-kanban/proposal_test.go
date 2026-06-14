package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFirmaPrefix(t *testing.T) {
	expected := map[string]string{
		"stayawesome": "sa",
		"quantbot":    "qb",
		"solartown":   "st",
		"mariobrain":  "mb",
		"angeloos":    "ag",
		"stack":       "sk",
	}

	for k, v := range expected {
		if val := firmaPrefix[k]; val != v {
			t.Errorf("Expected %q to be %q, got %q", k, v, val)
		}
	}
}

func TestApiDispatch(t *testing.T) {
	// 1. Connect to development database
	portfolioDsn := envOr("PORTFOLIO_DSN", "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable")

	ctx := context.Background()
	pPool, err := pgxpool.New(ctx, portfolioDsn)
	if err != nil {
		t.Fatalf("Failed to connect to portfolio DB: %v", err)
	}
	defer pPool.Close()

	// Swap global pool
	oldPool := pool
	pool = pPool
	defer func() {
		pool = oldPool
	}()

	testInitiativeID := "st-bead-native-reviewer"
	testWorkspaceID := "550e8400-e29b-41d4-a716-446655440000"

	// Cleanup test events
	cleanup := func() {
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'dispatched'", testInitiativeID)
	}
	cleanup()
	defer cleanup()

	// 2. Create mock vk-delegate script
	tmpDir, err := os.MkdirTemp("", "vk-mock")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	mockScriptPath := filepath.Join(tmpDir, "vk-delegate")
	scriptContent := fmt.Sprintf(`#!/bin/sh
echo "workspace_id:        %s"
echo "execution_process:   550e8400-e29b-41d4-a716-446655440001"
echo "workspace_url:       http://localhost:54682/workspaces/%s"
`, testWorkspaceID, testWorkspaceID)

	if err := os.WriteFile(mockScriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to write mock script: %v", err)
	}

	// Override the vk-delegate path used by the handler
	oldVkPath := vkDelegatePath
	vkDelegatePath = mockScriptPath
	defer func() {
		vkDelegatePath = oldVkPath
	}()

	// 3. Start master-kanban serve command in a background goroutine
	srvCmd := cmdServe()
	testPort := "17771"
	srvCmd.SetArgs([]string{"--port", testPort})
	go func() {
		_ = srvCmd.Execute()
	}()
	// Allow server to boot up
	time.Sleep(500 * time.Millisecond)

	// 4. Test POS /api/dispatch
	payload := map[string]string{
		"id":   testInitiativeID,
		"lane": "hack",
		"note": "Implement test features detached",
	}
	pBytes, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", "http://localhost:"+testPort+"/api/dispatch", bytes.NewReader(pBytes))
	req.Header.Set("Content-Type", "application/json")

	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("POST to dispatch endpoint failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected dispatch endpoint to return status 200, got %d", resp.StatusCode)
	}

	var respData struct {
		Ok          bool   `json:"ok"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&respData); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if !respData.Ok || respData.WorkspaceID != testWorkspaceID {
		t.Errorf("Unexpected response: %+v", respData)
	}

	// 5. Verify the dispatch event was written to the portfolio.initiative_event table
	var dbRef string
	err = pPool.QueryRow(ctx,
		`SELECT payload->>'ref' FROM portfolio.initiative_event 
		 WHERE initiative_id = $1 AND kind = 'dispatched' AND payload->>'lane' = 'hack'`,
		testInitiativeID).Scan(&dbRef)
	if err != nil {
		t.Fatalf("Failed to find dispatched event in database: %v", err)
	}

	if dbRef != testWorkspaceID {
		t.Errorf("Expected event ref to be %q, got %q", testWorkspaceID, dbRef)
	}
}
