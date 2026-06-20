package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestCheckFirmaProposalsAndEndpoints(t *testing.T) {
	// 1. Connect to development databases
	portfolioDsn := envOr("PORTFOLIO_DSN", "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable")
	solartownDsn := envOr("SOLARTOWN_DSN", "postgres://remote:remote@127.0.0.1:5433/solartown_clean?sslmode=disable")

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

	// Cleanup any leftover test data
	cleanup := func() {
		_, _ = pPool.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id IN ($1, $2)", "proposal-"+testBeadID, testBeadID)
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
	srvCmd := cmdServe()
	testPort := "17770"
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
}
