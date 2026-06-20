package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// MCP JSON-RPC structures
type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   any    `json:"error,omitempty"`
}

type mcpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Resource structure for list
type mcpResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MimeType    string `json:"mimeType,omitempty"`
}

// Tool structures
type mcpTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

type mcpToolResponse struct {
	Content []mcpToolContent `json:"content"`
	IsError bool             `json:"isError,omitempty"`
}

type mcpToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func cmdMcp() *cobra.Command {
	var apiURL string
	c := &cobra.Command{
		Use:   "mcp",
		Short: "Startet den Master-Kanban MCP Server über stdio (Model Context Protocol)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Start stdio loop
			return runMcpServer(apiURL)
		},
	}
	c.Flags().StringVar(&apiURL, "api-url", envOr("PORTFOLIO_API_URL", "http://localhost:7770"), "URL des master-kanban serve API Backends")
	return c
}

func runMcpServer(apiURL string) error {
	dec := json.NewDecoder(os.Stdin)
	for {
		var req mcpRequest
		if err := dec.Decode(&req); err != nil {
			if err == io.EOF {
				return nil
			}
			sendMcpError(nil, -32700, "Parse error: "+err.Error())
			continue
		}

		if req.JSONRPC != "2.0" {
			sendMcpError(req.ID, -32600, "Invalid Request: expected jsonrpc 2.0")
			continue
		}

		switch req.Method {
		case "initialize":
			sendMcpResult(req.ID, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"resources": map[string]any{},
					"tools":     map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "master-kanban-mcp",
					"version": "0.1.0",
				},
			})

		case "notifications/initialized":
			// No response needed for notifications

		case "resources/list":
			resources := []mcpResource{
				{
					URI:         "board://all",
					Name:        "Board",
					Description: "Das gesamte Kanban Board inklusive aller Initiativen, Kapazitäten und Backlog.",
					MimeType:    "application/json",
				},
			}
			sendMcpResult(req.ID, map[string]any{
				"resources": resources,
			})

		case "resources/read":
			var params struct {
				URI string `json:"uri"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				sendMcpError(req.ID, -32602, "Invalid params: "+err.Error())
				continue
			}

			text, mime, err := readMcpResource(apiURL, params.URI)
			if err != nil {
				sendMcpError(req.ID, -32000, "Error reading resource: "+err.Error())
				continue
			}

			sendMcpResult(req.ID, map[string]any{
				"contents": []map[string]any{
					{
						"uri":      params.URI,
						"mimeType": mime,
						"text":     text,
					},
				},
			})

		case "tools/list":
			tools := []mcpTool{
				{
					Name:        "move-stage",
					Description: "Verschiebe eine Initiative in ein anderes Stage (idea, soon, now, watching, done). Dies ist eine mutierende Aktion und ist auth-gegated.",
					InputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id": map[string]any{
								"type":        "string",
								"description": "Die ID der Initiative (z.B. sa-karten-id)",
							},
							"stage": map[string]any{
								"type":        "string",
								"description": "Das Ziel-Stage. Gültige Werte: idea, soon, now, watching, done",
							},
						},
						"required": []string{"id", "stage"},
					},
				},
				{
					Name:        "capture",
					Description: "Erfasst ein Inline-Event/Aktion und ordnet es der passenden oder Catch-all-Initiative zu. Gewährleistet Idempotenz.",
					InputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"text": map[string]any{
								"type":        "string",
								"description": "Der Aktionstext/Event-Inhalt (z.B. Quick Fix details oder Commit-Text).",
							},
							"firma": map[string]any{
								"type":        "string",
								"description": "Die Firma (stayawesome, solartown, quantbot, mariobrain, angeloos, stack). Optional.",
							},
						},
						"required": []string{"text"},
					},
				},
			}
			sendMcpResult(req.ID, map[string]any{
				"tools": tools,
			})

		case "tools/call":
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				sendMcpError(req.ID, -32602, "Invalid params: "+err.Error())
				continue
			}

			if params.Name != "move-stage" && params.Name != "capture" {
				sendMcpError(req.ID, -32601, "Method not found (unknown tool): "+params.Name)
				continue
			}

			if params.Name == "move-stage" {
				var args struct {
					ID    string `json:"id"`
					Stage string `json:"stage"`
				}
				if err := json.Unmarshal(params.Arguments, &args); err != nil {
					sendMcpError(req.ID, -32602, "Invalid tool arguments: "+err.Error())
					continue
				}

				resText, isErr, err := callMcpToolMoveStage(apiURL, args.ID, args.Stage)
				if err != nil {
					sendMcpResult(req.ID, mcpToolResponse{
						Content: []mcpToolContent{
							{
								Type: "text",
								Text: "Fehler beim Ausführen der Aktion: " + err.Error(),
							},
						},
						IsError: true,
					})
					continue
				}

				sendMcpResult(req.ID, mcpToolResponse{
					Content: []mcpToolContent{
						{
							Type: "text",
							Text: resText,
						},
					},
					IsError: isErr,
				})
			} else if params.Name == "capture" {
				var args struct {
					Text  string `json:"text"`
					Firma string `json:"firma"`
				}
				if err := json.Unmarshal(params.Arguments, &args); err != nil {
					sendMcpError(req.ID, -32602, "Invalid tool arguments: "+err.Error())
					continue
				}

				resText, isErr, err := callMcpToolCapture(apiURL, args.Text, args.Firma)
				if err != nil {
					sendMcpResult(req.ID, mcpToolResponse{
						Content: []mcpToolContent{
							{
								Type: "text",
								Text: "Fehler beim Ausführen der Aktion: " + err.Error(),
							},
						},
						IsError: true,
					})
					continue
				}

				sendMcpResult(req.ID, mcpToolResponse{
					Content: []mcpToolContent{
						{
							Type: "text",
							Text: resText,
						},
					},
					IsError: isErr,
				})
			}

		default:
			sendMcpError(req.ID, -32601, "Method not found: "+req.Method)
		}
	}
}

func sendMcpResult(id any, result any) {
	resp := mcpResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	b, _ := json.Marshal(resp)
	os.Stdout.Write(append(b, '\n'))
}

func sendMcpError(id any, code int, message string) {
	resp := mcpResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: mcpError{
			Code:    code,
			Message: message,
		},
	}
	b, _ := json.Marshal(resp)
	os.Stdout.Write(append(b, '\n'))
}

func readMcpResource(apiURL, uri string) (string, string, error) {
	// Parse URIs like board://all, initiative://sa-card, plan-file://id
	cleanURI := uri
	if strings.Contains(uri, "://") {
		parts := strings.SplitN(uri, "://", 2)
		kind := parts[0]
		target := parts[1]

		switch kind {
		case "board":
			return fetchBoardResource(apiURL)
		case "initiative":
			return fetchInitiativeResource(apiURL, target)
		case "plan-file":
			return fetchPlanFileResource(apiURL, target)
		default:
			return "", "", fmt.Errorf("unsupported resource scheme: %s", kind)
		}
	}

	// Fallback to suffix matching or simple names
	if cleanURI == "board" {
		return fetchBoardResource(apiURL)
	} else if strings.HasPrefix(cleanURI, "initiative/") {
		return fetchInitiativeResource(apiURL, strings.TrimPrefix(cleanURI, "initiative/"))
	} else if strings.HasPrefix(cleanURI, "plan-file/") {
		return fetchPlanFileResource(apiURL, strings.TrimPrefix(cleanURI, "plan-file/"))
	}

	return "", "", fmt.Errorf("invalid resource URI: %s", uri)
}

func fetchBoardResource(apiURL string) (string, string, error) {
	// 1. Fetch initiatives
	initiatives, err := httpGetWrapper(apiURL + "/api/initiatives")
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch initiatives: %w", err)
	}

	// 2. Fetch capacity
	capacity, err := httpGetWrapper(apiURL + "/api/capacity")
	if err != nil {
		// Capacity might fail or skip, allow grace
		capacity = []byte(`{}`)
	}

	// 3. Fetch backlog
	backlog, err := httpGetWrapper(apiURL + "/api/backlog")
	if err != nil {
		backlog = []byte(`[]`)
	}

	combined := map[string]json.RawMessage{
		"initiatives": initiatives,
		"capacity":    capacity,
		"backlog":     backlog,
	}

	b, err := json.MarshalIndent(combined, "", "  ")
	if err != nil {
		return "", "", err
	}
	return string(b), "application/json", nil
}

func fetchInitiativeResource(apiURL, id string) (string, string, error) {
	data, err := httpGetWrapper(apiURL + "/api/initiative?id=" + id)
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch initiative %s: %w", id, err)
	}
	return string(data), "application/json", nil
}

func fetchPlanFileResource(apiURL, id string) (string, string, error) {
	data, err := httpGetWrapper(apiURL + "/api/plan-content?id=" + id)
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch plan-content %s: %w", id, err)
	}
	var res struct {
		Markdown string `json:"markdown"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return "", "", err
	}
	return res.Markdown, "text/markdown", nil
}

func callMcpToolMoveStage(apiURL, id, stage string) (string, bool, error) {
	payload := map[string]string{
		"id":    id,
		"stage": stage,
	}
	b, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", apiURL+"/api/move", bytes.NewReader(b))
	if err != nil {
		return "", true, err
	}
	req.Header.Set("Content-Type", "application/json")

	// Forward credentials from the environment if present
	if key := os.Getenv("PORTFOLIO_API_KEY"); key != "" {
		req.Header.Set("X-Api-Key", key)
	}
	if email := os.Getenv("PORTFOLIO_AUTH_EMAIL"); email != "" {
		req.Header.Set("X-Auth-Request-Email", email)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", true, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body)), true, nil
	}

	var res struct {
		OK bool `json:"ok"`
	}
	_ = json.Unmarshal(body, &res)

	if !res.OK {
		return "Aktion nicht erfolgreich: " + string(body), true, nil
	}

	return fmt.Sprintf("Initiative %s erfolgreich in das Stage '%s' verschoben.", id, stage), false, nil
}

func callMcpToolCapture(apiURL, text, firma string) (string, bool, error) {
	payload := map[string]string{
		"text":  text,
		"firma": firma,
	}
	b, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", apiURL+"/api/capture", bytes.NewReader(b))
	if err != nil {
		return "", true, err
	}
	req.Header.Set("Content-Type", "application/json")

	// Forward credentials from the environment if present
	if key := os.Getenv("PORTFOLIO_API_KEY"); key != "" {
		req.Header.Set("X-Api-Key", key)
	}
	if email := os.Getenv("PORTFOLIO_AUTH_EMAIL"); email != "" {
		req.Header.Set("X-Auth-Request-Email", email)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", true, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body)), true, nil
	}

	var res struct {
		OK        bool   `json:"ok"`
		MatchedID string `json:"matched_id"`
		Skipped   bool   `json:"skipped"`
	}
	_ = json.Unmarshal(body, &res)

	if !res.OK {
		return "Aktion nicht erfolgreich: " + string(body), true, nil
	}

	if res.Skipped {
		return fmt.Sprintf("Event bereits vorhanden (idempotent übersprungen) für Initiative: %s", res.MatchedID), false, nil
	}

	return fmt.Sprintf("Event erfolgreich erfasst für Initiative: %s", res.MatchedID), false, nil
}

func httpGetWrapper(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	// Forward credentials from the environment for read endpoints as well (just in case they become gated)
	if key := os.Getenv("PORTFOLIO_API_KEY"); key != "" {
		req.Header.Set("X-Api-Key", key)
	}
	if email := os.Getenv("PORTFOLIO_AUTH_EMAIL"); email != "" {
		req.Header.Set("X-Auth-Request-Email", email)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
}
