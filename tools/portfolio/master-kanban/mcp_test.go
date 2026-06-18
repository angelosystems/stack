package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// ── Fakes ─────────────────────────────────────────────────────────────────────

type fakeStore struct {
	initiatives map[string]json.RawMessage
	board       json.RawMessage
	plans       map[string]string
}

func (f fakeStore) Initiative(_ context.Context, id string) (json.RawMessage, error) {
	v, ok := f.initiatives[id]
	if !ok {
		return nil, fmt.Errorf("initiative nicht gefunden: %s", id)
	}
	return v, nil
}
func (f fakeStore) Board(_ context.Context) (json.RawMessage, error) { return f.board, nil }
func (f fakeStore) PlanFile(_ context.Context, ref string) (string, error) {
	v, ok := f.plans[ref]
	if !ok {
		return "", fmt.Errorf("kein plan-file: %s", ref)
	}
	return v, nil
}

type fakeMover struct {
	moved []string
	key   string
	err   error
}

func (m *fakeMover) Move(_ context.Context, id, stage, apiKey string) error {
	if m.err != nil {
		return m.err
	}
	m.moved = append(m.moved, id+":"+stage)
	m.key = apiKey
	return nil
}

func newTestServer() (*MCPServer, *fakeMover) {
	mover := &fakeMover{}
	srv := &MCPServer{
		Store: fakeStore{
			initiatives: map[string]json.RawMessage{
				"st-573ds": json.RawMessage(`{"initiative":{"id":"st-573ds","stage":"idea","title":"MCP-Copilot"},"links":[],"plan_items":[],"events":[]}`),
			},
			board: json.RawMessage(`[{"id":"st-573ds","stage":"idea"}]`),
			plans: map[string]string{"st-573ds-prd": "# PRD\nrealer Text"},
		},
		Mover:   mover,
		MoveKey: "server-key",
	}
	return srv, mover
}

// roundTrip schickt eine Request durch handle und liefert das Result-JSON.
func roundTrip(t *testing.T, srv *MCPServer, method string, params any) (map[string]any, *rpcError) {
	t.Helper()
	var raw json.RawMessage
	if params != nil {
		b, _ := json.Marshal(params)
		raw = b
	}
	resp := srv.handle(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: method, Params: raw})
	if resp == nil {
		t.Fatalf("%s: unerwartet keine Antwort (Notification?)", method)
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	b, _ := json.Marshal(resp.Result)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("result kein objekt: %v", err)
	}
	return m, nil
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestMCPInitialize(t *testing.T) {
	srv, _ := newTestServer()
	res, rpcErr := roundTrip(t, srv, "initialize", nil)
	if rpcErr != nil {
		t.Fatalf("initialize fehlgeschlagen: %v", rpcErr)
	}
	if res["protocolVersion"] != mcpProtocolVersion {
		t.Errorf("protocolVersion = %v, want %s", res["protocolVersion"], mcpProtocolVersion)
	}
	info := res["serverInfo"].(map[string]any)
	if info["name"] != "master-kanban-mcp" {
		t.Errorf("serverInfo.name = %v", info["name"])
	}
}

func TestResourceReadInitiative(t *testing.T) {
	srv, _ := newTestServer()
	res, rpcErr := roundTrip(t, srv, "resources/read", map[string]any{"uri": "kanban://initiative/st-573ds"})
	if rpcErr != nil {
		t.Fatalf("resources/read fehlgeschlagen: %v", rpcErr)
	}
	contents := res["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("erwarte 1 content, got %d", len(contents))
	}
	c := contents[0].(map[string]any)
	if c["mimeType"] != "application/json" {
		t.Errorf("mimeType = %v", c["mimeType"])
	}
	text := c["text"].(string)
	if !strings.Contains(text, "st-573ds") || !strings.Contains(text, "MCP-Copilot") {
		t.Errorf("initiative-Resource ohne realen Kontext: %s", text)
	}
}

func TestResourceReadNotFound(t *testing.T) {
	srv, _ := newTestServer()
	_, rpcErr := roundTrip(t, srv, "resources/read", map[string]any{"uri": "kanban://initiative/does-not-exist"})
	if rpcErr == nil {
		t.Fatal("erwarte rpcError für unbekannte Initiative")
	}
}

func TestToolsListContainsMoveStage(t *testing.T) {
	srv, _ := newTestServer()
	res, _ := roundTrip(t, srv, "tools/list", nil)
	tools := res["tools"].([]any)
	found := false
	for _, ti := range tools {
		if ti.(map[string]any)["name"] == "move-stage" {
			found = true
			if _, ok := ti.(map[string]any)["inputSchema"]; !ok {
				t.Error("move-stage ohne inputSchema")
			}
		}
	}
	if !found {
		t.Error("move-stage nicht in tools/list")
	}
}

func TestMoveStageUnauthorizedRejected(t *testing.T) {
	srv, mover := newTestServer()
	// tools/call ohne _meta → keine Auth
	res, rpcErr := roundTrip(t, srv, "tools/call", map[string]any{
		"name":      "move-stage",
		"arguments": map[string]any{"id": "st-573ds", "stage": "soon"},
	})
	if rpcErr != nil {
		t.Fatalf("tools/call sollte In-Band-Error liefern, kein rpcError: %v", rpcErr)
	}
	if res["isError"] != true {
		t.Errorf("erwarte isError=true bei fehlender Auth, got %v", res["isError"])
	}
	if len(mover.moved) != 0 {
		t.Errorf("/api/move darf bei Auth-Fehler NICHT aufgerufen werden, got %v", mover.moved)
	}
}

func TestMoveStageAuthorizedCallsMove(t *testing.T) {
	os.Setenv("PORTFOLIO_API_KEY", "test-secret-key")
	defer os.Unsetenv("PORTFOLIO_API_KEY")

	srv, mover := newTestServer()
	res, rpcErr := roundTrip(t, srv, "tools/call", map[string]any{
		"name":      "move-stage",
		"arguments": map[string]any{"id": "st-573ds", "stage": "soon"},
		"_meta":     map[string]any{"apiKey": "test-secret-key"},
	})
	if rpcErr != nil {
		t.Fatalf("rpcError: %v", rpcErr)
	}
	if res["isError"] == true {
		t.Fatalf("erwarte Erfolg, got isError: %v", res["content"])
	}
	if len(mover.moved) != 1 || mover.moved[0] != "st-573ds:soon" {
		t.Errorf("/api/move falsch aufgerufen: %v", mover.moved)
	}
	if mover.key != "server-key" {
		t.Errorf("MoveKey nicht durchgereicht: %q", mover.key)
	}
}

func TestMoveStageAuthorizedViaSSO(t *testing.T) {
	srv, mover := newTestServer()
	res, _ := roundTrip(t, srv, "tools/call", map[string]any{
		"name":      "move-stage",
		"arguments": map[string]any{"id": "st-573ds", "stage": "now"},
		"_meta":     map[string]any{"email": "mario@stayawesome.de"},
	})
	if res["isError"] == true {
		t.Fatalf("SSO-Auth sollte durchgehen: %v", res["content"])
	}
	if len(mover.moved) != 1 {
		t.Errorf("move nicht ausgeführt: %v", mover.moved)
	}
}

func TestMoveStageInvalidStage(t *testing.T) {
	srv, mover := newTestServer()
	res, _ := roundTrip(t, srv, "tools/call", map[string]any{
		"name":      "move-stage",
		"arguments": map[string]any{"id": "st-573ds", "stage": "bogus"},
		"_meta":     map[string]any{"email": "mario@stayawesome.de"},
	})
	if res["isError"] != true {
		t.Error("ungültige stage sollte isError sein")
	}
	if len(mover.moved) != 0 {
		t.Error("move bei ungültiger stage nicht ausführen")
	}
}

func TestExecuteToolUnauthorizedDirect(t *testing.T) {
	srv, _ := newTestServer()
	_, err := srv.ExecuteTool(context.Background(), "move-stage",
		json.RawMessage(`{"id":"st-573ds","stage":"soon"}`), CallMeta{})
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("erwarte unauthorized-Fehler, got %v", err)
	}
}

func TestServeStdioRoundTrip(t *testing.T) {
	srv, _ := newTestServer()
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
			`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"kanban://board"}}` + "\n")
	var out bytes.Buffer
	if err := srv.Serve(in, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	// Nur 2 Antworten: initialize + resources/read; die Notification erzeugt keine.
	if len(lines) != 2 {
		t.Fatalf("erwarte 2 Antworten, got %d: %q", len(lines), out.String())
	}
	var second rpcResponse
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("Antwort 2 kein JSON-RPC: %v", err)
	}
	if string(second.ID) != "2" {
		t.Errorf("Antwort-ID = %s, want 2", second.ID)
	}
}

func TestMethodNotFound(t *testing.T) {
	srv, _ := newTestServer()
	_, rpcErr := roundTrip(t, srv, "does/not/exist", nil)
	if rpcErr == nil || rpcErr.Code != -32601 {
		t.Errorf("erwarte -32601 method not found, got %v", rpcErr)
	}
}
