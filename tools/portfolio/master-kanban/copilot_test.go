package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
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
