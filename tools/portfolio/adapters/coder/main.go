// coder-adapter — Coder-Workspace Lifecycle → Master-Kanban
//
// Edge-triggered: nimmt Coder-Webhooks entgegen, mappt sie auf
// portfolio.initiative_event und schreibt direkt in mario_brain (:5434).
// Die DB-Connection läuft unter einem scoped Role (`coder_adapter`), der
// nur INSERT auf portfolio.initiative_event darf — siehe
// schema/portfolio-005-coder-adapter.sql.
//
// Workspace-Mapping: ein Workspace gehört per initiative_link
// (kind='coder_workspace', ref=<workspace-name>) zu genau einer Initiative.
// Webhooks ohne Link werden geloggt und verworfen — kein Auto-Link.
//
// Usage:
//   coder-adapter --listen :7785
//
// Webhook-Payload (Coder ≥ 2.x, vereinfacht):
//   { "event": "workspace.started", "workspace": { "name": "..." }, "user": {...} }
//
// Auth: shared secret im Header X-Coder-Secret (env CODER_WEBHOOK_SECRET).

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	dsn     = envOr("PORTFOLIO_DSN", "postgres://coder_adapter@127.0.0.1:5434/mario_brain?sslmode=disable")
	secret  = envOr("CODER_WEBHOOK_SECRET", "")
	listen  = flag.String("listen", ":7785", "HTTP listen address")
	Version string = "dev"
)

// eventKindFor mappt Coder-Lifecycle-Events auf die zulässigen
// initiative_event.kind-Werte. 'workspace.started' = Arbeit beginnt
// (activity); 'workspace.stopped'/'deleted' = Stand-by/Abschluss
// (activity). 'completed' bleibt explizit dem Backend vorbehalten, das
// echten Abschluss kennt (bd, plan_file).
func eventKindFor(coderEvent string) string {
	switch coderEvent {
	case "workspace.started", "workspace.stopped", "workspace.deleted",
		"workspace.created", "workspace.updated":
		return "activity"
	default:
		return ""
	}
}

type webhook struct {
	Event     string `json:"event"`
	Workspace struct {
		Name  string `json:"name"`
		Owner string `json:"owner"`
	} `json:"workspace"`
	User struct {
		Username string `json:"username"`
	} `json:"user"`
}

func main() {
	flag.Parse()
	if secret == "" {
		die("config", fmt.Errorf("CODER_WEBHOOK_SECRET ist nicht gesetzt"))
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		die("connect", err)
	}
	defer pool.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"ok":true,"version":"`+Version+`"}`)
	})
	mux.HandleFunc("/api/coder-webhook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("X-Coder-Secret") != secret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var wh webhook
		if err := json.NewDecoder(r.Body).Decode(&wh); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		status, err := handleEvent(r.Context(), pool, &wh)
		if err != nil {
			http.Error(w, err.Error(), status)
			return
		}
		fmt.Fprintln(w, `{"ok":true}`)
	})

	fmt.Println("coder-adapter listening", *listen)
	if err := http.ListenAndServe(*listen, mux); err != nil {
		die("listen", err)
	}
}

// handleEvent: Workspace → initiative_id auflösen, Event schreiben.
// Liefert HTTP-Status für Fehlerfälle. 204 (kein Link) wird als „ok,
// nichts zu tun" behandelt — Coder soll Webhooks nicht endlos retryen.
func handleEvent(ctx context.Context, p *pgxpool.Pool, wh *webhook) (int, error) {
	kind := eventKindFor(wh.Event)
	if kind == "" {
		return http.StatusOK, nil
	}
	if wh.Workspace.Name == "" {
		return http.StatusBadRequest, fmt.Errorf("workspace.name fehlt")
	}

	var initiativeID string
	err := p.QueryRow(ctx,
		`SELECT initiative_id FROM portfolio.initiative_link
		 WHERE kind='coder_workspace' AND ref=$1 LIMIT 1`,
		wh.Workspace.Name).Scan(&initiativeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  · no link for workspace %q (%s)\n", wh.Workspace.Name, wh.Event)
		return http.StatusOK, nil
	}

	payload, _ := json.Marshal(map[string]any{
		"coder_event":    wh.Event,
		"workspace_name": wh.Workspace.Name,
		"owner":          wh.Workspace.Owner,
	})
	actor := wh.User.Username
	if actor == "" {
		actor = "coder-adapter"
	}
	if _, err := p.Exec(ctx,
		`INSERT INTO portfolio.initiative_event
		   (initiative_id, kind, source_backend, payload, actor)
		 VALUES ($1, $2, 'coder', $3, $4)`,
		initiativeID, kind, payload, actor); err != nil {
		return http.StatusInternalServerError, err
	}
	fmt.Printf("  ✓ %s → %s (%s)\n", wh.Workspace.Name, initiativeID, wh.Event)
	return http.StatusOK, nil
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func die(ctx string, err error) {
	fmt.Fprintln(os.Stderr, ctx+":", err)
	os.Exit(1)
}
