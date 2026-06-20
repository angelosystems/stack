package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CopilotChatRequest is the request payload for the drawer/board chat copilot
type CopilotChatRequest struct {
	InitiativeID string `json:"initiative_id"`
	SessionID    string `json:"session_id"`
	Message      string `json:"message"`
}

// CopilotChatResponse is the response returned to the frontend
type CopilotChatResponse struct {
	Answer    string `json:"answer"`
	SessionID string `json:"session_id"`
}

// AgenticOrchestrator is the explicitly named and implemented Orchestration Component
// (Orchestrierungs-Komponente). It executes the agentic loop (LLM <-> Tool) entirely on
// the server-side backend, preventing any client-side credential leakage.
type AgenticOrchestrator struct {
	pool *pgxpool.Pool
}

// NewAgenticOrchestrator instantiates a new Orchestration Component.
func NewAgenticOrchestrator(p *pgxpool.Pool) *AgenticOrchestrator {
	return &AgenticOrchestrator{pool: p}
}

func mapPathToActiveWorktree(planPath string) string {
	if !strings.HasPrefix(planPath, "/opt/stack/") {
		return planPath
	}
	cwd, err := os.Getwd()
	if err != nil {
		return strings.Replace(planPath, "/opt/stack", "/root/solartown/stack/polecats/flint/stack", 1)
	}
	idx := strings.Index(cwd, "/polecats/")
	if idx != -1 {
		sub := cwd[idx+len("/polecats/"):]
		slashIdx := strings.Index(sub, "/")
		var polecatName string
		if slashIdx != -1 {
			polecatName = sub[:slashIdx]
		} else {
			polecatName = sub
		}
		prefix := cwd[:idx]
		activeWorktree := prefix + "/polecats/" + polecatName + "/stack"
		return strings.Replace(planPath, "/opt/stack", activeWorktree, 1)
	}
	if strings.Contains(cwd, "/solartown/stack/") {
		return strings.Replace(planPath, "/opt/stack", "/root/solartown/stack", 1)
	}
	return strings.Replace(planPath, "/opt/stack", "/root/solartown/stack/polecats/flint/stack", 1)
}

// Orchestrate executes the conversation grounding, calls the LLM, parses any Tool calls
// requested by the LLM, executes the local tool (e.g., move-stage), passes results back,
// and persists conversation history as initiative activity events.
func (ao *AgenticOrchestrator) Orchestrate(ctx context.Context, req CopilotChatRequest, actor string) (string, error) {
	p := ao.pool

	// 1. Fetch card/initiative details
	var info struct {
		ID          string
		Firma       string
		Stage       string
		Title       string
		Description string
	}
	err := p.QueryRow(ctx,
		`SELECT id, firma, stage, title, COALESCE(description, '') FROM portfolio.initiative WHERE id = $1`,
		req.InitiativeID).Scan(&info.ID, &info.Firma, &info.Stage, &info.Title, &info.Description)
	if err != nil {
		return "", fmt.Errorf("initiative nicht gefunden: %s", req.InitiativeID)
	}

	// 2. Fetch linked plan file (PRD) text if present
	var planPath string
	_ = p.QueryRow(ctx,
		`SELECT ref FROM portfolio.initiative_link WHERE initiative_id = $1 AND kind = 'plan_file' ORDER BY added_at DESC LIMIT 1`,
		req.InitiativeID).Scan(&planPath)

	prdContent := "Kein verlinktes Plan-Dokument (PRD) vorhanden."
	if planPath != "" {
		// Map to current worktree path dynamically
		mappedPath := mapPathToActiveWorktree(planPath)
		if raw, err := os.ReadFile(mappedPath); err == nil {
			prdContent = string(raw)
		}
	}

	// 3. Fetch linked beads (issues) and details
	rows, err := p.Query(ctx,
		`SELECT ref FROM portfolio.initiative_link WHERE initiative_id = $1 AND kind = 'bead'`,
		req.InitiativeID)
	beadsList := []string{}
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ref string
			if rows.Scan(&ref) == nil {
				beadsList = append(beadsList, ref)
			}
		}
	}
	beadsInfo := "Keine verlinkten Beads vorhanden."
	if len(beadsList) > 0 {
		beadsInfo = "Verlinkte Beads: " + strings.Join(beadsList, ", ")
	}

	// 4. Grounding context compilation (SC1)
	grounding := fmt.Sprintf(`=== INITIATIVE DETAILS ===
ID: %s
Firma: %s
Aktuelles Stage: %s
Titel: %s
Beschreibung: %s

=== BEADS CONTEXT ===
%s

=== PRD / PLAN FILE CONTENT ===
%s`, info.ID, info.Firma, info.Stage, info.Title, info.Description, beadsInfo, prdContent)

	// 5. System Instructions for Copilot
	systemPrompt := fmt.Sprintf(`Du bist der Master-Kanban MCP-Copilot (Kontext: %s).
Du unterstützt Benutzer dabei, diese Initiative zu analysieren und zu pflegen.

Hier ist der aktuelle reale Kontext der Karte (Grounding):
%s

Du kannst auf Wunsch des Benutzers Aktionen ausführen, z.B. die Karte in ein anderes Stage verschieben (idea, soon, now, watching, done).
Um ein Tool auszuführen, antworte in deinem Text EXAKT mit diesem Tool-Aufruf-Muster (es wird vom Server-Backend abgefangen, ausgeführt und dir das Ergebnis zurückgeliefert):
TOOL_CALL: move-stage {"id": "%s", "stage": "<ziel_stage>"}

Mögliche Stages sind: idea, soon, now, watching, done.
Antworte immer freundlich, präzise und auf Deutsch.`, info.ID, grounding, info.ID)

	// 6. Fetch Conversation History from Database (Thread-Isoliert über session_id)
	historyRows, err := p.Query(ctx,
		`SELECT payload FROM portfolio.initiative_event 
		 WHERE initiative_id = $1 AND kind = 'activity' AND source_backend = 'master' AND payload->>'category' = 'copilot' AND payload->>'session_id' = $2
		 ORDER BY at ASC LIMIT 50`,
		req.InitiativeID, req.SessionID)
	
	messages := []map[string]string{}
	if err == nil {
		defer historyRows.Close()
		for historyRows.Next() {
			var pld json.RawMessage
			if historyRows.Scan(&pld) == nil {
				var item struct {
					Role string `json:"role"`
					Text string `json:"text"`
				}
				if json.Unmarshal(pld, &item) == nil && item.Role != "" {
					messages = append(messages, map[string]string{
						"role":    item.Role,
						"content": item.Text,
					})
				}
			}
		}
	}

	// Append the incoming user message
	messages = append(messages, map[string]string{
		"role":    "user",
		"content": req.Message,
	})

	// 7. Save user message event in portfolio.initiative_event (L4)
	userEventPayload, _ := json.Marshal(map[string]any{
		"category":   "copilot",
		"role":       "user",
		"text":       req.Message,
		"session_id": req.SessionID,
		"at":         time.Now(),
	})
	_, _ = p.Exec(ctx,
		`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
		 VALUES ($1, 'activity', 'master', $2, $3)`,
		req.InitiativeID, userEventPayload, actor)

	// 8. Call LLM (agentic loop)
	answer, err := callGlm(systemPrompt, messages)
	if err != nil {
		return "", fmt.Errorf("Fehler beim Aufruf des LLM-Sidecars: %w", err)
	}

	// Check for Tool Calls inside the response (agentic loop)
	re := regexp.MustCompile(`TOOL_CALL:\s*move-stage\s*(\{.*\})`)
	match := re.FindStringSubmatch(answer)
	if len(match) > 1 {
		var toolArgs struct {
			ID    string `json:"id"`
			Stage string `json:"stage"`
		}
		if json.Unmarshal([]byte(match[1]), &toolArgs) == nil && toolArgs.ID != "" && toolArgs.Stage != "" {
			// Execute move-stage directly (secured with SSO actor detail)
			_, err = p.Exec(ctx,
				`UPDATE portfolio.initiative SET stage = $2, stage_locked_by_human = true WHERE id = $1`,
				toolArgs.ID, toolArgs.Stage)

			var toolResult string
			if err != nil {
				toolResult = "Fehler beim Verschieben: " + err.Error()
			} else {
				toolResult = fmt.Sprintf("Erfolg: Initiative %s erfolgreich in Stage '%s' verschoben.", toolArgs.ID, toolArgs.Stage)
				// Log the move event
				_, _ = p.Exec(ctx,
					`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, from_stage, to_stage, actor)
					 VALUES ($1, 'moved', 'master', $2, $3, $4)`,
					toolArgs.ID, info.Stage, toolArgs.Stage, actor)
			}

			// Feed tool result back to LLM to get final natural response
			messages = append(messages, map[string]string{
				"role":    "assistant",
				"content": answer,
			})
			messages = append(messages, map[string]string{
				"role":    "user",
				"content": "TOOL_RESPONSE: " + toolResult,
			})

			finalAnswer, err := callGlm(systemPrompt, messages)
			if err == nil {
				answer = finalAnswer
			}
		}
	}

	// 9. Save assistant message event in portfolio.initiative_event (L4)
	assistantEventPayload, _ := json.Marshal(map[string]any{
		"category":   "copilot",
		"role":       "assistant",
		"text":       answer,
		"session_id": req.SessionID,
		"at":         time.Now(),
	})
	_, _ = p.Exec(ctx,
		`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
		 VALUES ($1, 'activity', 'master', $2, $3)`,
		req.InitiativeID, assistantEventPayload, "mcp-copilot")

	return answer, nil
}

// handleCopilotChat is the main server-side endpoint for the agentic copilot chat
func handleCopilotChat(p *pgxpool.Pool) http.HandlerFunc {
	orchestrator := NewAgenticOrchestrator(p)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Auth-Request-Email, X-Api-Key")
		if r.Method == "OPTIONS" {
			return
		}
		if r.Method != "POST" {
			http.Error(w, "POST only", 405)
			return
		}

		// Auth gating (SC5)
		if !checkAuth(r) {
			http.Error(w, "unauthorized", 401)
			return
		}

		var req CopilotChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON payload: "+err.Error(), 400)
			return
		}

		req.InitiativeID = strings.TrimSpace(req.InitiativeID)
		if req.InitiativeID == "" {
			http.Error(w, "initiative_id ist erforderlich", 400)
			return
		}

		req.Message = strings.TrimSpace(req.Message)
		if req.Message == "" {
			http.Error(w, "message darf nicht leer sein", 400)
			return
		}

		if req.SessionID == "" {
			// Fallback session identifier if none is provided
			req.SessionID = "default-session-" + req.InitiativeID
		}

		// Execute chat via the Orchestrator Component
		answer, err := orchestrator.Orchestrate(r.Context(), req, actorFrom(r))
		if err != nil {
			if strings.Contains(err.Error(), "nicht gefunden") {
				http.Error(w, err.Error(), 404)
			} else {
				http.Error(w, err.Error(), 502)
			}
			return
		}

		// 10. Send final answer back to client
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(CopilotChatResponse{
			Answer:    answer,
			SessionID: req.SessionID,
		})
	}
}
