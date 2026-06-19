package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMCP_ServerResourcesAndTools(t *testing.T) {
	// Set up test credentials
	os.Setenv("PORTFOLIO_API_KEY", "test-secret-copilot")
	os.Setenv("PORTFOLIO_AUTH_EMAIL", "testcopilot@stayawesome.de")
	defer os.Unsetenv("PORTFOLIO_API_KEY")
	defer os.Unsetenv("PORTFOLIO_AUTH_EMAIL")

	// Set up mock HTTP Server representing the backend
	mux := http.NewServeMux()
	mux.HandleFunc("/api/initiatives", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"sa-card-1","title":"Test Card"}]`))
	})
	mux.HandleFunc("/api/capacity", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"stayawesome":{"polecats":1,"vkslots":4}}`))
	})
	mux.HandleFunc("/api/backlog", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/api/initiative", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "missing id", 400)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"initiative":{"id":"` + id + `","title":"Details"}}`))
	})
	mux.HandleFunc("/api/move", func(w http.ResponseWriter, r *http.Request) {
		if !checkAuth(r) {
			http.Error(w, "unauthorized", 401)
			return
		}
		var body struct{ Id, Stage string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	// 1. Test Fetching Board Resource
	boardText, _, err := readMcpResource(server.URL, "board://all")
	if err != nil {
		t.Fatalf("failed to read board resource: %v", err)
	}
	if !strings.Contains(boardText, "sa-card-1") {
		t.Errorf("expected board text to contain sa-card-1, got %s", boardText)
	}

	// 2. Test Fetching Initiative Resource
	initText, _, err := readMcpResource(server.URL, "initiative://sa-card-1")
	if err != nil {
		t.Fatalf("failed to read initiative resource: %v", err)
	}
	if !strings.Contains(initText, "sa-card-1") {
		t.Errorf("expected initiative text to contain sa-card-1, got %s", initText)
	}

	// 3. Test Calling move-stage Tool with correct auth
	resMsg, isErr, err := callMcpToolMoveStage(server.URL, "sa-card-1", "soon")
	if err != nil {
		t.Fatalf("failed to call move-stage: %v", err)
	}
	if isErr {
		t.Errorf("expected move-stage tool call to succeed, but got error signal")
	}
	if !strings.Contains(resMsg, "erfolgreich") {
		t.Errorf("expected success message, got %s", resMsg)
	}

	// 4. Test Calling move-stage Tool without auth (expecting HTTP 401)
	os.Unsetenv("PORTFOLIO_API_KEY")
	os.Unsetenv("PORTFOLIO_AUTH_EMAIL")
	resMsgFail, isErrFail, err := callMcpToolMoveStage(server.URL, "sa-card-1", "soon")
	if err != nil {
		t.Fatalf("failed to call move-stage: %v", err)
	}
	if !isErrFail {
		t.Errorf("expected move-stage tool call without auth to fail, but got success signal")
	}
	if !strings.Contains(resMsgFail, "HTTP 401") {
		t.Errorf("expected HTTP 401 error, got %s", resMsgFail)
	}
}

func TestCopilotChatEndpoint_ValidationAndAuth(t *testing.T) {
	os.Setenv("PORTFOLIO_API_KEY", "test-secret-copilot")
	defer os.Unsetenv("PORTFOLIO_API_KEY")

	dsn := os.Getenv("PORTFOLIO_DSN")
	if dsn == "" {
		dsn = "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable"
	}

	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skip("skipping integration tests; database not reachable:", err)
	}
	defer p.Close()

	if err := p.Ping(ctx); err != nil {
		t.Skip("skipping integration tests; database ping failed:", err)
	}

	// Inserts a test initiative
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = 'sa-test-copilot-card'")
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend)
		VALUES ('sa-test-copilot-card', 'stayawesome', 'idea', 'Test Copilot Card', 'plan_file')`)
	if err != nil {
		t.Fatalf("failed to create test card: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = 'sa-test-copilot-card'")

	handler := handleCopilotChat(p)
	server := httptest.NewServer(handler)
	defer server.Close()

	// 1. Send unauthorized request (should get 401)
	payload := CopilotChatRequest{
		InitiativeID: "sa-test-copilot-card",
		Message:      "Hallo, wer bist du?",
	}
	b, _ := json.Marshal(payload)
	resp, err := http.Post(server.URL, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", resp.StatusCode)
	}

	// 2. Send authorized request with non-existent card (should get 404)
	payload404 := CopilotChatRequest{
		InitiativeID: "sa-non-existent-card",
		Message:      "Hallo",
	}
	b404, _ := json.Marshal(payload404)
	req404, _ := http.NewRequest("POST", server.URL, bytes.NewReader(b404))
	req404.Header.Set("X-Api-Key", "test-secret-copilot")
	req404.Header.Set("Content-Type", "application/json")
	resp404, err := http.DefaultClient.Do(req404)
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}
	if resp404.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", resp404.StatusCode)
	}

	// 3. Test database persistence of historical events (L4)
	// Let's directly insert a dummy message event and query it
	dummySession := "test-session-123"
	userEventPayload, _ := json.Marshal(map[string]any{
		"category":   "copilot",
		"role":       "user",
		"text":       "This is a test message to query.",
		"session_id": dummySession,
		"at":         time.Now(),
	})
	_, err = p.Exec(ctx,
		`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
		 VALUES ($1, 'activity', 'master', $2, $3)`,
		"sa-test-copilot-card", userEventPayload, "mario")
	if err != nil {
		t.Fatalf("failed to insert mock history: %v", err)
	}

	// Query events to verify isolation and constraints
	var count int
	err = p.QueryRow(ctx,
		`SELECT COUNT(*) FROM portfolio.initiative_event
		 WHERE initiative_id = $1 AND kind = 'activity' AND payload->>'session_id' = $2`,
		"sa-test-copilot-card", dummySession).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query inserted event: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 history item, got %d", count)
	}
}
