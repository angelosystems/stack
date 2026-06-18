package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

// ── master-kanban-mcp ─────────────────────────────────────────────────────────
//
// Exponiert das Board als MCP-Server (stdio, JSON-RPC 2.0): die eine
// Aktionsfläche für alle Clients (Drawer-Chat, Sessions, Copilot-Orchestrator).
//   Resources (read): initiative/<id> (inkl. Plan-Baum, Beads/Links, Events),
//                     plan-file/<ref> (PRD-Text), board (alle Initiativen, verdichtet).
//   Tools (act):      move-stage — dünner Wrapper auf /api/move, auth-gegated
//                     (gleicher Pfad wie /api/dispatch).
// Bewährt sich das MCP-Muster nicht, kann der Client direkt gegen /api/* gehen —
// Rückbau = diese eine Wrapper-Schicht entfernen.

const (
	mcpProtocolVersion = "2024-11-05"
	kanbanURIScheme    = "kanban://"
)

var validStages = map[string]bool{
	"idea": true, "now": true, "soon": true, "watching": true, "done": true,
}

// ── JSON-RPC 2.0 Wire-Format ──────────────────────────────────────────────────

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// ── Auth ──────────────────────────────────────────────────────────────────────

// CallMeta trägt die Aufrufer-Identität (aus den MCP-Params `_meta`), damit
// mutierende Tools über denselben Pfad wie /api/dispatch gegated werden.
type CallMeta struct {
	Email  string `json:"email"`
	APIKey string `json:"apiKey"`
}

func (m CallMeta) authorized() bool { return authorized(m.Email, m.APIKey) }

func parseMeta(raw json.RawMessage) CallMeta {
	var m CallMeta
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

// ── Pluggable Backends (für Tests injizierbar) ────────────────────────────────

// BoardStore liefert die read-only Resource-Inhalte.
type BoardStore interface {
	Initiative(ctx context.Context, id string) (json.RawMessage, error)
	Board(ctx context.Context) (json.RawMessage, error)
	PlanFile(ctx context.Context, ref string) (string, error)
}

// StageMover ruft den mutierenden /api/move-Endpoint (die Wahrheit).
type StageMover interface {
	Move(ctx context.Context, id, stage, apiKey string) error
}

// ── MCPServer ─────────────────────────────────────────────────────────────────

type MCPServer struct {
	Store   BoardStore
	Mover   StageMover
	MoveKey string // X-Api-Key, das beim /api/move-Wrapper-Call durchgereicht wird
}

// Serve liest newline-delimited JSON-RPC von in und schreibt Antworten nach out
// (MCP-stdio-Transport).
func (s *MCPServer) Serve(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	enc := json.NewEncoder(out)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = enc.Encode(rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		if resp := s.handle(req); resp != nil {
			if err := enc.Encode(resp); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func (s *MCPServer) handle(req rpcRequest) *rpcResponse {
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"
	reply := func(result any, e *rpcError) *rpcResponse {
		if isNotification {
			return nil
		}
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: e}
	}

	switch req.Method {
	case "initialize":
		return reply(map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities": map[string]any{
				"resources": map[string]any{},
				"tools":     map[string]any{},
			},
			"serverInfo": map[string]any{"name": "master-kanban-mcp", "version": Version},
		}, nil)

	case "notifications/initialized", "notifications/cancelled":
		return nil

	case "ping":
		return reply(map[string]any{}, nil)

	case "resources/list":
		return reply(map[string]any{"resources": s.resourceList()}, nil)

	case "resources/templates/list":
		return reply(map[string]any{"resourceTemplates": s.resourceTemplates()}, nil)

	case "resources/read":
		var p struct {
			URI string `json:"uri"`
		}
		_ = json.Unmarshal(req.Params, &p)
		contents, err := s.readResource(context.Background(), p.URI)
		if err != nil {
			return reply(nil, &rpcError{Code: -32002, Message: err.Error()})
		}
		return reply(map[string]any{"contents": contents}, nil)

	case "tools/list":
		return reply(map[string]any{"tools": []map[string]any{toolToMCP(moveStageTool())}}, nil)

	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Meta      json.RawMessage `json:"_meta"`
		}
		_ = json.Unmarshal(req.Params, &p)
		out, err := s.ExecuteTool(context.Background(), p.Name, p.Arguments, parseMeta(p.Meta))
		if err != nil {
			return reply(map[string]any{
				"content": []map[string]any{{"type": "text", "text": err.Error()}},
				"isError": true,
			}, nil)
		}
		return reply(map[string]any{
			"content": []map[string]any{{"type": "text", "text": out}},
			"isError": false,
		}, nil)

	default:
		return reply(nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method})
	}
}

// ── Resources ─────────────────────────────────────────────────────────────────

func (s *MCPServer) resourceList() []map[string]any {
	return []map[string]any{
		{
			"uri":         kanbanURIScheme + "board",
			"name":        "board",
			"description": "Alle Initiativen (verdichtet): Stage, Counts, last_activity — für Triage.",
			"mimeType":    "application/json",
		},
	}
}

func (s *MCPServer) resourceTemplates() []map[string]any {
	return []map[string]any{
		{
			"uriTemplate": kanbanURIScheme + "initiative/{id}",
			"name":        "initiative",
			"description": "Karten-Kontext: Initiative + Plan-Baum + Beads/Links + Events.",
			"mimeType":    "application/json",
		},
		{
			"uriTemplate": kanbanURIScheme + "plan-file/{ref}",
			"name":        "plan-file",
			"description": "PRD-/Plan-Markdown eines plan_item (ref = plan_item-id).",
			"mimeType":    "text/markdown",
		},
	}
}

func (s *MCPServer) readResource(ctx context.Context, uri string) ([]map[string]any, error) {
	path := strings.TrimPrefix(uri, kanbanURIScheme)
	switch {
	case path == "board":
		j, err := s.Store.Board(ctx)
		if err != nil {
			return nil, err
		}
		return []map[string]any{{"uri": uri, "mimeType": "application/json", "text": string(j)}}, nil

	case strings.HasPrefix(path, "initiative/"):
		id := strings.TrimPrefix(path, "initiative/")
		j, err := s.Store.Initiative(ctx, id)
		if err != nil {
			return nil, err
		}
		return []map[string]any{{"uri": uri, "mimeType": "application/json", "text": string(j)}}, nil

	case strings.HasPrefix(path, "plan-file/"):
		ref := strings.TrimPrefix(path, "plan-file/")
		md, err := s.Store.PlanFile(ctx, ref)
		if err != nil {
			return nil, err
		}
		return []map[string]any{{"uri": uri, "mimeType": "text/markdown", "text": md}}, nil
	}
	return nil, fmt.Errorf("unbekannte Resource-URI: %s", uri)
}

// ── Tools ─────────────────────────────────────────────────────────────────────

func moveStageTool() LLMTool {
	return LLMTool{
		Name: "move-stage",
		Description: "Verschiebt eine Initiative (Karte) in eine andere Board-Stage. " +
			"Mutierend und auth-gegated (gleicher Pfad wie /api/dispatch). " +
			"stage ∈ idea|now|soon|watching|done.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":    map[string]any{"type": "string", "description": "Initiative-ID, z.B. st-573ds"},
				"stage": map[string]any{"type": "string", "enum": []string{"idea", "now", "soon", "watching", "done"}},
			},
			"required": []string{"id", "stage"},
		},
	}
}

func toolToMCP(t LLMTool) map[string]any {
	return map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.InputSchema}
}

// ExecuteTool implementiert ToolExecutor — derselbe Pfad, ob über tools/call
// (externer MCP-Client) oder über den Copilot-Orchestrator aufgerufen.
func (s *MCPServer) ExecuteTool(ctx context.Context, name string, input json.RawMessage, meta CallMeta) (string, error) {
	switch name {
	case "move-stage":
		if !meta.authorized() {
			return "", fmt.Errorf("unauthorized: move-stage erfordert gültige Auth (SSO-Email oder API-Key)")
		}
		var args struct {
			ID           string `json:"id"`
			InitiativeID string `json:"initiative_id"`
			Stage        string `json:"stage"`
		}
		if err := json.Unmarshal(input, &args); err != nil {
			return "", fmt.Errorf("ungültige Argumente: %w", err)
		}
		id := args.ID
		if id == "" {
			id = args.InitiativeID
		}
		if id == "" || args.Stage == "" {
			return "", fmt.Errorf("id und stage sind erforderlich")
		}
		if !validStages[args.Stage] {
			return "", fmt.Errorf("ungültige stage %q (idea|now|soon|watching|done)", args.Stage)
		}
		if err := s.Mover.Move(ctx, id, args.Stage, s.MoveKey); err != nil {
			return "", err
		}
		return fmt.Sprintf("Initiative %s nach Stage '%s' verschoben.", id, args.Stage), nil
	}
	return "", fmt.Errorf("unbekanntes Tool: %s", name)
}

// ── Produktive Backends ───────────────────────────────────────────────────────

type pgBoardStore struct{ pool *pgxpool.Pool }

func (s pgBoardStore) Initiative(ctx context.Context, id string) (json.RawMessage, error) {
	if id == "" {
		return nil, fmt.Errorf("initiative-id fehlt")
	}
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM portfolio.initiative_summary WHERE id=$1)`, id).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("initiative nicht gefunden: %s", id)
	}
	var j json.RawMessage
	err := s.pool.QueryRow(ctx, `SELECT json_build_object(
	  'initiative', (SELECT row_to_json(s) FROM portfolio.initiative_summary s WHERE s.id=$1),
	  'links', COALESCE((SELECT json_agg(row_to_json(l) ORDER BY l.kind, l.added_at)
	                     FROM portfolio.initiative_link l WHERE l.initiative_id=$1), '[]'::json),
	  'plan_items', COALESCE((SELECT json_agg(row_to_json(pi)) FROM (
	                     SELECT id, parent_id, slug, layer, status, title, repo, path
	                     FROM portfolio.plan_item WHERE initiative_id=$1
	                     ORDER BY parent_id NULLS FIRST, id) pi), '[]'::json),
	  'events', COALESCE((SELECT json_agg(row_to_json(e)) FROM (
	                     SELECT kind, source_backend, from_stage, to_stage, payload, actor, at
	                     FROM portfolio.initiative_event WHERE initiative_id=$1
	                     ORDER BY at DESC LIMIT 40) e), '[]'::json))`, id).Scan(&j)
	return j, err
}

func (s pgBoardStore) Board(ctx context.Context) (json.RawMessage, error) {
	// R-A: verdichtet (Top-50 nach Aktivität), nicht das volle Board.
	var j json.RawMessage
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(json_agg(row_to_json(t)), '[]'::json) FROM (
	  SELECT id, firma, stage, title, bead_count, vk_count, pr_count, plan_count, last_activity
	  FROM portfolio.initiative_summary
	  ORDER BY last_activity DESC NULLS LAST
	  LIMIT 50) t`).Scan(&j)
	return j, err
}

func (s pgBoardStore) PlanFile(ctx context.Context, ref string) (string, error) {
	it, err := planItem(s.pool, ref)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(it.Path)
	if err != nil {
		return "", err
	}
	if len(raw) > 300000 {
		raw = raw[:300000]
	}
	return string(raw), nil
}

// httpStageMover ist der dünne Wrapper auf den existierenden /api/move-Endpoint.
type httpStageMover struct{ baseURL string }

func (m httpStageMover) Move(ctx context.Context, id, stage, apiKey string) error {
	body, _ := json.Marshal(map[string]string{"id": id, "stage": stage})
	req, _ := http.NewRequestWithContext(ctx, "POST",
		strings.TrimRight(m.baseURL, "/")+"/api/move", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-Api-Key", apiKey)
	}
	cl := &http.Client{Timeout: 30 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("/api/move %d: %.200s", resp.StatusCode, b)
	}
	return nil
}

// ── CLI ───────────────────────────────────────────────────────────────────────

func cmdMCP() *cobra.Command {
	var baseURL string
	c := &cobra.Command{
		Use:   "mcp",
		Short: "master-kanban-mcp — Board als MCP über stdio (Resources + move-stage Tool)",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := connect()
			srv := &MCPServer{
				Store:   pgBoardStore{pool: p},
				Mover:   httpStageMover{baseURL: baseURL},
				MoveKey: envOr("PORTFOLIO_API_KEY", "dev-secret"),
			}
			fmt.Fprintln(os.Stderr, "master-kanban-mcp: stdio-Server bereit "+
				"(Resources: board, initiative/<id>, plan-file/<ref> | Tool: move-stage)")
			return srv.Serve(os.Stdin, os.Stdout)
		},
	}
	c.Flags().StringVar(&baseURL, "base-url", envOr("PORTFOLIO_BASE_URL", "http://127.0.0.1:7770"),
		"Basis-URL des serve-Backends für den /api/move-Wrapper")
	return c
}

// cmdCopilot fährt eine einmalige Orchestrator-Runde über eine Karte —
// macht die LLM↔Tool-Schleife end-to-end lauffähig (Demo/Smoke).
func cmdCopilot() *cobra.Command {
	var initiativeID, message, baseURL, email, apiKey string
	c := &cobra.Command{
		Use:   "copilot",
		Short: "Eine Orchestrator-Runde (LLM↔Tool-Schleife) über eine Karte",
		RunE: func(cmd *cobra.Command, args []string) error {
			if initiativeID == "" || message == "" {
				return fmt.Errorf("--initiative und --message sind erforderlich")
			}
			p := connect()
			store := pgBoardStore{pool: p}
			srv := &MCPServer{
				Store:   store,
				Mover:   httpStageMover{baseURL: baseURL},
				MoveKey: envOr("PORTFOLIO_API_KEY", "dev-secret"),
			}
			ctx := context.Background()
			ctxJSON, err := store.Initiative(ctx, initiativeID)
			if err != nil {
				return err
			}
			orch := &Orchestrator{
				LLM:      glmLLM{},
				Tools:    []LLMTool{moveStageTool()},
				Executor: srv,
				MaxSteps: 8,
			}
			history := []map[string]any{{"role": "user", "content": message}}
			res, err := orch.Run(ctx, buildCopilotSystem(string(ctxJSON)), history, CallMeta{Email: email, APIKey: apiKey})
			if err != nil {
				return err
			}
			for _, st := range res.Steps {
				fmt.Fprintf(os.Stderr, "[tool] %s(%s) -> %s%s\n", st.Tool, st.Input, st.Output, st.Err)
			}
			fmt.Println(res.Text)
			return nil
		},
	}
	c.Flags().StringVar(&initiativeID, "initiative", "", "Initiative-ID (Karte)")
	c.Flags().StringVar(&message, "message", "", "Nutzer-Nachricht an den Copilot")
	c.Flags().StringVar(&baseURL, "base-url", envOr("PORTFOLIO_BASE_URL", "http://127.0.0.1:7770"), "serve-Backend-URL")
	c.Flags().StringVar(&email, "email", "", "SSO-Email für Auth-gegated Tools")
	c.Flags().StringVar(&apiKey, "api-key", envOr("PORTFOLIO_API_KEY", ""), "API-Key für Auth-gegated Tools")
	return c
}
