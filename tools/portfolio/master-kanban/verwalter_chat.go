package main

// mk-verwalter-chat WP1: Chat-Proxy Cockpit ↔ mk-verwalter. Nachrichten sind
// Kommentare am dauerhaften Chat-Issue der Paperclip-CF-Instanz (vault),
// erreicht ueber den bestehenden 3101-Tunnel; jede Mario-Nachricht weckt den
// Verwalter (Self-Wake-Route). Kein neuer Speicher, keine neue Auth.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	cfBase        = "http://127.0.0.1:3101"
	cfCompanyID   = "88632df2-43a5-4620-9dea-1535f6cdfb35"
	mkAgentID     = "013774b1-ec18-42ce-8267-5eff32e42abe"
	chatIssueFile = "/root/.secrets/cf-mario-chat-issue.id"
	sinkTokenFile = "/root/.secrets/cf-paperclip-token.env" // PAPERCLIP_TOKEN= (Kommentar-Recht)
	mkTokenFile   = "/root/.secrets/cf-mk-verwalter-agent-key.env" // MK_AGENT_TOKEN= (Self-Wake)
)

func secretVar(file, key string) string {
	raw, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimSpace(strings.SplitN(line, "=", 2)[1])
		}
	}
	return ""
}

func chatIssueID() string {
	raw, err := os.ReadFile(chatIssueFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func cfRequest(method, path, token string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, cfBase+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 10 * time.Second}
	return client.Do(req)
}

type chatMsg struct {
	Von  string `json:"von"` // mario | verwalter
	Text string `json:"text"`
	At   string `json:"at"`
}

// verwalterRunning: laeuft/steht gerade ein Run des mk-verwalter an?
func verwalterRunning(token string) bool {
	resp, err := cfRequest("GET", "/api/companies/"+cfCompanyID+"/heartbeat-runs?agentId="+mkAgentID+"&limit=1", token, nil)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var payload any
	if json.NewDecoder(resp.Body).Decode(&payload) != nil {
		return false
	}
	items := extractList(payload, "runs", "data")
	if len(items) == 0 {
		return false
	}
	if m, ok := items[0].(map[string]any); ok {
		s, _ := m["status"].(string)
		return s == "running" || s == "queued"
	}
	return false
}

func extractList(payload any, keys ...string) []any {
	if l, ok := payload.([]any); ok {
		return l
	}
	if m, ok := payload.(map[string]any); ok {
		for _, k := range keys {
			if l, ok := m[k].([]any); ok {
				return l
			}
		}
	}
	return nil
}

func handleVerwalterChat() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			return
		}
		issue := chatIssueID()
		sink := secretVar(sinkTokenFile, "PAPERCLIP_TOKEN")
		if issue == "" || sink == "" {
			http.Error(w, "Chat-Kanal nicht konfiguriert: "+chatIssueFile+" / "+sinkTokenFile+" pruefen", 503)
			return
		}

		switch r.Method {
		case "GET":
			resp, err := cfRequest("GET", "/api/issues/"+issue+"/comments", sink, nil)
			if err != nil {
				http.Error(w, "Fabrik-Instanz nicht erreichbar (Tunnel :3101 / cf-fabrik-tunnel.service pruefen): "+err.Error(), 502)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
				http.Error(w, fmt.Sprintf("Chat-Issue nicht lesbar (HTTP %d): %s — Issue-ID in %s pruefen", resp.StatusCode, string(b), chatIssueFile), 502)
				return
			}
			var payload any
			_ = json.NewDecoder(resp.Body).Decode(&payload)
			var msgs []chatMsg
			for _, it := range extractList(payload, "comments", "data") {
				m, ok := it.(map[string]any)
				if !ok {
					continue
				}
				body, _ := m["body"].(string)
				at, _ := m["createdAt"].(string)
				von := "verwalter"
				if strings.HasPrefix(body, "**Mario:**") {
					von = "mario"
					body = strings.TrimSpace(strings.TrimPrefix(body, "**Mario:**"))
				}
				msgs = append(msgs, chatMsg{Von: von, Text: body, At: at})
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"messages": msgs,
				"running":  verwalterRunning(sink),
			})

		case "POST":
			var body struct{ Text string `json:"text"` }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" {
				http.Error(w, "text erforderlich", 400)
				return
			}
			resp, err := cfRequest("POST", "/api/issues/"+issue+"/comments", sink,
				map[string]string{"body": "**Mario:** " + strings.TrimSpace(body.Text)})
			if err != nil {
				http.Error(w, "Fabrik-Instanz nicht erreichbar (Tunnel :3101 pruefen): "+err.Error(), 502)
				return
			}
			resp.Body.Close()
			if resp.StatusCode != 201 && resp.StatusCode != 200 {
				http.Error(w, fmt.Sprintf("Kommentar abgelehnt (HTTP %d)", resp.StatusCode), 502)
				return
			}
			// Wake best-effort — misslingt er, holt der Re-Wake-Backup-Tick nach.
			woken := false
			if mk := secretVar(mkTokenFile, "MK_AGENT_TOKEN"); mk != "" {
				wresp, werr := cfRequest("POST", "/api/agents/"+mkAgentID+"/wakeup", mk,
					map[string]string{"reason": "Mario-Chat: neue Nachricht im Cockpit-Kanal — bitte im Chat-Issue antworten"})
				if werr == nil {
					wresp.Body.Close()
					woken = wresp.StatusCode < 300
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "woken": woken})

		default:
			http.Error(w, "GET oder POST", 405)
		}
	}
}
