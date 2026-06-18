package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ── Copilot-Orchestrator ──────────────────────────────────────────────────────
//
// Die agentische Orchestrierungs-Komponente (Deep-Tech Must-Fix #1 des MCP-
// Copilot-PRD): sie fährt die LLM↔Tool-Schleife — LLM-Call → Tool-Call ausführen
// → Tool-Ergebnis zurückgeben → erneut fragen — bis das Modell ohne weiteren
// Tool-Aufruf antwortet. Der Browser kann das nicht (keine Keys client-seitig);
// diese Komponente lebt im serve-Backend und nutzt die MCP-Tool-Fläche als
// Executor. Damit ist das LLM ein Actor, der über die Board-Endpoints handelt.

// LLMTool beschreibt ein Tool für das Modell (Anthropic input_schema-Vertrag).
type LLMTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// LLMToolCall ist ein vom Modell angeforderter Tool-Aufruf.
type LLMToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// LLMResponse bündelt eine Modell-Antwort: Text + angeforderte Tool-Calls +
// die rohen Content-Blocks (für die Rückgabe in die Konversations-History).
type LLMResponse struct {
	StopReason string
	Text       string
	ToolCalls  []LLMToolCall
	Content    []map[string]any
}

// LLMClient abstrahiert das Modell, damit der Loop offline testbar ist.
type LLMClient interface {
	Complete(ctx context.Context, system string, messages []map[string]any, tools []LLMTool) (LLMResponse, error)
}

// ToolExecutor führt einen benannten Tool-Call aus. Der MCPServer implementiert
// dieses Interface — derselbe Pfad wie ein externer MCP-Client.
type ToolExecutor interface {
	ExecuteTool(ctx context.Context, name string, input json.RawMessage, meta CallMeta) (string, error)
}

// OrchestratorStep protokolliert einen ausgeführten Tool-Call (für Events/Audit).
type OrchestratorStep struct {
	Tool   string
	Input  json.RawMessage
	Output string
	Err    string
}

// Orchestrator fährt die LLM↔Tool-Schleife.
type Orchestrator struct {
	LLM      LLMClient
	Tools    []LLMTool
	Executor ToolExecutor
	MaxSteps int
}

// OrchestratorResult ist das Ergebnis einer Runde: finaler Text + Tool-Schritte.
type OrchestratorResult struct {
	Text  string
	Steps []OrchestratorStep
}

// Run führt die Schleife bis zur finalen Modell-Antwort (oder MaxSteps).
func (o *Orchestrator) Run(ctx context.Context, system string, history []map[string]any, meta CallMeta) (OrchestratorResult, error) {
	max := o.MaxSteps
	if max <= 0 {
		max = 8
	}
	messages := append([]map[string]any{}, history...)
	var res OrchestratorResult
	for i := 0; i < max; i++ {
		resp, err := o.LLM.Complete(ctx, system, messages, o.Tools)
		if err != nil {
			return res, err
		}
		content := resp.Content
		if content == nil {
			content = []map[string]any{{"type": "text", "text": resp.Text}}
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": content})

		if len(resp.ToolCalls) == 0 {
			res.Text = resp.Text
			return res, nil
		}

		toolResults := make([]map[string]any, 0, len(resp.ToolCalls))
		for _, tc := range resp.ToolCalls {
			out, exErr := o.Executor.ExecuteTool(ctx, tc.Name, tc.Input, meta)
			step := OrchestratorStep{Tool: tc.Name, Input: tc.Input, Output: out}
			tr := map[string]any{"type": "tool_result", "tool_use_id": tc.ID}
			if exErr != nil {
				step.Err = exErr.Error()
				tr["is_error"] = true
				tr["content"] = exErr.Error()
			} else {
				tr["content"] = out
			}
			res.Steps = append(res.Steps, step)
			toolResults = append(toolResults, tr)
		}
		messages = append(messages, map[string]any{"role": "user", "content": toolResults})
	}
	return res, fmt.Errorf("orchestrator: MaxSteps (%d) erreicht ohne finale Antwort", max)
}

// ── GLM-Anbindung (Z.ai anthropic-compat, gleicher Vertrag wie callGlm) ───────

// glmLLM ist die produktive LLMClient-Implementierung über one-api/Z.ai.
type glmLLM struct{}

func (glmLLM) Complete(ctx context.Context, system string, messages []map[string]any, tools []LLMTool) (LLMResponse, error) {
	key := envOr("ZAI_KEY", "")
	if key == "" {
		return LLMResponse{}, fmt.Errorf("ZAI_KEY nicht gesetzt (systemd-Unit)")
	}
	payload := map[string]any{
		"model":      envOr("REVIEWER_MODEL", "glm-5.1"),
		"max_tokens": 4096,
		"system":     system,
		"messages":   messages,
	}
	if len(tools) > 0 {
		payload["tools"] = tools
	}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, "POST",
		envOr("REVIEWER_BASE_URL", "https://api.z.ai/api/anthropic")+"/v1/messages", bytes.NewReader(b))
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")
	cl := &http.Client{Timeout: 120 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return LLMResponse{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return LLMResponse{}, fmt.Errorf("GLM %d: %.300s", resp.StatusCode, raw)
	}
	var out struct {
		StopReason string           `json:"stop_reason"`
		Content    []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return LLMResponse{}, err
	}
	return parseLLMContent(out.StopReason, out.Content), nil
}

// parseLLMContent zerlegt Anthropic-Content-Blocks in Text + Tool-Calls.
func parseLLMContent(stop string, content []map[string]any) LLMResponse {
	r := LLMResponse{StopReason: stop, Content: content}
	for _, blk := range content {
		switch blk["type"] {
		case "text":
			if t, ok := blk["text"].(string); ok {
				r.Text += t
			}
		case "tool_use":
			var tc LLMToolCall
			if id, ok := blk["id"].(string); ok {
				tc.ID = id
			}
			if n, ok := blk["name"].(string); ok {
				tc.Name = n
			}
			if inp, ok := blk["input"]; ok {
				tc.Input, _ = json.Marshal(inp)
			}
			r.ToolCalls = append(r.ToolCalls, tc)
		}
	}
	return r
}

// buildCopilotSystem erdet den Agenten im realen Karten-Kontext (SC1-Grounding).
func buildCopilotSystem(initiativeJSON string) string {
	return "Du bist der Master-Kanban-Copilot, ein Actor im Board-Event-Log. " +
		"Du beantwortest Fragen ausschließlich auf Basis des unten gelieferten Karten-Kontexts " +
		"(Initiative, Plan-Baum, Beads/Links, Events) und handelst über die bereitgestellten Tools. " +
		"Erfinde keine Beads, Events oder Stages. Mutierende Tools (move-stage) nur auf klare Nutzer-Absicht. " +
		"Antworte knapp auf Deutsch.\n\n--- KARTEN-KONTEXT (JSON) ---\n" + initiativeJSON
}
