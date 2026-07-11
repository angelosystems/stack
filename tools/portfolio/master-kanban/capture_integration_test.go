//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCaptureCommand(t *testing.T) {
	dsn := mkIntegrationDSN(t)

	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skip("skipping integration test; db not reachable:", err)
	}
	defer p.Close()

	if err := p.Ping(ctx); err != nil {
		t.Skip("skipping integration test; db ping failed:", err)
	}

	// 1. Setup clean test initiatives
	testInitID := "st-test-capture-specific"
	testCatchAllID := "st-catch-all"

	// Ensure any old test events are removed
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id IN ($1, $2)", testInitID, testCatchAllID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)

	// Insert test initiative
	_, err = p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend)
		VALUES ($1, 'solartown', 'idea', 'Test Capture Initiative', 'plan_file')`, testInitID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}
	defer func() {
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)
	}()

	// Ensure st-catch-all exists (usually seeded, but let's insert if missing)
	var catchAllExists bool
	_ = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM portfolio.initiative WHERE id = $1)`, testCatchAllID).Scan(&catchAllExists)
	if !catchAllExists {
		_, err = p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend)
			VALUES ($1, 'solartown', 'watching', 'Ad-hoc / Sonstiges', 'master')`, testCatchAllID)
		if err != nil {
			t.Fatalf("failed to insert st-catch-all: %v", err)
		}
		defer func() {
			_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testCatchAllID)
			_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testCatchAllID)
		}()
	}

	// 2. Test Specific Matching via slug or full ID in text
	cmd := cmdCapture()
	// Assign DSN flag to mock pool connection
	pool = p

	// Case A: matches by full ID
	textA := "Quick fix in st-test-capture-specific for reactor issues (08b1119)"
	cmd.SetArgs([]string{textA})
	err = cmd.Execute()
	if err != nil {
		t.Fatalf("Capture command failed: %v", err)
	}

	// Verify event was logged under testInitID
	var countA int
	err = p.QueryRow(ctx, `SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'activity' AND payload->>'title' = $2`, testInitID, textA).Scan(&countA)
	if err != nil {
		t.Fatalf("failed to query logged event: %v", err)
	}
	if countA != 1 {
		t.Errorf("expected 1 logged event, got %d", countA)
	}

	// Case B: matches by slug only
	textB := "Refactored test-capture-specific helper functionality"
	cmd.SetArgs([]string{textB})
	err = cmd.Execute()
	if err != nil {
		t.Fatalf("Capture command failed: %v", err)
	}

	// Verify event was logged under testInitID
	var countB int
	err = p.QueryRow(ctx, `SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'activity' AND payload->>'title' = $2`, testInitID, textB).Scan(&countB)
	if err != nil {
		t.Fatalf("failed to query logged event: %v", err)
	}
	if countB != 1 {
		t.Errorf("expected 1 logged event, got %d", countB)
	}

	// Case C: Idempotence (running identical event should not duplicate)
	cmd.SetArgs([]string{textB})
	err = cmd.Execute()
	if err != nil {
		t.Fatalf("Capture command second run failed: %v", err)
	}

	// Verify event count is still 1 (did not duplicate)
	err = p.QueryRow(ctx, `SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'activity' AND payload->>'title' = $2`, testInitID, textB).Scan(&countB)
	if err != nil {
		t.Fatalf("failed to query logged event: %v", err)
	}
	if countB != 1 {
		t.Errorf("idempotence failed: expected event count to be 1, got %d", countB)
	}

	// Case D: Fallback to Catch-all Initiative
	textD := "An unscoped quick fix that doesn't reference any initiative"
	cmd = cmdCapture()
	cmd.SetArgs([]string{"--firma", "solartown", textD})
	err = cmd.Execute()
	if err != nil {
		t.Fatalf("Capture command failed for catch-all fallback: %v", err)
	}

	// Verify event was logged under st-catch-all
	var countD int
	err = p.QueryRow(ctx, `SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'activity' AND payload->>'title' = $2`, testCatchAllID, textD).Scan(&countD)
	if err != nil {
		t.Fatalf("failed to query logged event on catch-all: %v", err)
	}
	if countD != 1 {
		t.Errorf("expected 1 logged event on catch-all initiative, got %d", countD)
	}
}

func TestCaptureAPIAndMCP(t *testing.T) {
	dsn := mkIntegrationDSN(t)

	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skip("skipping integration test; db not reachable:", err)
	}
	defer p.Close()

	if err := p.Ping(ctx); err != nil {
		t.Skip("skipping integration test; db ping failed:", err)
	}

	testInitID := "st-test-capture-specific"
	testCatchAllID := "st-catch-all"

	// Ensure any old test events are removed
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id IN ($1, $2)", testInitID, testCatchAllID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)

	// Insert test initiative
	_, err = p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend)
		VALUES ($1, 'solartown', 'idea', 'Test Capture Initiative', 'plan_file')`, testInitID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}
	defer func() {
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)
	}()

	// Ensure st-catch-all exists (usually seeded, but let's insert if missing)
	var catchAllExists bool
	_ = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM portfolio.initiative WHERE id = $1)`, testCatchAllID).Scan(&catchAllExists)
	if !catchAllExists {
		_, err = p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend)
			VALUES ($1, 'solartown', 'watching', 'Ad-hoc / Sonstiges', 'master')`, testCatchAllID)
		if err != nil {
			t.Fatalf("failed to insert st-catch-all: %v", err)
		}
		defer func() {
			_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testCatchAllID)
			_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testCatchAllID)
		}()
	}

	// Create test handler for /api/capture
	mux := http.NewServeMux()
	mux.HandleFunc("/api/capture", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Auth-Request-Email, X-Api-Key")
		if r.Method == "OPTIONS" {
			return
		}
		var body struct {
			Text  string `json:"text"`
			Firma string `json:"firma"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if body.Text == "" {
			http.Error(w, "text ist erforderlich", 400)
			return
		}

		matchedID, skipped, err := captureEvent(r.Context(), p, body.Text, body.Firma, "mcp-test")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":         true,
			"matched_id": matchedID,
			"skipped":    skipped,
		})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	// 1. Test MCP Client Call: matches specific initiative
	textA := "Quick fix in st-test-capture-specific for API and MCP tests (abcdef)"
	resText, isErr, err := callMcpToolCapture(server.URL, textA, "solartown")
	if err != nil {
		t.Fatalf("callMcpToolCapture failed: %v", err)
	}
	if isErr {
		t.Fatalf("callMcpToolCapture returned error: %s", resText)
	}
	if !strings.Contains(resText, "Event erfolgreich erfasst für Initiative: st-test-capture-specific") {
		t.Errorf("unexpected success message: %s", resText)
	}

	// Verify event was logged under testInitID
	var countA int
	err = p.QueryRow(ctx, `SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'activity' AND payload->>'title' = $2`, testInitID, textA).Scan(&countA)
	if err != nil {
		t.Fatalf("failed to query logged event: %v", err)
	}
	if countA != 1 {
		t.Errorf("expected 1 logged event, got %d", countA)
	}

	// 2. Test MCP Client Call Idempotency: call again with identical text
	resText, isErr, err = callMcpToolCapture(server.URL, textA, "solartown")
	if err != nil {
		t.Fatalf("callMcpToolCapture failed: %v", err)
	}
	if isErr {
		t.Fatalf("callMcpToolCapture returned error: %s", resText)
	}
	if !strings.Contains(resText, "Event bereits vorhanden (idempotent übersprungen) für Initiative: st-test-capture-specific") {
		t.Errorf("unexpected idempotence message: %s", resText)
	}

	// Verify count remains 1
	err = p.QueryRow(ctx, `SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'activity' AND payload->>'title' = $2`, testInitID, textA).Scan(&countA)
	if err != nil {
		t.Fatalf("failed to query logged event: %v", err)
	}
	if countA != 1 {
		t.Errorf("idempotency check failed, event was duplicated: count is %d", countA)
	}

	// 3. Test MCP Client Call Catch-all
	textB := "An completely unrelated event without any slug"
	resText, isErr, err = callMcpToolCapture(server.URL, textB, "solartown")
	if err != nil {
		t.Fatalf("callMcpToolCapture failed on catch-all: %v", err)
	}
	if isErr {
		t.Fatalf("callMcpToolCapture returned error on catch-all: %s", resText)
	}
	if !strings.Contains(resText, "Event erfolgreich erfasst für Initiative: st-catch-all") {
		t.Errorf("unexpected catch-all success message: %s", resText)
	}

	// Verify event was logged under st-catch-all
	var countB int
	err = p.QueryRow(ctx, `SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'activity' AND payload->>'title' = $2`, testCatchAllID, textB).Scan(&countB)
	if err != nil {
		t.Fatalf("failed to query logged event on catch-all: %v", err)
	}
	if countB != 1 {
		t.Errorf("expected 1 logged event on catch-all, got %d", countB)
	}
}
