package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNormalizeFirma(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"stayawesome", "stayawesome"},
		{"sa", "stayawesome"},
		{"Solartown", "solartown"},
		{"st", "solartown"},
		{"stack", "stack"},
		{"sk", "stack"},
		{"unknown", "unknown"},
	}

	for _, tc := range tests {
		actual := normalizeFirma(tc.input)
		if actual != tc.expected {
			t.Errorf("normalizeFirma(%q) = %q; expected %q", tc.input, actual, tc.expected)
		}
	}
}

func TestGuessFirmaFromCWD(t *testing.T) {
	// We are currently in stack/polecats/flint/stack...
	// So guessFirmaFromCWD should return "stack"
	guessed := guessFirmaFromCWD()
	if guessed != "stack" {
		t.Errorf("expected guessed firma to be 'stack', got %q", guessed)
	}
}

func TestCaptureCommand(t *testing.T) {
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

func TestCaptureMcpTool(t *testing.T) {
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

	// 1. Check tools/list contains capture tool
	reqList := McpRequest{
		JsonRPC: "2.0",
		Method:  "tools/list",
		ID:      1,
	}
	resList := dispatchMcpRequest(ctx, p, reqList, nil)
	if resList.Error != nil {
		t.Fatalf("Mcp tools/list failed: %v", resList.Error)
	}

	resultMap, ok := resList.Result.(map[string]any)
	if !ok {
		t.Fatalf("Mcp tools/list result is not map")
	}
	toolsListSlice, ok := resultMap["tools"].([]map[string]any)
	if !ok {
		// Try generic slice
		genericSlice, ok := resultMap["tools"].([]any)
		if !ok {
			t.Fatalf("Mcp tools/list tools field is missing or invalid")
		}
		// Convert
		toolsListSlice = make([]map[string]any, len(genericSlice))
		for i, v := range genericSlice {
			toolsListSlice[i] = v.(map[string]any)
		}
	}

	hasCapture := false
	for _, tool := range toolsListSlice {
		if tool["name"] == "capture" {
			hasCapture = true
			break
		}
	}
	if !hasCapture {
		t.Errorf("expected 'capture' tool in tools/list, got: %v", toolsListSlice)
	}

	// 2. Check tools/call auth rejection
	reqCall := McpRequest{
		JsonRPC: "2.0",
		Method:  "tools/call",
		ID:      2,
		Params:  json.RawMessage(`{"name":"capture","arguments":{"text":"Testing MCP capture"}}`),
	}
	resCallUnauth := dispatchMcpRequest(ctx, p, reqCall, nil) // no http.Request = unauthorized
	if resCallUnauth.Error == nil {
		t.Errorf("expected unauthorized error on nil http.Request")
	} else {
		errMap := resCallUnauth.Error.(map[string]any)
		if errMap["code"] != 401 {
			t.Errorf("expected unauthorized code 401, got: %v", errMap["code"])
		}
	}

	// Create authorized fake request
	httpReq, _ := http.NewRequest("POST", "/api/mcp", nil)
	httpReq.Header.Set("X-Auth-Request-Email", "testuser@stayawesome.de")

	// 3. Test successful tools/call
	testInitID := "st-test-capture-specific"
	testCatchAllID := "st-catch-all"

	// Ensure clean slate
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id IN ($1, $2)", testInitID, testCatchAllID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)

	_, err = p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend)
		VALUES ($1, 'solartown', 'idea', 'Test Capture Initiative', 'plan_file')`, testInitID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}
	defer func() {
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)
	}()

	textMcp := "MCP capture in st-test-capture-specific works great"
	reqCall.Params = json.RawMessage(`{"name":"capture","arguments":{"text":"` + textMcp + `"}}`)
	resCallSuccess := dispatchMcpRequest(ctx, p, reqCall, httpReq)
	if resCallSuccess.Error != nil {
		t.Fatalf("dispatchMcpRequest failed: %v", resCallSuccess.Error)
	}

	// Verify event was logged under testInitID
	var count int
	err = p.QueryRow(ctx, `SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'activity' AND payload->>'title' = $2`, testInitID, textMcp).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query logged event: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 logged event, got %d", count)
	}

	// 4. Test idempotence in tools/call (second call does not duplicate)
	resCallDup := dispatchMcpRequest(ctx, p, reqCall, httpReq)
	if resCallDup.Error != nil {
		t.Fatalf("dispatchMcpRequest second call failed: %v", resCallDup.Error)
	}

	err = p.QueryRow(ctx, `SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id = $1 AND kind = 'activity' AND payload->>'title' = $2`, testInitID, textMcp).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query logged event: %v", err)
	}
	if count != 1 {
		t.Errorf("idempotence failed: expected 1 event, got %d", count)
	}
}
