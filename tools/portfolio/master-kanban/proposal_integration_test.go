//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCheckFirmaProposalsAndEndpoints(t *testing.T) {
	// 1. Connect to development databases
	portfolioDsn := mkIntegrationDSN(t)
	solartownDsn := mkSecondaryDSN(t, "SOLARTOWN_DSN")

	ctx := context.Background()
	pPool, err := pgxpool.New(ctx, portfolioDsn)
	if err != nil {
		t.Fatalf("Failed to connect to portfolio DB: %v", err)
	}
	defer pPool.Close()

	sPool, err := pgxpool.New(ctx, solartownDsn)
	if err != nil {
		t.Fatalf("Failed to connect to solartown DB: %v", err)
	}
	defer sPool.Close()

	// Store and swap the global pool variables so main package functions use them
	oldStPool := stPool
	stPool = sPool
	oldPool := pool
	pool = pPool
	defer func() {
		stPool = oldStPool
		pool = oldPool
	}()

	testBeadID := "st-wisp-0hoz"

	// Ensure st-catch-all exists in portfolio database
	_, _ = pPool.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title) VALUES ('st-catch-all', 'solartown', 'watching', 'Solartown Catch-all') ON CONFLICT (id) DO NOTHING`)

	// Cleanup any leftover test data
	cleanup := func() {
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id IN ($1, $2)", testBeadID, "st-catch-all")
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id IN ($1, $2)", "proposal-"+testBeadID, testBeadID)
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id LIKE 'proposal-%'")
		_, _ = sPool.Exec(ctx, "UPDATE beads.labels SET deleted_at=now() WHERE issue_id=$1 AND label='lane:plan'", testBeadID)
	}
	cleanup()
	defer cleanup()

	// 2. Fetch original st-ib5e (Detox) status so we can restore it exactly
	var origStatus string
	var origClosedAt *time.Time
	err = sPool.QueryRow(ctx, "SELECT status, closed_at FROM beads.issues WHERE id='st-ib5e' AND deleted_at IS NULL").Scan(&origStatus, &origClosedAt)
	if err != nil {
		t.Fatalf("Could not fetch original st-ib5e: %v", err)
	}

	// Restore original st-ib5e status at end of test
	defer func() {
		_, err := sPool.Exec(ctx, "UPDATE beads.issues SET status=$1, closed_at=$2 WHERE id='st-ib5e'", origStatus, origClosedAt)
		if err != nil {
			t.Errorf("Failed to restore st-ib5e status: %v", err)
		}
	}()

	// 3. Test: with Detox open, checkFirmaProposals should do nothing
	_, err = sPool.Exec(ctx, "UPDATE beads.issues SET status='hooked', closed_at=NULL WHERE id='st-ib5e'")
	if err != nil {
		t.Fatalf("Failed to open st-ib5e status: %v", err)
	}

	checkFirmaProposals(pPool, "solartown")

	// Verify no proposals were created
	var count int
	err = pPool.QueryRow(ctx, "SELECT count(*) FROM portfolio.initiative WHERE id LIKE 'proposal-%'").Scan(&count)
	if err != nil {
		t.Fatalf("Query proposals count failed: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected 0 proposals when detox is open, got %d", count)
	}

	// 4. Test: with Detox closed, checkFirmaProposals should generate proposals
	// Build mock JSON structure
	innerJSON := fmt.Sprintf(`[{"bead_id": %q, "title": "Vorschlag: mol-polecat-work", "reasoning": "Highly feasible and important for stack monitoring."}]`, testBeadID)
	type TextContent struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type MockResponse struct {
		Content []TextContent `json:"content"`
	}
	mockResponse := MockResponse{
		Content: []TextContent{
			{
				Type: "text",
				Text: innerJSON,
			},
		},
	}
	mockResponseBytes, _ := json.Marshal(mockResponse)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(mockResponseBytes)
	}))
	defer server.Close()

	// Override callGlm config
	os.Setenv("ZAI_KEY", "test-key-123")
	os.Setenv("REVIEWER_BASE_URL", server.URL)
	defer func() {
		os.Unsetenv("ZAI_KEY")
		os.Unsetenv("REVIEWER_BASE_URL")
	}()

	// Set Detox bead as closed in the past so the 2026-05-01 testBead is younger than Detox
	detoxTime := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	_, err = sPool.Exec(ctx, "UPDATE beads.issues SET status='closed', closed_at=$1 WHERE id='st-ib5e'", detoxTime)
	if err != nil {
		t.Fatalf("Failed to close st-ib5e status: %v", err)
	}

	// Run proposal check
	_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative WHERE stage='soon' AND id NOT LIKE 'proposal-%'")
	checkFirmaProposals(pPool, "solartown")

	// Verify proposal card was created in the database
	var propTitle string
	var propStatusDot string
	err = pPool.QueryRow(ctx, "SELECT title, status_dot FROM portfolio.initiative WHERE id = $1", "proposal-"+testBeadID).Scan(&propTitle, &propStatusDot)
	if err != nil {
		t.Fatalf("Proposal not found in DB: %v", err)
	}

	if propTitle != "Vorschlag: mol-polecat-work" {
		t.Errorf("Expected proposal title 'Vorschlag: mol-polecat-work', got %q", propTitle)
	}

	var statusDotData struct {
		Proposed  bool   `json:"proposed"`
		BeadID    string `json:"bead_id"`
		Reasoning string `json:"reasoning"`
	}
	if err := json.Unmarshal([]byte(propStatusDot), &statusDotData); err != nil {
		t.Fatalf("Failed to parse status_dot of proposal: %v", err)
	}
	if !statusDotData.Proposed || statusDotData.BeadID != testBeadID || statusDotData.Reasoning == "" {
		t.Errorf("Invalid status_dot content: %+v", statusDotData)
	}

	// 5. Start our master-kanban serve command in a background goroutine to test accept/reject endpoints
	http.DefaultServeMux = http.NewServeMux()
	srvCmd := cmdServe()
	testPort, err := getFreePort()
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	srvCmd.SetArgs([]string{"--port", testPort})
	go func() {
		_ = srvCmd.Execute()
	}()
	// Allow server to boot up
	time.Sleep(500 * time.Millisecond)

	// A. Test Accept Endpoint
	acceptPayload := map[string]string{"id": "proposal-" + testBeadID}
	pBytes, _ := json.Marshal(acceptPayload)
	req, _ := http.NewRequest("POST", "http://localhost:"+testPort+"/api/proposal/accept", bytes.NewReader(pBytes))
	req.Header.Set("Content-Type", "application/json")

	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("POST to accept endpoint failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected accept endpoint to return status 200, got %d", resp.StatusCode)
	}

	// Verify proposal card is deleted
	var propExists bool
	err = pPool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM portfolio.initiative WHERE id = $1)", "proposal-"+testBeadID).Scan(&propExists)
	if err != nil {
		t.Fatalf("Failed to check if proposal card still exists: %v", err)
	}
	if propExists {
		t.Errorf("Expected proposal card to be deleted after accept")
	}

	// Verify real initiative card is created
	var cardTitle string
	var cardStage string
	err = pPool.QueryRow(ctx, "SELECT title, stage FROM portfolio.initiative WHERE id = $1", testBeadID).Scan(&cardTitle, &cardStage)
	if err != nil {
		t.Fatalf("Real initiative card not found in DB: %v", err)
	}
	if cardTitle != "Vorschlag: mol-polecat-work" || cardStage != "idea" {
		t.Errorf("Unexpected real initiative card values: title=%q, stage=%q", cardTitle, cardStage)
	}

	// Verify 'created' event was written on accept
	var createdEventCount int
	err = pPool.QueryRow(ctx, "SELECT COUNT(*) FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'created'", testBeadID).Scan(&createdEventCount)
	if err != nil {
		t.Fatalf("Failed to query created event: %v", err)
	}
	if createdEventCount != 1 {
		t.Errorf("Expected 1 created event for accepted proposal, got %d", createdEventCount)
	}

	// Verify lane:plan label is set on the bead in beads database
	var labelExists bool
	err = sPool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM beads.labels WHERE issue_id = $1 AND label = 'lane:plan' AND deleted_at IS NULL)", testBeadID).Scan(&labelExists)
	if err != nil {
		t.Fatalf("Failed to check if lane:plan label is set: %v", err)
	}
	if !labelExists {
		t.Errorf("Expected lane:plan label to be set on the bead %s", testBeadID)
	}

	// B. Test Reject Endpoint
	// First let's re-generate the proposal card
	checkFirmaProposals(pPool, "solartown")

	// Verify proposal is back
	err = pPool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM portfolio.initiative WHERE id = $1)", "proposal-"+testBeadID).Scan(&propExists)
	if err != nil || !propExists {
		t.Fatalf("Failed to re-generate proposal card for rejection test")
	}

	rejectPayload := map[string]string{"id": "proposal-" + testBeadID}
	rBytes, _ := json.Marshal(rejectPayload)
	req, _ = http.NewRequest("POST", "http://localhost:"+testPort+"/api/proposal/reject", bytes.NewReader(rBytes))
	req.Header.Set("Content-Type", "application/json")

	resp, err = cl.Do(req)
	if err != nil {
		t.Fatalf("POST to reject endpoint failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected reject endpoint to return status 200, got %d", resp.StatusCode)
	}

	// Verify proposal card is deleted spurlos
	err = pPool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM portfolio.initiative WHERE id = $1)", "proposal-"+testBeadID).Scan(&propExists)
	if err != nil {
		t.Fatalf("Failed to check if proposal card exists after reject: %v", err)
	}
	if propExists {
		t.Errorf("Expected proposal card to be deleted after reject")
	}

	// Verify rejection activity event was logged on st-catch-all
	var rejectEventCount int
	err = pPool.QueryRow(ctx, "SELECT COUNT(*) FROM portfolio.initiative_event WHERE initiative_id = 'st-catch-all' AND kind = 'activity' AND payload->>'action' = 'reject'").Scan(&rejectEventCount)
	if err != nil {
		t.Fatalf("Failed to query reject event on catch-all: %v", err)
	}
	if rejectEventCount != 1 {
		t.Errorf("Expected 1 reject event on st-catch-all, got %d", rejectEventCount)
	}

	// C. Test /api/move Endpoint
	movePayload := map[string]string{"id": testBeadID, "stage": "soon"}
	mBytes, _ := json.Marshal(movePayload)
	req, _ = http.NewRequest("POST", "http://localhost:"+testPort+"/api/move", bytes.NewReader(mBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Request-Email", "mario@solartown.de")

	resp, err = cl.Do(req)
	if err != nil {
		t.Fatalf("POST to move endpoint failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected move endpoint to return status 200, got %d", resp.StatusCode)
	}

	// Verify stage is updated
	var currentStage string
	err = pPool.QueryRow(ctx, "SELECT stage FROM portfolio.initiative WHERE id = $1", testBeadID).Scan(&currentStage)
	if err != nil {
		t.Fatalf("Failed to query stage: %v", err)
	}
	if currentStage != "soon" {
		t.Errorf("Expected stage to be 'soon', got %q", currentStage)
	}

	// Verify move event was logged
	var moveEventCount int
	var fromStage, toStage string
	err = pPool.QueryRow(ctx, "SELECT COUNT(*), from_stage, to_stage FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'moved' AND actor = 'mario@solartown.de' GROUP BY from_stage, to_stage", testBeadID).Scan(&moveEventCount, &fromStage, &toStage)
	if err != nil {
		t.Fatalf("Failed to query move event: %v", err)
	}
	if moveEventCount != 1 {
		t.Errorf("Expected 1 move event with actor 'mario@solartown.de', got %d", moveEventCount)
	}
	if fromStage != "idea" || toStage != "soon" {
		t.Errorf("Expected stage transition from 'idea' to 'soon', got from %q to %q", fromStage, toStage)
	}
}

func getFreePort() (string, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return "", err
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return "", err
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port), nil
}

func TestApiDispatch(t *testing.T) {
	// 1. Connect to development database
	portfolioDsn := mkIntegrationDSN(t)

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
	http.DefaultServeMux = http.NewServeMux()
	srvCmd := cmdServe()
	testPort, err := getFreePort()
	if err != nil {
		t.Fatalf("Failed to get free port: %v", err)
	}
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
