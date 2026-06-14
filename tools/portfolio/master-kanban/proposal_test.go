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

	// Create a temp directory for plan-based repository mock
	tmpRepoDir, err := os.MkdirTemp("", "solartown-mock-repo")
	if err != nil {
		t.Fatalf("Failed to create temp repo dir: %v", err)
	}
	defer os.RemoveAll(tmpRepoDir)

	// Route "solartown" company to our temp directory for target resolution
	os.Setenv("PLANFILE_REPOS", tmpRepoDir+"=solartown")
	defer os.Unsetenv("PLANFILE_REPOS")

	// Cleanup test events & links
	cleanup := func() {
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'dispatched'", testInitiativeID)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1 AND kind = 'plan_file'", testInitiativeID)
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

	// 4. Test POST /api/dispatch (lane: hack)
	payloadHack := map[string]string{
		"id":   testInitiativeID,
		"lane": "hack",
		"note": "Implement test features detached",
	}
	pHackBytes, _ := json.Marshal(payloadHack)
	reqHack, _ := http.NewRequest("POST", "http://localhost:"+testPort+"/api/dispatch", bytes.NewReader(pHackBytes))
	reqHack.Header.Set("Content-Type", "application/json")

	cl := &http.Client{Timeout: 5 * time.Second}
	respHack, err := cl.Do(reqHack)
	if err != nil {
		t.Fatalf("POST to dispatch hack endpoint failed: %v", err)
	}
	defer respHack.Body.Close()

	if respHack.StatusCode != http.StatusOK {
		t.Errorf("Expected dispatch hack endpoint to return status 200, got %d", respHack.StatusCode)
	}

	var respHackData struct {
		Ok          bool   `json:"ok"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.NewDecoder(respHack.Body).Decode(&respHackData); err != nil {
		t.Fatalf("Failed to decode hack response: %v", err)
	}

	if !respHackData.Ok || respHackData.WorkspaceID != testWorkspaceID {
		t.Errorf("Unexpected hack response: %+v", respHackData)
	}

	// Verify the hack dispatch event was written to the portfolio.initiative_event table
	var dbHackRef string
	err = pPool.QueryRow(ctx,
		`SELECT payload->>'ref' FROM portfolio.initiative_event 
		 WHERE initiative_id = $1 AND kind = 'dispatched' AND payload->>'lane' = 'hack'`,
		testInitiativeID).Scan(&dbHackRef)
	if err != nil {
		t.Fatalf("Failed to find hack dispatched event in database: %v", err)
	}

	if dbHackRef != testWorkspaceID {
		t.Errorf("Expected event ref to be %q, got %q", testWorkspaceID, dbHackRef)
	}

	// 5. Test POST /api/dispatch (lane: plan-tech, First Dispatch)
	payloadPlan1 := map[string]string{
		"id":   testInitiativeID,
		"lane": "plan-tech",
		"note": "First plan dispatch",
	}
	pPlanBytes1, _ := json.Marshal(payloadPlan1)
	reqPlan1, _ := http.NewRequest("POST", "http://localhost:"+testPort+"/api/dispatch", bytes.NewReader(pPlanBytes1))
	reqPlan1.Header.Set("Content-Type", "application/json")

	respPlan1, err := cl.Do(reqPlan1)
	if err != nil {
		t.Fatalf("First plan dispatch failed: %v", err)
	}
	defer respPlan1.Body.Close()

	if respPlan1.StatusCode != http.StatusOK {
		t.Errorf("Expected first plan dispatch status 200, got %d", respPlan1.StatusCode)
	}

	var respPlanData1 struct {
		Ok     bool   `json:"ok"`
		Path   string `json:"path"`
		Reused bool   `json:"reused"`
	}
	if err := json.NewDecoder(respPlan1.Body).Decode(&respPlanData1); err != nil {
		t.Fatalf("Failed to decode plan response 1: %v", err)
	}

	if !respPlanData1.Ok || respPlanData1.Reused {
		t.Errorf("First plan dispatch: expected ok=true, reused=false, got: %+v", respPlanData1)
	}

	// Verify file was created on disk in our temp repo
	if _, err := os.Stat(respPlanData1.Path); err != nil {
		t.Errorf("Expected PRD file to exist at %s, but got error: %v", respPlanData1.Path, err)
	}

	// 6. Test POST /api/dispatch (lane: plan-tech, Second Dispatch on SAME card)
	payloadPlan2 := map[string]string{
		"id":   testInitiativeID,
		"lane": "plan-tech",
		"note": "Second plan dispatch",
	}
	pPlanBytes2, _ := json.Marshal(payloadPlan2)
	reqPlan2, _ := http.NewRequest("POST", "http://localhost:"+testPort+"/api/dispatch", bytes.NewReader(pPlanBytes2))
	reqPlan2.Header.Set("Content-Type", "application/json")

	respPlan2, err := cl.Do(reqPlan2)
	if err != nil {
		t.Fatalf("Second plan dispatch failed: %v", err)
	}
	defer respPlan2.Body.Close()

	if respPlan2.StatusCode != http.StatusOK {
		t.Errorf("Expected second plan dispatch status 200, got %d", respPlan2.StatusCode)
	}

	var respPlanData2 struct {
		Ok     bool   `json:"ok"`
		Path   string `json:"path"`
		Reused bool   `json:"reused"`
	}
	if err := json.NewDecoder(respPlan2.Body).Decode(&respPlanData2); err != nil {
		t.Fatalf("Failed to decode plan response 2: %v", err)
	}

	if !respPlanData2.Ok || !respPlanData2.Reused {
		t.Errorf("Second plan dispatch: expected ok=true, reused=true, got: %+v", respPlanData2)
	}

	if respPlanData2.Path != respPlanData1.Path {
		t.Errorf("Expected paths to match, got %s and %s", respPlanData1.Path, respPlanData2.Path)
	}

	// Verify we only have 1 plan_file link in the database for this initiative
	var planLinkCount int
	err = pPool.QueryRow(ctx,
		`SELECT count(*) FROM portfolio.initiative_link 
		 WHERE initiative_id = $1 AND kind = 'plan_file'`,
		testInitiativeID).Scan(&planLinkCount)
	if err != nil {
		t.Fatalf("Failed to query database plan links: %v", err)
	}
	if planLinkCount != 1 {
		t.Errorf("Expected exactly 1 plan_file link in database, got %d", planLinkCount)
	}
}
