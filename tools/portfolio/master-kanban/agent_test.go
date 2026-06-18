package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// scriptedLLM gibt vorab definierte Antworten zurück (deterministischer Loop).
type scriptedLLM struct {
	responses []LLMResponse
	calls     int
	seenTools int
}

func (l *scriptedLLM) Complete(_ context.Context, _ string, _ []map[string]any, tools []LLMTool) (LLMResponse, error) {
	l.seenTools = len(tools)
	r := l.responses[l.calls]
	l.calls++
	return r, nil
}

type recordingExecutor struct {
	executed []string
	lastMeta CallMeta
}

func (e *recordingExecutor) ExecuteTool(_ context.Context, name string, _ json.RawMessage, meta CallMeta) (string, error) {
	e.executed = append(e.executed, name)
	e.lastMeta = meta
	return "ok: " + name, nil
}

func TestOrchestratorRunsToolLoop(t *testing.T) {
	llm := &scriptedLLM{responses: []LLMResponse{
		{ // 1. Runde: Modell fordert einen Tool-Call an
			StopReason: "tool_use",
			Content: []map[string]any{
				{"type": "tool_use", "id": "t1", "name": "move-stage", "input": map[string]any{"id": "st-573ds", "stage": "soon"}},
			},
			ToolCalls: []LLMToolCall{{ID: "t1", Name: "move-stage", Input: json.RawMessage(`{"id":"st-573ds","stage":"soon"}`)}},
		},
		{ // 2. Runde: Modell antwortet final ohne Tool
			StopReason: "end_turn",
			Text:       "Erledigt: nach soon verschoben.",
			Content:    []map[string]any{{"type": "text", "text": "Erledigt: nach soon verschoben."}},
		},
	}}
	exec := &recordingExecutor{}
	orch := &Orchestrator{LLM: llm, Tools: []LLMTool{moveStageTool()}, Executor: exec, MaxSteps: 5}

	res, err := orch.Run(context.Background(), "sys",
		[]map[string]any{{"role": "user", "content": "verschieb nach soon"}},
		CallMeta{Email: "mario@stayawesome.de"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "Erledigt: nach soon verschoben." {
		t.Errorf("finaler Text = %q", res.Text)
	}
	if len(res.Steps) != 1 || res.Steps[0].Tool != "move-stage" {
		t.Errorf("erwarte 1 move-stage-Step, got %v", res.Steps)
	}
	if len(exec.executed) != 1 || exec.executed[0] != "move-stage" {
		t.Errorf("Tool nicht ausgeführt: %v", exec.executed)
	}
	if l := llm; l.calls != 2 {
		t.Errorf("erwarte 2 LLM-Calls, got %d", l.calls)
	}
	if llm.seenTools != 1 {
		t.Errorf("Tools nicht an LLM gereicht: %d", llm.seenTools)
	}
	if exec.lastMeta.Email != "mario@stayawesome.de" {
		t.Errorf("CallMeta nicht durchgereicht: %+v", exec.lastMeta)
	}
}

func TestOrchestratorNoToolReturnsImmediately(t *testing.T) {
	llm := &scriptedLLM{responses: []LLMResponse{
		{StopReason: "end_turn", Text: "Nur eine Lese-Antwort.", Content: []map[string]any{{"type": "text", "text": "Nur eine Lese-Antwort."}}},
	}}
	exec := &recordingExecutor{}
	orch := &Orchestrator{LLM: llm, Executor: exec}
	res, err := orch.Run(context.Background(), "sys", []map[string]any{{"role": "user", "content": "was ist hier los?"}}, CallMeta{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "Nur eine Lese-Antwort." || len(res.Steps) != 0 {
		t.Errorf("Lese-Antwort sollte ohne Tool-Schritt zurückkehren: %+v", res)
	}
}

// Integration: Orchestrator + echter MCPServer-Executor + fakeMover.
// Belegt, dass die LLM↔Tool-Schleife tatsächlich /api/move (via Mover) auslöst.
func TestOrchestratorWithMCPExecutor(t *testing.T) {
	os.Setenv("PORTFOLIO_API_KEY", "test-secret-key")
	defer os.Unsetenv("PORTFOLIO_API_KEY")

	srv, mover := newTestServer()
	llm := &scriptedLLM{responses: []LLMResponse{
		{StopReason: "tool_use",
			Content:   []map[string]any{{"type": "tool_use", "id": "t1", "name": "move-stage", "input": map[string]any{"id": "st-573ds", "stage": "watching"}}},
			ToolCalls: []LLMToolCall{{ID: "t1", Name: "move-stage", Input: json.RawMessage(`{"id":"st-573ds","stage":"watching"}`)}}},
		{StopReason: "end_turn", Text: "verschoben.", Content: []map[string]any{{"type": "text", "text": "verschoben."}}},
	}}
	orch := &Orchestrator{LLM: llm, Tools: []LLMTool{moveStageTool()}, Executor: srv, MaxSteps: 5}

	res, err := orch.Run(context.Background(), "sys",
		[]map[string]any{{"role": "user", "content": "ab nach watching"}},
		CallMeta{APIKey: "test-secret-key"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Text != "verschoben." {
		t.Errorf("Text = %q", res.Text)
	}
	if len(mover.moved) != 1 || mover.moved[0] != "st-573ds:watching" {
		t.Errorf("Mover nicht über Orchestrator aufgerufen: %v", mover.moved)
	}
}

func TestOrchestratorUnauthorizedToolBecomesErrorStep(t *testing.T) {
	srv, mover := newTestServer()
	llm := &scriptedLLM{responses: []LLMResponse{
		{StopReason: "tool_use",
			Content:   []map[string]any{{"type": "tool_use", "id": "t1", "name": "move-stage", "input": map[string]any{"id": "st-573ds", "stage": "done"}}},
			ToolCalls: []LLMToolCall{{ID: "t1", Name: "move-stage", Input: json.RawMessage(`{"id":"st-573ds","stage":"done"}`)}}},
		{StopReason: "end_turn", Text: "Ich darf das nicht ohne Auth.", Content: []map[string]any{{"type": "text", "text": "Ich darf das nicht ohne Auth."}}},
	}}
	orch := &Orchestrator{LLM: llm, Tools: []LLMTool{moveStageTool()}, Executor: srv, MaxSteps: 5}
	res, err := orch.Run(context.Background(), "sys",
		[]map[string]any{{"role": "user", "content": "verschieb"}}, CallMeta{}) // keine Auth
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Steps) != 1 || res.Steps[0].Err == "" || !strings.Contains(res.Steps[0].Err, "unauthorized") {
		t.Errorf("erwarte unauthorized-Error-Step, got %+v", res.Steps)
	}
	if len(mover.moved) != 0 {
		t.Errorf("Mover bei fehlender Auth nicht aufrufen: %v", mover.moved)
	}
}

func TestParseLLMContent(t *testing.T) {
	r := parseLLMContent("tool_use", []map[string]any{
		{"type": "text", "text": "denke nach "},
		{"type": "tool_use", "id": "x", "name": "move-stage", "input": map[string]any{"id": "a", "stage": "now"}},
	})
	if r.Text != "denke nach " {
		t.Errorf("Text = %q", r.Text)
	}
	if len(r.ToolCalls) != 1 || r.ToolCalls[0].Name != "move-stage" {
		t.Fatalf("ToolCalls falsch: %+v", r.ToolCalls)
	}
	if !strings.Contains(string(r.ToolCalls[0].Input), `"stage":"now"`) {
		t.Errorf("Input nicht serialisiert: %s", r.ToolCalls[0].Input)
	}
}
