package main

// mk-session-lane Stufe 2: Frage an die bauende Session — headless Resume
// (claude --resume <id> -p) im Hintergrund, Antwort als Karten-Kommentar.
// Ehrlich teuer: jeder Aufruf laedt den vollen Session-Kontext; darum nur
// EINE laufende Frage je Karte und harte Laufzeit-Kappe.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var sessionAskBusy sync.Map // initiativeID -> true solange eine Frage laeuft

func sessionComment(p *pgxpool.Pool, initiativeID, actor, text string) {
	payload, _ := json.Marshal(map[string]string{"text": text})
	_, _ = p.Exec(context.Background(),
		`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
		 VALUES ($1,'commented','master',$2::jsonb,$3)`, initiativeID, string(payload), actor)
}

func runSessionAsk(p *pgxpool.Pool, initiativeID, title, sessionID, frage string) {
	defer sessionAskBusy.Delete(initiativeID)
	kurz := sessionID
	if len(kurz) > 8 {
		kurz = kurz[:8]
	}
	prompt := fmt.Sprintf(
		"Frage aus dem Master-Kanban zur Karte %s (%q):\n\n%s\n\n"+
			"Antworte kompakt in Markdown NUR mit der Antwort auf die Frage — "+
			"fuehre KEINE Aktionen aus, aendere keine Dateien, starte nichts.",
		initiativeID, title, frage)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "--resume", sessionID, "-p", prompt, "--max-turns", "3")
	cmd.Dir = "/root" // Sessions laufen auf werkstatt in /root; ponytail: CWD je Karte aus plan_item.repo
	cmd.Env = append(cmd.Environ(), "HOME=/root")
	out, err := cmd.Output()
	answer := strings.TrimSpace(string(out))
	if err != nil && answer == "" {
		msg := err.Error()
		if ctx.Err() == context.DeadlineExceeded {
			msg = "Laufzeit-Kappe (15 min) erreicht — Session-Kontext vermutlich zu gross fuer eine schnelle Antwort"
		}
		sessionComment(p, initiativeID, "session:"+kurz,
			"⚠ Session-Antwort fehlgeschlagen: "+msg+" — Alternative: ⧉ Session aufrufen (Terminal-Resume).")
		return
	}
	if len(answer) > 6000 {
		answer = answer[:6000] + "\n\n… (gekuerzt — volle Antwort im Session-Transcript)"
	}
	sessionComment(p, initiativeID, "session:"+kurz, "🤖 Antwort der Session "+kurz+":\n\n"+answer)
}

func handleSessionAsk(p *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,X-Api-Key")
		if r.Method == "OPTIONS" {
			return
		}
		if r.Method != "POST" {
			http.Error(w, "POST only", 405)
			return
		}
		if !checkAuth(r) {
			http.Error(w, "auth erforderlich (SSO-Header oder X-Api-Key)", 401)
			return
		}
		var body struct {
			Id   string `json:"id"`
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" || body.Id == "" {
			http.Error(w, "id und text erforderlich", 400)
			return
		}
		var title, sessionID string
		err := p.QueryRow(r.Context(),
			`SELECT i.title, COALESCE((SELECT t.value FROM portfolio.initiative_tag t
			         WHERE t.initiative_id=i.id AND t.kind='session' LIMIT 1),'')
			 FROM portfolio.initiative i WHERE i.id=$1`, body.Id).Scan(&title, &sessionID)
		if err != nil {
			http.Error(w, "karte nicht gefunden: "+body.Id, 404)
			return
		}
		if sessionID == "" {
			http.Error(w, "karte hat kein session-Tag — nur Session-gebaute Karten sind befragbar", 409)
			return
		}
		if _, busy := sessionAskBusy.LoadOrStore(body.Id, true); busy {
			http.Error(w, "an dieser Karte laeuft schon eine Frage — Antwort kommt als Kommentar", 429)
			return
		}
		frage := strings.TrimSpace(body.Text)
		sessionComment(p, body.Id, "mario", "❓ Frage an die Session: "+frage)
		go runSessionAsk(p, body.Id, title, sessionID, frage)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(202)
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "hint": "Antwort kommt als Karten-Kommentar (Session laedt ihren Kontext — Minuten, nicht Sekunden)",
		})
	}
}
