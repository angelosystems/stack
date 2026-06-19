package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

var (
	dsn     string
	pool    *pgxpool.Pool
	stPool  *pgxpool.Pool // Solartown-Ledger (read + Triage-Labels)
	Version string = "dev"
)

// pgxRows deckt pgx.Rows ab, ohne pgx direkt zu importieren wo's nicht nötig ist
type pgxRows interface {
	Next() bool
	Scan(dest ...any) error
	Close()
}

func writeJSONArray(w http.ResponseWriter, rows pgxRows) {
	w.Write([]byte("["))
	first := true
	for rows.Next() {
		var j json.RawMessage
		if err := rows.Scan(&j); err != nil {
			continue
		}
		if !first {
			w.Write([]byte(","))
		}
		w.Write(j)
		first = false
	}
	w.Write([]byte("]"))
}

type planItemRow struct {
	ID, Slug, Layer, Status, Title, Repo, Path string
}

func planItem(p *pgxpool.Pool, id string) (*planItemRow, error) {
	if id == "" {
		return nil, fmt.Errorf("id fehlt")
	}
	var it planItemRow
	err := p.QueryRow(context.Background(),
		`SELECT id, slug, COALESCE(layer,''), COALESCE(status,''), COALESCE(title,''), repo, path
		 FROM portfolio.plan_item WHERE id=$1`, id).
		Scan(&it.ID, &it.Slug, &it.Layer, &it.Status, &it.Title, &it.Repo, &it.Path)
	if err != nil {
		return nil, fmt.Errorf("plan_item %s nicht gefunden", id)
	}
	// Pfad-Schutz: nur Files unterhalb der registrierten Repo-Wurzel
	if !strings.HasPrefix(it.Path, it.Repo+"/") {
		return nil, fmt.Errorf("pfad außerhalb des repos")
	}
	return &it, nil
}

func gitCommit(repo, path, msg string) {
	add := exec.Command("git", "-C", repo, "add", "--", path)
	_ = add.Run()
	c := exec.Command("git", "-C", repo, "commit", "-m", msg+"\n\nCo-Authored-By: Claude Fable 5 <noreply@anthropic.com>", "--", path)
	if out, err := c.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "git commit %s: %v: %s\n", path, err, out)
	}
}

// callGlm — Z.ai anthropic-compat (gleicher Vertrag wie tools/_lib/glm-client.js in solartown)
func callGlm(system string, messages []map[string]string) (string, error) {
	key := envOr("ZAI_KEY", "")
	if key == "" {
		return "", fmt.Errorf("ZAI_KEY nicht gesetzt (systemd-Unit)")
	}
	payload := map[string]any{
		"model":      envOr("REVIEWER_MODEL", "glm-5.1"),
		"max_tokens": 4096,
		"system":     system,
		"messages":   messages,
	}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", envOr("REVIEWER_BASE_URL", "https://api.z.ai/api/anthropic")+"/v1/messages", bytes.NewReader(b))
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")
	cl := &http.Client{Timeout: 120 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("GLM %d: %.300s", resp.StatusCode, raw)
	}
	var out struct {
		Content []struct{ Type, Text string } `json:"content"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String(), nil
}

func solartownPool() (*pgxpool.Pool, error) {
	if stPool != nil {
		return stPool, nil
	}
	p, err := pgxpool.New(context.Background(),
		envOr("SOLARTOWN_DSN", "postgres://remote:remote@127.0.0.1:5433/solartown_clean?sslmode=disable"))
	if err != nil {
		return nil, err
	}
	stPool = p
	return p, nil
}

func main() {
	root := &cobra.Command{
		Use:   "master-kanban",
		Short: "Master-Kanban CLI — Portfolio-Layer auf mario-brain Postgres",
	}
	root.PersistentFlags().StringVar(&dsn, "dsn", envOr("PORTFOLIO_DSN", "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable"), "Postgres DSN")

	root.AddCommand(cmdList(), cmdAdd(), cmdMove(), cmdLink(), cmdSync(), cmdServe(), cmdEvents(), cmdResolveRepo(), cmdDeployReactor(), cmdCapture())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// actorFrom — SSO-User aus oauth2-proxy (nginx snippets/oauth2-require.conf)
func actorFrom(r *http.Request) string {
	if a := r.Header.Get("X-Auth-Request-Email"); a != "" {
		return a
	}
	return "mario"
}

// checkAuth verifiziert, dass entweder ein gültiger SSO-Header (X-Auth-Request-Email) oder ein gültiger API-Key (X-Api-Key) übergeben wurde.
func checkAuth(r *http.Request) bool {
	if email := r.Header.Get("X-Auth-Request-Email"); email != "" {
		return true
	}
	if key := r.Header.Get("X-Api-Key"); key != "" && key == envOr("PORTFOLIO_API_KEY", "dev-secret") {
		return true
	}
	return false
}

var firmaPrefix = map[string]string{
	"stayawesome": "sa", "solartown": "st", "quantbot": "qb",
	"mariobrain": "mb", "stack": "sk", "angeloos": "ag",
}

func slugify(s string) string {
	s = strings.NewReplacer(
		"ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss").Replace(strings.ToLower(s))
	var b strings.Builder
	prevDash := true
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case !prevDash:
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	if out == "" {
		out = "karte"
	}
	return out
}

func connect() *pgxpool.Pool {
	if pool != nil {
		return pool
	}
	p, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	pool = p
	return p
}

func cmdList() *cobra.Command {
	var firma, stage string
	c := &cobra.Command{
		Use:   "list",
		Short: "Liste alle Initiativen aus initiative_summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := connect()
			q := `SELECT id, firma, stage, title, primary_backend, bead_count, vk_count, pr_count, plan_count, last_activity FROM portfolio.initiative_summary`
			conds := []string{}
			vals := []any{}
			if firma != "" {
				vals = append(vals, firma)
				conds = append(conds, fmt.Sprintf("firma = $%d", len(vals)))
			}
			if stage != "" {
				vals = append(vals, stage)
				conds = append(conds, fmt.Sprintf("stage = $%d", len(vals)))
			}
			if len(conds) > 0 {
				q += " WHERE " + strings.Join(conds, " AND ")
			}
			q += " ORDER BY firma, stage, id"
			rows, err := p.Query(context.Background(), q, vals...)
			if err != nil {
				return err
			}
			defer rows.Close()
			fmt.Printf("%-32s %-12s %-9s %-50s  🐝  🔧  📦  📄  last\n", "id", "firma", "stage", "title")
			for rows.Next() {
				var id, fa, sg, title, pb string
				var bc, vc, pc, plc int
				var la *time.Time
				if err := rows.Scan(&id, &fa, &sg, &title, &pb, &bc, &vc, &pc, &plc, &la); err != nil {
					return err
				}
				laStr := "—"
				if la != nil {
					laStr = la.Format("01-02")
				}
				if len(title) > 50 {
					title = title[:47] + "…"
				}
				fmt.Printf("%-32s %-12s %-9s %-50s  %2d  %2d  %2d  %2d  %s\n", id, fa, sg, title, bc, vc, pc, plc, laStr)
			}
			return rows.Err()
		},
	}
	c.Flags().StringVar(&firma, "firma", "", "Filter firma")
	c.Flags().StringVar(&stage, "stage", "", "Filter stage")
	return c
}

func cmdAdd() *cobra.Command {
	var firma, stage, title, primary string
	c := &cobra.Command{
		Use:   "add <id>",
		Short: "Initiative anlegen",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := connect()
			_, err := p.Exec(context.Background(),
				`INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend) VALUES ($1,$2,$3,$4,$5)`,
				args[0], firma, stage, title, primary)
			if err == nil {
				logEvent(p, args[0], "created", "master", "", stage, fmt.Sprintf(`{"title":"%s"}`, escape(title)))
				fmt.Println("✓ created", args[0])
			}
			return err
		},
	}
	c.Flags().StringVar(&firma, "firma", "", "firma (required)")
	c.Flags().StringVar(&stage, "stage", "idea", "initial stage")
	c.Flags().StringVar(&title, "title", "", "title (required)")
	c.Flags().StringVar(&primary, "primary-backend", "plan_file", "primary backend")
	c.MarkFlagRequired("firma")
	c.MarkFlagRequired("title")
	return c
}

func cmdMove() *cobra.Command {
	return &cobra.Command{
		Use:   "move <id> <stage>",
		Short: "Stage ändern (idea|now|soon|watching|done)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := connect()
			tag, err := p.Exec(context.Background(), `UPDATE portfolio.initiative SET stage = $2, stage_locked_by_human = true WHERE id = 			                      `, args[0], args[1])
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return fmt.Errorf("not found: %s", args[0])
			}
			fmt.Printf("✓ moved %s → %s (trigger logs event automatically)\n", args[0], args[1])
			return nil
		},
	}
}

func cmdLink() *cobra.Command {
	return &cobra.Command{
		Use:   "link <id> <kind> <ref>",
		Short: "Backend-ref linken (kind: bead|vk_workspace|github_pr|plan_file)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := connect()
			_, err := p.Exec(context.Background(),
				`INSERT INTO portfolio.initiative_link (initiative_id, kind, ref) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`,
				args[0], args[1], args[2])
			if err == nil {
				logEvent(p, args[0], "linked", "master", "", "", fmt.Sprintf(`{"kind":"%s","ref":"%s"}`, args[1], escape(args[2])))
				fmt.Printf("✓ linked %s → %s:%s\n", args[0], args[1], args[2])
			}
			return err
		},
	}
}

func cmdSync() *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Pull live counts (placeholder — adapter-layer übernimmt das ab Stage 2.5)",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("master-kanban sync: counts werden vom adapter-layer (Stage 2.5) live in initiative_event geschrieben.")
			fmt.Println("Diese command bleibt als Manual-Trigger / Backfill für initiale Loads.")
			return nil
		},
	}
}

func cmdServe() *cobra.Command {
	var port string
	c := &cobra.Command{
		Use:   "serve",
		Short: "JSON-API für Cockpit-Page (Stage 3)",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := connect()
			http.HandleFunc("/api/initiatives", func(w http.ResponseWriter, r *http.Request) {
				rows, err := p.Query(r.Context(), `SELECT row_to_json(s) FROM portfolio.initiative_summary s ORDER BY firma, stage, id`)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				defer rows.Close()
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Write([]byte("["))
				first := true
				for rows.Next() {
					var j json.RawMessage
					if err := rows.Scan(&j); err != nil {
						continue
					}
					if !first {
						w.Write([]byte(","))
					}
					w.Write(j)
					first = false
				}
				w.Write([]byte("]"))
			})
			// P5 — Karten-Detail (Side-Peek): Initiative + Links + Event-Historie als ein JSON
			http.HandleFunc("/api/initiative", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				id := r.URL.Query().Get("id")
				if id == "" {
					http.Error(w, "id fehlt", 400)
					return
				}
				var exists bool
				if err := p.QueryRow(r.Context(),
					`SELECT EXISTS(SELECT 1 FROM portfolio.initiative_summary WHERE id=$1)`, id).Scan(&exists); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				if !exists {
					http.Error(w, "initiative nicht gefunden: "+id, 404)
					return
				}
				var j json.RawMessage
				err := p.QueryRow(r.Context(), `SELECT json_build_object(
					'initiative', (SELECT row_to_json(s) FROM portfolio.initiative_summary s WHERE s.id=$1),
					'links', COALESCE((SELECT json_agg(row_to_json(l) ORDER BY l.kind, l.added_at)
					                   FROM portfolio.initiative_link l WHERE l.initiative_id=$1), '[]'::json),
					'events', COALESCE((SELECT json_agg(row_to_json(e)) FROM (
					                     SELECT kind, source_backend, from_stage, to_stage, payload, actor, at
					                     FROM portfolio.initiative_event WHERE initiative_id=$1
					                     ORDER BY at DESC LIMIT 40) e), '[]'::json))`, id).Scan(&j)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				w.Write(j)
			})
			// P5 — Kommentar zur Karte: landet als 'commented'-Event in der Historie
			http.HandleFunc("/api/comment", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Methods", "POST,OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				if r.Method == "OPTIONS" {
					return
				}
				if r.Method != "POST" {
					http.Error(w, "POST only", 405)
					return
				}
				var body struct{ Id, Text string }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				body.Text = strings.TrimSpace(body.Text)
				if body.Id == "" || body.Text == "" {
					http.Error(w, "id und text erforderlich", 400)
					return
				}
				if len(body.Text) > 4000 {
					http.Error(w, "text zu lang (max 4000 Zeichen)", 400)
					return
				}
				var exists bool
				if err := p.QueryRow(r.Context(),
					`SELECT EXISTS(SELECT 1 FROM portfolio.initiative WHERE id=$1)`, body.Id).Scan(&exists); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				if !exists {
					http.Error(w, "initiative nicht gefunden: "+body.Id, 404)
					return
				}
				actor := actorFrom(r)
				payload, _ := json.Marshal(map[string]string{"text": body.Text})
				var j json.RawMessage
				err := p.QueryRow(r.Context(),
					`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
					 VALUES ($1,'commented','master',$2,$3)
					 RETURNING json_build_object('kind',kind,'source_backend',source_backend,
					   'from_stage',from_stage,'to_stage',to_stage,'payload',payload,'actor',actor,'at',at)`,
					body.Id, payload, actor).Scan(&j)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(j)
			})
			// P5 — Felder editieren (title/description); Pointer unterscheiden „fehlt" von „leer"
			http.HandleFunc("/api/update", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Methods", "POST,OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				if r.Method == "OPTIONS" {
					return
				}
				if r.Method != "POST" {
					http.Error(w, "POST only", 405)
					return
				}
				var body struct {
					Id          string  `json:"id"`
					Title       *string `json:"title"`
					Description *string `json:"description"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				sets := []string{"updated_at = now()"}
				vals := []any{body.Id}
				fields := []string{}
				if body.Title != nil {
					t := strings.TrimSpace(*body.Title)
					if t == "" {
						http.Error(w, "title darf nicht leer sein", 400)
						return
					}
					vals = append(vals, t)
					sets = append(sets, fmt.Sprintf("title = $%d", len(vals)))
					fields = append(fields, "title")
				}
				if body.Description != nil {
					vals = append(vals, strings.TrimSpace(*body.Description))
					sets = append(sets, fmt.Sprintf("description = NULLIF($%d,'')", len(vals)))
					fields = append(fields, "description")
				}
				if len(fields) == 0 {
					http.Error(w, "nichts zu ändern (title/description)", 400)
					return
				}
				tag, err := p.Exec(r.Context(),
					`UPDATE portfolio.initiative SET `+strings.Join(sets, ", ")+` WHERE id = $1`, vals...)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				if tag.RowsAffected() == 0 {
					http.Error(w, "initiative nicht gefunden: "+body.Id, 404)
					return
				}
				payload, _ := json.Marshal(map[string]any{"fields": fields})
				_, _ = p.Exec(r.Context(),
					`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
					 VALUES ($1,'edited','master',$2,$3)`, body.Id, payload, actorFrom(r))
				fmt.Fprintln(w, `{"ok":true}`)
			})
			// P5 — Karte aus der UI anlegen; id = <firma-prefix>-<slug(title)>
			http.HandleFunc("/api/create", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Methods", "POST,OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				if r.Method == "OPTIONS" {
					return
				}
				if r.Method != "POST" {
					http.Error(w, "POST only", 405)
					return
				}
				var body struct{ Firma, Title, Stage string }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				body.Title = strings.TrimSpace(body.Title)
				prefix, ok := firmaPrefix[body.Firma]
				if !ok || body.Title == "" {
					http.Error(w, "firma und title erforderlich", 400)
					return
				}
				if body.Stage == "" {
					body.Stage = "idea"
				}
				base := prefix + "-" + slugify(body.Title)
				id := base
				for i := 2; ; i++ {
					var exists bool
					if err := p.QueryRow(r.Context(),
						`SELECT EXISTS(SELECT 1 FROM portfolio.initiative WHERE id=$1)`, id).Scan(&exists); err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					if !exists {
						break
					}
					id = fmt.Sprintf("%s-%d", base, i)
				}
				if _, err := p.Exec(r.Context(),
					`INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend)
					 VALUES ($1,$2,$3,$4,'plan_file')`, id, body.Firma, body.Stage, body.Title); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				payload, _ := json.Marshal(map[string]string{"title": body.Title})
				_, _ = p.Exec(r.Context(),
					`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, to_stage, payload, actor)
					 VALUES ($1,'created','master',$2,$3,$4)`, id, body.Stage, payload, actorFrom(r))
				var j json.RawMessage
				if err := p.QueryRow(r.Context(),
					`SELECT row_to_json(s) FROM portfolio.initiative_summary s WHERE s.id=$1`, id).Scan(&j); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(j)
			})
			// P5 — Archivieren: Karte verschwindet vom Board (summary filtert archived_at)
			http.HandleFunc("/api/archive", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Methods", "POST,OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				if r.Method == "OPTIONS" {
					return
				}
				if r.Method != "POST" {
					http.Error(w, "POST only", 405)
					return
				}
				var body struct{ Id string }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				tag, err := p.Exec(r.Context(),
					`UPDATE portfolio.initiative SET archived_at = now(), updated_at = now()
					 WHERE id = $1 AND archived_at IS NULL`, body.Id)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				if tag.RowsAffected() == 0 {
					http.Error(w, "initiative nicht gefunden (oder schon archiviert): "+body.Id, 404)
					return
				}
				_, _ = p.Exec(r.Context(),
					`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, actor)
					 VALUES ($1,'archived','master',$2)`, body.Id, actorFrom(r))
				fmt.Fprintln(w, `{"ok":true}`)
			})
			http.HandleFunc("/api/move", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Methods", "POST,OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
				if r.Method == "OPTIONS" {
					return
				}
				var body struct{ Id, Stage string }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				_, err := p.Exec(r.Context(), `UPDATE portfolio.initiative SET stage = $2, stage_locked_by_human = true WHERE id = 				                              `, body.Id, body.Stage)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				fmt.Fprintln(w, `{"ok":true}`)
			})
			// A6 — Review-Workbench: Dokument lesen
			http.HandleFunc("/api/plan-content", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				it, err := planItem(p, r.URL.Query().Get("id"))
				if err != nil {
					http.Error(w, err.Error(), 404)
					return
				}
				raw, err := os.ReadFile(it.Path)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{
					"id": it.ID, "title": it.Title, "status": it.Status, "layer": it.Layer,
					"repo": it.Repo, "path": it.Path, "markdown": string(raw),
				})
			})

			// A6 — pre-warmed GLM-Reviewer-Chat (Z.ai anthropic-compat)
			http.HandleFunc("/api/review-chat", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				if r.Method != "POST" {
					http.Error(w, "POST only", 405)
					return
				}
				var body struct {
					Id       string              `json:"id"`
					Messages []map[string]string `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				it, err := planItem(p, body.Id)
				if err != nil {
					http.Error(w, err.Error(), 404)
					return
				}
				raw, _ := os.ReadFile(it.Path)
				system := "Du bist der Plan-Reviewer der Solartown Production Lane (Quick-Schicht R1-R5: " +
					"R1 Success-Criteria messbar, R2 Rules/Constraints referenziert, R3 Artefakte existieren, " +
					"R4 Limitations+Decisions dokumentiert, R5 keine Zeitschätzungen). " +
					"Layer dieses Plans: " + it.Layer + ". Sei präzise, deutsch, konstruktiv-kritisch. " +
					"Wenn du konkrete Textänderungen am Dokument vorschlägst, gib sie als Blöcke EXAKT in diesem Format:\n" +
					"```edit\n<<<<<<< SEARCH\n(exakter bestehender Text)\n=======\n(neuer Text)\n>>>>>>> REPLACE\n```\n" +
					"SEARCH muss wörtlich im Dokument vorkommen. Mehrere Blöcke erlaubt.\n\n" +
					"=== DOKUMENT (" + it.Path + ") ===\n" + string(raw)
				msgs := body.Messages
				if len(msgs) == 0 {
					msgs = []map[string]string{{"role": "user", "content": "Führe den Preflight-Review durch: " +
						"Gesamturteil (approve-empfehlung ja/nein), die 3 wichtigsten Schwächen, " +
						"und konkrete Änderungsvorschläge als edit-Blöcke wo sinnvoll."}}
				}
				answer, err := callGlm(system, msgs)
				if err != nil {
					http.Error(w, err.Error(), 502)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"role": "assistant", "content": answer})
			})

			// A6 — Edit-Block übernehmen (SEARCH/REPLACE) + git commit
			http.HandleFunc("/api/plan-edit", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				if r.Method != "POST" {
					http.Error(w, "POST only", 405)
					return
				}
				var body struct{ Id, Search, Replace string }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				it, err := planItem(p, body.Id)
				if err != nil {
					http.Error(w, err.Error(), 404)
					return
				}
				raw, err := os.ReadFile(it.Path)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				var out string
				if body.Search == "" { // Append-Modus (z.B. Constraint-Sektion)
					out = strings.TrimRight(string(raw), "\n") + "\n\n" + body.Replace + "\n"
				} else if !strings.Contains(string(raw), body.Search) {
					http.Error(w, "SEARCH-Text nicht im Dokument gefunden", 409)
					return
				} else {
					out = strings.Replace(string(raw), body.Search, body.Replace, 1)
				}
				if err := os.WriteFile(it.Path, []byte(out), 0644); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				gitCommit(it.Repo, it.Path, "review-workbench: edit "+it.Slug)
				fmt.Fprintln(w, `{"ok":true}`)
			})

			// A6 — Approve: Frontmatter-status flippen + git commit (Adapter zieht Board nach)
			http.HandleFunc("/api/plan-approve", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				if r.Method != "POST" {
					http.Error(w, "POST only", 405)
					return
				}
				var body struct{ Id, Status string }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				if body.Status == "" {
					body.Status = "approved"
				}
				it, err := planItem(p, body.Id)
				if err != nil {
					http.Error(w, err.Error(), 404)
					return
				}
				raw, err := os.ReadFile(it.Path)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				re := regexp.MustCompile(`(?m)^status:\s*.*$`)
				if !re.Match(raw) {
					http.Error(w, "kein status-Feld im Frontmatter", 409)
					return
				}
				out := re.ReplaceAll(raw, []byte("status: "+body.Status))
				if err := os.WriteFile(it.Path, out, 0644); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				gitCommit(it.Repo, it.Path, "review-workbench: "+it.Slug+" → "+body.Status)
				fmt.Fprintln(w, `{"ok":true,"status":"`+body.Status+`"}`)
			})

			// A6/v3 — Git-Tab: Commits + letzter Diff des Plan-Files
			http.HandleFunc("/api/plan-git", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				it, err := planItem(p, r.URL.Query().Get("id"))
				if err != nil {
					http.Error(w, err.Error(), 404)
					return
				}
				logOut, _ := exec.Command("git", "-C", it.Repo, "log", "-10",
					"--pretty=format:%h|%ad|%s", "--date=short", "--", it.Path).Output()
				diffOut, _ := exec.Command("git", "-C", it.Repo, "log", "-1", "-p",
					"--pretty=format:", "--", it.Path).Output()
				type cm struct{ Hash, Date, Msg string }
				var commits []cm
				for _, line := range strings.Split(strings.TrimSpace(string(logOut)), "\n") {
					parts := strings.SplitN(line, "|", 3)
					if len(parts) == 3 {
						commits = append(commits, cm{parts[0], parts[1], parts[2]})
					}
				}
				d := string(diffOut)
				if len(d) > 40000 {
					d = d[:40000] + "\n… (gekürzt)"
				}
				json.NewEncoder(w).Encode(map[string]any{"commits": commits, "diff": d})
			})

			// A6/v3 — Scope-Files-Tab: Datei aus dem Repo des Plans lesen (read-only)
			http.HandleFunc("/api/plan-file", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				it, err := planItem(p, r.URL.Query().Get("id"))
				if err != nil {
					http.Error(w, err.Error(), 404)
					return
				}
				fp := r.URL.Query().Get("path")
				if !strings.HasPrefix(fp, "/") {
					fp = it.Repo + "/" + fp
				}
				clean := strings.ReplaceAll(fp, "/../", "/") // Pfad-Klettern unterbinden
				if !strings.HasPrefix(clean, it.Repo+"/") {
					http.Error(w, "pfad außerhalb des plan-repos", 403)
					return
				}
				raw, err := os.ReadFile(clean)
				if err != nil {
					http.Error(w, err.Error(), 404)
					return
				}
				if len(raw) > 300000 {
					raw = raw[:300000]
				}
				json.NewEncoder(w).Encode(map[string]string{"path": clean, "content": string(raw)})
			})

			// A6 — Workbench-Seite selbst (hinter nginx location /review)
			http.HandleFunc("/review", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				http.ServeFile(w, r, "/var/www/master/review.html")
			})

			// P4.1 — Plan-Baum einer Initiative (oder alle, für Pipeline-View)
			http.HandleFunc("/api/plans", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				q := `SELECT row_to_json(t) FROM (
				        SELECT id, initiative_id, parent_id, slug, layer, status, title, repo, path, updated_at
				        FROM portfolio.plan_item`
				var rows pgxRows
				var err error
				if init := r.URL.Query().Get("initiative"); init != "" {
					q += ` WHERE initiative_id = $1 ORDER BY parent_id NULLS FIRST, id) t`
					rows, err = p.Query(r.Context(), q, init)
				} else {
					q += ` ORDER BY initiative_id, parent_id NULLS FIRST, id) t`
					rows, err = p.Query(r.Context(), q)
				}
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				defer rows.Close()
				writeJSONArray(w, rows)
			})

			// P4.3 — Bead-Rollup pro Plan-Slug (Konvention: label plan:<slug>)
			http.HandleFunc("/api/rollup", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				sp, err := solartownPool()
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				rows, err := sp.Query(r.Context(), `SELECT row_to_json(t) FROM (
					SELECT replace(l.label,'plan:','') AS slug,
					       count(*) AS total,
					       count(*) FILTER (WHERE i.status='closed') AS closed
					FROM beads.labels l JOIN beads.issues i ON i.id=l.issue_id
					WHERE l.label LIKE 'plan:%' AND l.deleted_at IS NULL AND i.deleted_at IS NULL
					GROUP BY l.label) t`)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				defer rows.Close()
				writeJSONArray(w, rows)
			})

			// P2.3 — Backlog: offene Beads ohne lane:*-Label (Solartown-Ledger :5433)
			http.HandleFunc("/api/backlog", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				sp, err := solartownPool()
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				limit := 50
				if l := r.URL.Query().Get("limit"); l != "" {
					fmt.Sscanf(l, "%d", &limit)
				}

				type BacklogItem struct {
					ID                string    `json:"id"`
					Rig               string    `json:"rig"`
					Title             string    `json:"title"`
					IssueType         string    `json:"issue_type"`
					Priority          int       `json:"priority"`
					CreatedAt         time.Time `json:"created_at"`
					PlanCount         int       `json:"plan_count"`
					HasLanePlanSignal bool      `json:"has_lane_plan_signal"`
					Firma             string    `json:"firma"`
				}

				var items []BacklogItem
				rows, err := sp.Query(r.Context(), `
					SELECT i.id, i.rig, i.title, i.issue_type, COALESCE(i.priority, 0), i.created_at
					FROM beads.issues i
					WHERE i.status='open' AND i.deleted_at IS NULL
					  AND COALESCE(i.ephemeral,false)=false
					  AND NOT EXISTS (SELECT 1 FROM beads.labels l
					                  WHERE l.issue_id=i.id AND l.label LIKE 'lane:%' AND l.deleted_at IS NULL)
					ORDER BY i.created_at DESC LIMIT $1`, limit)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				defer rows.Close()

				for rows.Next() {
					var item BacklogItem
					if err := rows.Scan(&item.ID, &item.Rig, &item.Title, &item.IssueType, &item.Priority, &item.CreatedAt); err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					items = append(items, item)
				}

				if len(items) > 0 {
					ids := make([]string, len(items))
					for i, item := range items {
						ids[i] = item.ID
					}

					hasSignalMap := make(map[string]bool)
					signalRows, err := sp.Query(r.Context(), `
						SELECT DISTINCT issue_id FROM beads.labels
						WHERE label = 'lane:plan' AND issue_id = ANY($1)`, ids)
					if err == nil {
						defer signalRows.Close()
						for signalRows.Next() {
							var issueID string
							if err := signalRows.Scan(&issueID); err == nil {
								hasSignalMap[issueID] = true
							}
						}
					}

					type initInfo struct {
						Firma     string
						PlanCount int
					}
					initMap := make(map[string]initInfo)
					if pool != nil {
						portfolioRows, err := pool.Query(r.Context(), `
							SELECT l.ref AS bead_id, i.firma,
							       COALESCE((SELECT count(*) FROM portfolio.initiative_link pl WHERE pl.initiative_id = i.id AND pl.kind = 'plan_file'), 0) AS plan_count
							FROM portfolio.initiative_link l
							JOIN portfolio.initiative i ON i.id = l.initiative_id
							WHERE l.kind = 'bead' AND l.ref = ANY($1)`, ids)
						if err == nil {
							defer portfolioRows.Close()
							for portfolioRows.Next() {
								var beadID, firma string
								var planCount int
								if err := portfolioRows.Scan(&beadID, &firma, &planCount); err == nil {
									initMap[beadID] = initInfo{
										Firma:     firma,
										PlanCount: planCount,
									}
								}
							}
						}
					}

					for i := range items {
						item := &items[i]
						item.HasLanePlanSignal = hasSignalMap[item.ID]
						if info, ok := initMap[item.ID]; ok {
							item.Firma = info.Firma
							item.PlanCount = info.PlanCount
						} else {
							prefix := ""
							if parts := strings.Split(item.ID, "-"); len(parts) > 0 {
								prefix = parts[0]
							}
							firma := ""
							switch prefix {
							case "sa":
								firma = "stayawesome"
							case "st":
								firma = "solartown"
							case "qb":
								firma = "quantbot"
							case "mb":
								firma = "mariobrain"
							case "ag", "sk":
								firma = "stack"
							default:
								switch item.Rig {
								case "stayawesomeOS":
									firma = "stayawesome"
								case "testrig":
									firma = "solartown"
								case "quantumshift":
									firma = "quantbot"
								case "mariobrain":
									firma = "mariobrain"
								case "stack":
									firma = "stack"
								case "clean":
									firma = "angeloos"
								}
							}
							item.Firma = firma
							item.PlanCount = 0
						}
					}
				}

				if items == nil {
					items = []BacklogItem{}
				}
				json.NewEncoder(w).Encode(items)
			})

			// P2.3 — Triage / Dispatch: lane:hacker | lane:plan | lane:human als Label setzen (secured with SSO/API-Key)
			dispatchHandler := func(w http.ResponseWriter, r *http.Request) {
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
				if !checkAuth(r) {
					http.Error(w, "unauthorized", 401)
					return
				}
				var body struct{ Id, Lane string }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				if body.Lane == "hack" {
					body.Lane = "hacker"
				}
				if body.Lane != "hacker" && body.Lane != "plan" && body.Lane != "plan+deep-tech" && body.Lane != "plan+deep-business" && body.Lane != "human" {
					http.Error(w, "lane must be hacker|plan|plan+deep-tech|plan+deep-business|human", 400)
					return
				}
				sp, err := solartownPool()
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				var rig string
				if err := sp.QueryRow(r.Context(),
					`SELECT rig FROM beads.issues WHERE id=$1 AND deleted_at IS NULL`, body.Id).Scan(&rig); err != nil {
					http.Error(w, "bead not found: "+body.Id, 404)
					return
				}
				// Re-Triage: alte lane:*-Labels soft-deleten, neues setzen
				if _, err := sp.Exec(r.Context(),
					`UPDATE beads.labels SET deleted_at=now()
					 WHERE issue_id=$1 AND label LIKE 'lane:%' AND deleted_at IS NULL AND label <> 'lane:'||$2`,
					body.Id, body.Lane); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				if _, err := sp.Exec(r.Context(),
					`INSERT INTO beads.labels (issue_id, rig, label) VALUES ($1,$2,'lane:'||$3)
					 ON CONFLICT (issue_id, label) DO UPDATE SET deleted_at=NULL`,
					body.Id, rig, body.Lane); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}

				// If bead is linked to an initiative, log a 'dispatched' event in portfolio.initiative_event
				var initiativeID string
				err = p.QueryRow(r.Context(),
					`SELECT initiative_id FROM portfolio.initiative_link WHERE kind='bead' AND ref=$1`,
					body.Id).Scan(&initiativeID)
				if err == nil && initiativeID != "" {
					payloadBytes, _ := json.Marshal(map[string]any{
						"lane":    body.Lane,
						"bead_id": body.Id,
						"note":    "Bead " + body.Id + " triaged to lane: " + body.Lane,
					})
					_, _ = p.Exec(r.Context(),
						`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
						 VALUES ($1, 'dispatched', 'bead', $2, $3)`,
						initiativeID, payloadBytes, actorFrom(r))
				}

				fmt.Fprintln(w, `{"ok":true}`)
			}

			http.HandleFunc("/api/triage", dispatchHandler)

			// P2.2 — Kapazitätsanzeige je Lane
			http.HandleFunc("/api/capacity", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				sp, err := solartownPool()
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}

				firmaRig := map[string]string{
					"stayawesome": "stayawesomeOS",
					"solartown":   "testrig",
					"quantbot":    "quantumshift",
					"stack":       "stack",
					"angeloos":    "clean",
					"mariobrain":  "mariobrain",
				}

				type capData struct {
					Polecats int `json:"polecats"`
					VKSlots  int `json:"vkslots"`
				}
				res := make(map[string]capData)

				q := `WITH all_polecats AS (
					SELECT i.id, SUBSTRING(i.id FROM '-polecat-(.+)$') AS name
					FROM beads.issues i
					JOIN beads.labels l ON l.issue_id=i.id AND l.rig=i.rig AND l.label='gt:agent'
					LEFT JOIN beads.labels lm ON lm.issue_id=i.id AND lm.rig=i.rig AND lm.label LIKE 'mode:%' AND lm.deleted_at IS NULL
					WHERE i.rig=$1 AND i.id LIKE $2 AND (lm.label = $3 OR (lm.label IS NULL AND $4 = 'production'))
				),
				busy_assignees AS (
					SELECT DISTINCT assignee FROM beads.issues
					WHERE rig=$1 AND status IN ('in_progress','hooked') AND title NOT LIKE 'Merge:%' AND assignee LIKE $5
				)
				SELECT name FROM all_polecats
				WHERE name IS NOT NULL AND name NOT LIKE '%reviewer%'
				AND ($1 || '/polecats/' || name) NOT IN (SELECT assignee FROM busy_assignees)
				AND ($1 || '/' || name) NOT IN (SELECT assignee FROM busy_assignees)
				ORDER BY name`

				for fID, rig := range firmaRig {
					rows, err := sp.Query(r.Context(), q, rig, "%-"+rig+"-polecat-%", "mode:production", "production", rig+"/%")
					if err != nil {
						continue
					}

					idle := 0
					for rows.Next() {
						var name string
						if err := rows.Scan(&name); err == nil {
							if _, err := os.Stat("/root/solartown/" + rig + "/polecats/" + name); err == nil {
								idle++
							}
						}
					}
					rows.Close()

					var vkCount int
					sp.QueryRow(r.Context(), "SELECT count(*) FROM beads.issues WHERE rig=$1 AND status='hooked' AND assignee LIKE 'vk/%'", rig).Scan(&vkCount)

					slots := 5 - vkCount

					res[fID] = capData{Polecats: idle, VKSlots: slots}
				}

				json.NewEncoder(w).Encode(res)
			})

			http.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Methods", "POST,OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Api-Key")
				if r.Method == "OPTIONS" {
					return
				}
				if r.Method != "POST" {
					http.Error(w, "POST only", 405)
					return
				}
				key := r.Header.Get("X-Api-Key")
				if key == "" || key != envOr("PORTFOLIO_API_KEY", "dev-secret") {
					http.Error(w, "unauthorized", 401)
					return
				}
				var ev struct {
					InitiativeId  string          `json:"initiative_id"`
					Kind          string          `json:"kind"`
					SourceBackend string          `json:"source_backend"`
					Payload       json.RawMessage `json:"payload"`
					Actor         string          `json:"actor"`
				}
				if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				_, err := p.Exec(r.Context(),
					`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor) VALUES ($1,$2,$3,$4,$5)`,
					ev.InitiativeId, ev.Kind, ev.SourceBackend, ev.Payload, ev.Actor)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}

				if ev.Kind == "stage_proposed" {
					var pLoad struct {
						Stage string `json:"stage"`
					}
					if err := json.Unmarshal(ev.Payload, &pLoad); err == nil && pLoad.Stage != "" {
						var currentStage string
						var locked bool
						if err := p.QueryRow(r.Context(), `SELECT stage, COALESCE(stage_locked_by_human, false) FROM portfolio.initiative WHERE id=$1`, ev.InitiativeId).Scan(&currentStage, &locked); err == nil {
							stageRank := map[string]int{"idea": 0, "soon": 1, "now": 2, "watching": 3, "done": 4}
							if !locked && stageRank[pLoad.Stage] > stageRank[currentStage] {
								_, _ = p.Exec(r.Context(), `UPDATE portfolio.initiative SET stage=$2 WHERE id=$1`, ev.InitiativeId, pLoad.Stage)
							}
						}
					}
				}

				fmt.Fprintln(w, `{"ok":true}`)
			})
			// GitHub-Webhook (Org angelosystems): HMAC-verifiziert, mappt
			// pull_request-Events auf initiative_link kind=github_pr mit
			// ref-Konvention owner/repo#N. Edge-triggered statt Polling.
			// nginx routet diesen Pfad am SSO vorbei — Auth ist die Signatur.
			http.HandleFunc("/api/github-webhook", func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					http.Error(w, "POST only", 405)
					return
				}
				secret := envOr("GITHUB_WEBHOOK_SECRET", "")
				if secret == "" {
					http.Error(w, "webhook nicht konfiguriert", 503)
					return
				}
				raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
				if err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				mac := hmac.New(sha256.New, []byte(secret))
				mac.Write(raw)
				want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
				if !hmac.Equal([]byte(r.Header.Get("X-Hub-Signature-256")), []byte(want)) {
					http.Error(w, "bad signature", 401)
					return
				}
				if r.Header.Get("X-GitHub-Event") != "pull_request" {
					fmt.Fprintln(w, `{"ok":true,"skipped":"event"}`)
					return
				}
				var ev struct {
					Action      string `json:"action"`
					PullRequest struct {
						Number int    `json:"number"`
						Title  string `json:"title"`
						Merged bool   `json:"merged"`
						State  string `json:"state"`
					} `json:"pull_request"`
					Repository struct {
						FullName string `json:"full_name"`
					} `json:"repository"`
				}
				if err := json.Unmarshal(raw, &ev); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				ref := fmt.Sprintf("%s#%d", ev.Repository.FullName, ev.PullRequest.Number)
				rows, err := p.Query(r.Context(),
					`SELECT initiative_id FROM portfolio.initiative_link WHERE kind='github_pr' AND ref=$1`, ref)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				var ids []string
				for rows.Next() {
					var id string
					if rows.Scan(&id) == nil {
						ids = append(ids, id)
					}
				}
				rows.Close()
				kind := "activity"
				if ev.Action == "closed" && ev.PullRequest.Merged {
					kind = "completed"
				}
				payload, _ := json.Marshal(map[string]any{
					"ref": ref, "action": ev.Action, "pr_state": ev.PullRequest.State,
					"merged": ev.PullRequest.Merged, "title": ev.PullRequest.Title,
				})
				matched := 0
				for _, id := range ids {
					if _, err := p.Exec(r.Context(),
						`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor) VALUES ($1,$2,'github',$3,'github-webhook')`,
						id, kind, payload); err == nil {
						matched++
					}
				}
				fmt.Fprintf(w, `{"ok":true,"matched":%d}`+"\n", matched)
			})
			// /api/version - Version und SHA-Endpoint für das CD-Gate/Health-Check (SC1)
			http.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				json.NewEncoder(w).Encode(map[string]string{"version": Version})
			})
			// P2 — Dispatch aus der Karte (st-bopm)
			http.HandleFunc("/api/dispatch", handleDispatch(p))
			fmt.Println("master-kanban serve auf :" + port)
			fmt.Println("  GET  /api/initiatives  — initiative_summary VIEW")
			fmt.Println("  GET  /api/initiative   — Karten-Detail (?id=…)")
			fmt.Println("  POST /api/move         — {id, stage}")
			fmt.Println("  POST /api/events       — Adapter-Endpoint (X-Api-Key)")
			fmt.Println("  POST /api/github-webhook — GitHub pull_request (HMAC)")
			return http.ListenAndServe(":"+port, nil)
		},
	}
	c.Flags().StringVar(&port, "port", "7770", "HTTP port")
	return c
}

func cmdEvents() *cobra.Command {
	var initiative string
	var limit int
	c := &cobra.Command{
		Use:   "events",
		Short: "Letzte Events anzeigen",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := connect()
			q := `SELECT initiative_id, kind, source_backend, COALESCE(actor,''), at FROM portfolio.initiative_event`
			vals := []any{}
			if initiative != "" {
				vals = append(vals, initiative)
				q += " WHERE initiative_id = $1"
			}
			vals = append(vals, limit)
			q += fmt.Sprintf(" ORDER BY at DESC LIMIT $%d", len(vals))
			rows, err := p.Query(context.Background(), q, vals...)
			if err != nil {
				return err
			}
			defer rows.Close()
			fmt.Printf("%-32s %-18s %-10s %-15s %s\n", "initiative", "kind", "source", "actor", "at")
			for rows.Next() {
				var iid, kind, src, actor string
				var at time.Time
				if err := rows.Scan(&iid, &kind, &src, &actor, &at); err != nil {
					return err
				}
				fmt.Printf("%-32s %-18s %-10s %-15s %s\n", iid, kind, src, actor, at.Format("2006-01-02 15:04:05"))
			}
			return rows.Err()
		},
	}
	c.Flags().StringVar(&initiative, "initiative", "", "Filter initiative_id")
	c.Flags().IntVar(&limit, "limit", 30, "Max rows")
	return c
}

func logEvent(p *pgxpool.Pool, id, kind, src, from, to, payload string) {
	_, _ = p.Exec(context.Background(),
		`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, from_stage, to_stage, payload, actor) VALUES ($1,$2,$3, NULLIF($4,''), NULLIF($5,''), NULLIF($6,'')::jsonb, $7)`,
		id, kind, src, from, to, payload, "cli")
}

func escape(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }

// rigTownMap maps a company (firma) to its standard local git repository root path.
var rigTownMap = map[string]string{
	"stayawesome": "/root/stayawesomeOS",
	"quantbot":    "/opt/quantbot",
	"solartown":   "/root/solartown",
	"mariobrain":  "/root/mario-brain",
	"angeloos":    "/opt/stack",
	"stack":       "/opt/stack",
}

// getReposMap returns:
// 1. A map of company name to repository path (merging PLANFILE_REPOS environment variables and rigTownMap defaults).
// 2. A slice of all unique known repository paths sorted by length descending (longest first) to allow accurate prefix matching.
func getReposMap() (map[string]string, []string) {
	repos := make(map[string]string)
	// Seed with default rigTownMap
	for k, v := range rigTownMap {
		repos[k] = v
	}

	// Override or supplement with PLANFILE_REPOS if present
	spec := os.Getenv("PLANFILE_REPOS")
	if spec != "" {
		for _, part := range strings.Split(spec, ",") {
			kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
			if len(kv) == 2 {
				repoPath := strings.TrimSpace(kv[0])
				firma := strings.TrimSpace(kv[1])
				if repoPath != "" && firma != "" {
					repos[firma] = repoPath
				}
			}
		}
	}

	// Extract unique paths and sort by length descending to match longest prefix first
	pathMap := make(map[string]bool)
	var paths []string
	for _, p := range repos {
		if !pathMap[p] {
			pathMap[p] = true
			paths = append(paths, p)
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		return len(paths[i]) > len(paths[j])
	})

	return repos, paths
}

// resolveTargetRepo derives the target repository path for an initiative based on its primary backend,
// its links (specifically kind=plan_file paths), and falls back to company-to-repo maps (rig-town-map).
//
// R-B Klärung: Der Fallback über die firma→repo-Map (rig-town-map) ist vollkommen ausreichend,
// da reine Ideen-Karten noch keine zugeordneten Plan-Dateien auf der Platte haben.
// Sobald die Karte konkretisiert wird (z.B. durch Anlegen eines PRDs), wird ein plan_file-Link
// erzeugt, welcher dann primär für die exakte Repo-Ableitung herangezogen wird.
func resolveTargetRepo(p *pgxpool.Pool, id string) (string, error) {
	if id == "" {
		return "", fmt.Errorf("id darf nicht leer sein")
	}

	// 1. Fetch initiative info
	var info struct {
		ID             string
		Firma          string
		PrimaryBackend string
	}
	err := p.QueryRow(context.Background(),
		`SELECT id, firma, COALESCE(primary_backend, '') FROM portfolio.initiative WHERE id = $1`, id).
		Scan(&info.ID, &info.Firma, &info.PrimaryBackend)
	if err != nil {
		return "", fmt.Errorf("initiative %s nicht gefunden: %w", id, err)
	}

	// 2. Fetch linked plan files
	rows, err := p.Query(context.Background(),
		`SELECT ref FROM portfolio.initiative_link WHERE initiative_id = $1 AND kind = 'plan_file' ORDER BY added_at DESC`, id)
	if err != nil {
		return "", fmt.Errorf("fehler beim laden der plan_file links für %s: %w", id, err)
	}
	defer rows.Close()

	var planPaths []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err == nil && ref != "" {
			planPaths = append(planPaths, ref)
		}
	}

	reposMap, knownPaths := getReposMap()

	// 3. Resolve repository from plan paths if any exist
	var matchedRepo string
	for _, planPath := range planPaths {
		for _, rPath := range knownPaths {
			if strings.HasPrefix(planPath, rPath+"/") || planPath == rPath {
				matchedRepo = rPath
				break
			}
		}
		if matchedRepo != "" {
			break
		}
	}

	// 4. Fallback to company-to-repo mapping (rig-town-map)
	if matchedRepo == "" {
		var ok bool
		matchedRepo, ok = reposMap[info.Firma]
		if !ok {
			return "", fmt.Errorf("kein Repository für Firma %s in rig-town-map konfiguriert", info.Firma)
		}
	}

	return matchedRepo, nil
}

// cmdResolveRepo provides a command-line interface to resolve the target repository.
func cmdResolveRepo() *cobra.Command {
	c := &cobra.Command{
		Use:   "resolve-repo <initiative-id>",
		Short: "Leitet das Ziel-Repository für eine Initiative ab",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := connect()
			repo, err := resolveTargetRepo(p, args[0])
			if err != nil {
				return err
			}
			fmt.Printf("Ziel-Repo für %s: %s\n", args[0], repo)
			return nil
		},
	}
	return c
}

func handleDispatch(p *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			return
		}
		if r.Method != "POST" {
			http.Error(w, "POST only", 405)
			return
		}

		var body struct {
			Id   string `json:"id"`
			Lane string `json:"lane"`
			Note string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		body.Id = strings.TrimSpace(body.Id)
		body.Lane = strings.TrimSpace(body.Lane)
		if body.Id == "" || (body.Lane != "plan" && body.Lane != "plan-deep" && body.Lane != "hack") {
			http.Error(w, "id und gültige lane (plan, plan-deep oder hack) erforderlich", 400)
			return
		}

		// Fetch initiative metadata
		var info struct {
			ID             string
			Firma          string
			Title          string
			Description    string
			PrimaryBackend string
		}
		err := p.QueryRow(r.Context(),
			`SELECT id, firma, title, COALESCE(description, ''), COALESCE(primary_backend, '')
			 FROM portfolio.initiative WHERE id = $1`, body.Id).
			Scan(&info.ID, &info.Firma, &info.Title, &info.Description, &info.PrimaryBackend)
		if err != nil {
			http.Error(w, "initiative nicht gefunden: "+body.Id, 404)
			return
		}

		var canonicalRef, filePath string
		if body.Lane == "plan" || body.Lane == "plan-deep" {
			// Resolve target repository
			repo, err := resolveTargetRepo(p, body.Id)
			if err != nil {
				http.Error(w, "fehler beim ermitteln des ziel-repos: "+err.Error(), 500)
				return
			}

			// Get prefix and slug
			prefix, ok := firmaPrefix[info.Firma]
			var slug string
			if ok && strings.HasPrefix(info.ID, prefix+"-") {
				slug = strings.TrimPrefix(info.ID, prefix+"-")
			} else {
				slug = info.ID
			}

			// Map /opt/stack to current worktree
			mappedRepo := repo
			if repo == "/opt/stack" {
				mappedRepo = "/root/solartown/stack/polecats/obsidian/stack"
			}

			// Create PRD-Scaffold directory
			dirPath := filepath.Join(mappedRepo, "docs", "plans")
			if err := os.MkdirAll(dirPath, 0755); err != nil {
				http.Error(w, "mkdir failed: "+err.Error(), 500)
				return
			}

			fileName := slug + "-prd.md"
			filePath = filepath.Join(dirPath, fileName)
			canonicalRef = filepath.Join(repo, "docs", "plans", fileName)

			// Determine review.deep
			reviewDeep := "none"
			if body.Lane == "plan-deep" {
				reviewDeep = "spec-panel"
			}

			var panelBlock string
			if reviewDeep == "spec-panel" {
				panelBlock = "  panel-mode: critique\n  panel-focus: [requirements, architecture]\n"
			}

			scaffold := fmt.Sprintf(`---
title: %s
slug: %s
status: draft
layer: prd
parent_plan: null
scope: %s
created: %s
review:
  quick: auto
  deep: %s
%sreferences:
  - docs/plans/master-kanban.md
---

# PRD: %s

## Why

%s

## Goal

- Feature 1
- Feature 2

## Anforderungen

### R1 - Core Flow
TBD

## Arbeitspakete

### Phase 1 - Prototype
TBD
`, info.Title, slug, info.Description, time.Now().Format("2006-01-02"), reviewDeep, panelBlock, info.Title, info.Description)

			// Write scaffold if it does not exist (idempotent)
			if _, err := os.Stat(filePath); os.IsNotExist(err) {
				if err := os.WriteFile(filePath, []byte(scaffold), 0644); err != nil {
					http.Error(w, "schreiben des prd-scaffolds fehlgeschlagen: "+err.Error(), 500)
					return
				}
			}

			// Link the plan file in portfolio.initiative_link
			_, err = p.Exec(r.Context(),
				`INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
				 VALUES ($1, 'plan_file', $2)
				 ON CONFLICT (initiative_id, kind, ref) DO NOTHING`, info.ID, canonicalRef)
			if err != nil {
				http.Error(w, "verlinken der plan-datei fehlgeschlagen: "+err.Error(), 500)
				return
			}
		}

		// Log event (kind=dispatched, source_backend=plan_file)
		payloadMap := map[string]any{
			"lane": body.Lane,
			"note": body.Note,
		}
		if canonicalRef != "" {
			payloadMap["ref"] = canonicalRef
		}
		payloadBytes, _ := json.Marshal(payloadMap)
		_, err = p.Exec(r.Context(),
			`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
			 VALUES ($1, 'dispatched', 'plan_file', $2, $3)`,
			info.ID, payloadBytes, actorFrom(r))
		if err != nil {
			http.Error(w, "schreiben des events fehlgeschlagen: "+err.Error(), 500)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		respData := map[string]any{
			"ok": true,
		}
		if canonicalRef != "" {
			respData["ref"] = canonicalRef
		}
		if filePath != "" {
			respData["path"] = filePath
		}
		json.NewEncoder(w).Encode(respData)
	}
}

func normalizeFirma(f string) string {
	f = strings.ToLower(strings.TrimSpace(f))
	if _, ok := firmaPrefix[f]; ok {
		return f
	}
	for k, v := range firmaPrefix {
		if f == v {
			return k
		}
	}
	return f
}

func guessFirmaFromCWD() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	cwd = strings.ToLower(cwd)
	if strings.Contains(cwd, "solartown/stack") || strings.Contains(cwd, "polecats/flint/stack") || strings.Contains(cwd, "polecats/") {
		return "stack"
	}
	if strings.Contains(cwd, "stayawesome") {
		return "stayawesome"
	}
	if strings.Contains(cwd, "quantbot") {
		return "quantbot"
	}
	if strings.Contains(cwd, "solartown") {
		return "solartown"
	}
	if strings.Contains(cwd, "mariobrain") {
		return "mariobrain"
	}
	if strings.Contains(cwd, "angeloos") {
		return "angeloos"
	}
	return ""
}

func cmdCapture() *cobra.Command {
	var firma string
	c := &cobra.Command{
		Use:   "capture <text>",
		Short: "Hängt eine Inline-Aktion mit einem Befehl als Event an die passende oder die Catch-all-Initiative",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := args[0]
			p := connect()

			// 1. Fetch all active non-archived initiative IDs
			rows, err := p.Query(context.Background(), `SELECT id, firma FROM portfolio.initiative WHERE archived_at IS NULL`)
			if err != nil {
				return fmt.Errorf("fehler beim laden der initiativen: %w", err)
			}
			defer rows.Close()

			type matchCandidate struct {
				key string
				id  string
			}
			var candidates []matchCandidate

			for rows.Next() {
				var id, f string
				if err := rows.Scan(&id, &f); err != nil {
					return err
				}
				// Add full ID as match key
				candidates = append(candidates, matchCandidate{key: id, id: id})
				// Add slug (without company prefix) as match key
				if parts := strings.SplitN(id, "-", 2); len(parts) == 2 {
					candidates = append(candidates, matchCandidate{key: parts[1], id: id})
				}
			}
			rows.Close()

			// Sort match keys by length descending to match the most specific candidate first
			sort.Slice(candidates, func(i, j int) bool {
				return len(candidates[i].key) > len(candidates[j].key)
			})

			// 2. Look for matching initiative in the text
			var matchedID string
			textLower := strings.ToLower(text)
			for _, cand := range candidates {
				if strings.Contains(textLower, strings.ToLower(cand.key)) {
					matchedID = cand.id
					break
				}
			}

			// 3. Fallback to catch-all if no specific initiative matched
			if matchedID == "" {
				targetFirma := normalizeFirma(firma)
				if targetFirma == "" {
					targetFirma = normalizeFirma(guessFirmaFromCWD())
				}
				if targetFirma == "" {
					targetFirma = "solartown" // default fallback
				}

				// Standard catch-all ID for the given firma prefix
				prefix := firmaPrefix[targetFirma]
				var exists bool
				if prefix != "" {
					candidateID := prefix + "-catch-all"
					err := p.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM portfolio.initiative WHERE id=$1)`, candidateID).Scan(&exists)
					if err == nil && exists {
						matchedID = candidateID
					}
				}

				// Global fallback searching for any available %-catch-all in the database
				if matchedID == "" {
					var fallbackID string
					err := p.QueryRow(context.Background(), `SELECT id FROM portfolio.initiative WHERE id LIKE '%-catch-all' LIMIT 1`).Scan(&fallbackID)
					if err == nil && fallbackID != "" {
						matchedID = fallbackID
					} else {
						return fmt.Errorf("keine passende Initiative gefunden und keine Catch-all-Initiative in der Datenbank vorhanden")
					}
				}
			}

			// 4. Ensure idempotence (check if an identical 'activity' event exists for this initiative)
			var eventExists bool
			err = p.QueryRow(context.Background(),
				`SELECT EXISTS(
					SELECT 1 FROM portfolio.initiative_event
					WHERE initiative_id = $1
					  AND kind = 'activity'
					  AND source_backend = 'master'
					  AND payload->>'title' = $2
				)`, matchedID, text).Scan(&eventExists)
			if err != nil {
				return fmt.Errorf("fehler beim idempotenz-check: %w", err)
			}

			if eventExists {
				fmt.Printf("✓ Event bereits vorhanden (idempotent übersprungen) für Initiative: %s\n", matchedID)
				return nil
			}

			// 5. Insert event into portfolio.initiative_event
			payload, err := json.Marshal(map[string]any{"title": text})
			if err != nil {
				return fmt.Errorf("fehler beim serialisieren des payloads: %w", err)
			}

			_, err = p.Exec(context.Background(),
				`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
				 VALUES ($1, 'activity', 'master', $2::jsonb, 'cli')`,
				matchedID, string(payload))
			if err != nil {
				return fmt.Errorf("fehler beim schreiben des events: %w", err)
			}

			fmt.Printf("✓ Event erfolgreich erfasst für Initiative: %s\n", matchedID)
			return nil
		},
	}
	c.Flags().StringVarP(&firma, "firma", "f", "", "Firma (stayawesome|solartown|quantbot|mariobrain|angeloos|stack)")
	return c
}

