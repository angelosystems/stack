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
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

var (
	dsn     string
	pool    *pgxpool.Pool
	stPool  *pgxpool.Pool // Solartown-Ledger (read + Triage-Labels)
	qbPool  *pgxpool.Pool // Quantbot-Ledger (read KPI events)
	Version string        = "dev"
)

func quantbotPool() (*pgxpool.Pool, error) {
	if qbPool != nil {
		return qbPool, nil
	}
	p, err := pgxpool.New(context.Background(),
		envOr("QUANTBOT_DSN", "postgres://quantbot@127.0.0.1:54330/quantbot?sslmode=disable"))
	if err != nil {
		return nil, err
	}
	qbPool = p
	return p, nil
}

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

	root.AddCommand(cmdList(), cmdAdd(), cmdMove(), cmdLink(), cmdSync(), cmdServe(), cmdEvents(), cmdResolveRepo(), cmdDeployReactor(), cmdCapture(), cmdMcp(), cmdSage(), cmdFleetParse(), cmdParseTranscripts(), cmdSteward(), cmdFlowManager())

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

func countDanglingWorkspaces() (int, error) {
	vkDB := envOr("VK_DB", "/root/.local/share/vibe-kanban/db.v2.sqlite")
	if _, err := os.Stat(vkDB); os.IsNotExist(err) {
		return 0, nil
	}

	query := `
SELECT COUNT(*) FROM workspaces w
WHERE w.archived = 0
  AND w.id NOT IN (
    SELECT s.workspace_id FROM sessions s
    JOIN execution_processes e ON e.session_id = s.id
    WHERE e.status = 'running'
  )
  AND w.id NOT IN (
    SELECT pr.workspace_id FROM pull_requests pr
    WHERE pr.pr_status = 'open'
  )
  AND w.created_at < datetime('now', '-24 hours');
`

	cmd := exec.Command("sqlite3", "-readonly", vkDB, query)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("sqlite3 failed: %w, output: %s", err, string(out))
	}

	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return 0, nil
	}

	var count int
	_, err = fmt.Sscanf(trimmed, "%d", &count)
	if err != nil {
		return 0, err
	}

	return count, nil
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

func parseTime(val any) time.Time {
	if val == nil {
		return time.Now()
	}
	s, ok := val.(string)
	if !ok {
		return time.Now()
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04:05.999999Z07:00", s)
		if err != nil {
			t, err = time.Parse("2006-01-02 15:04:05.999999-07", s)
			if err != nil {
				return time.Now()
			}
		}
	}
	return t
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

				var items []map[string]any
				for rows.Next() {
					var j []byte
					if err := rows.Scan(&j); err != nil {
						continue
					}
					var item map[string]any
					if err := json.Unmarshal(j, &item); err == nil {
						items = append(items, item)
					}
				}

				// Enrich items with lane information based on linked beads
				if len(items) > 0 {
					// 1. Gather all initiative IDs
					initIDs := make([]string, 0, len(items))
					for _, item := range items {
						if id, ok := item["id"].(string); ok {
							initIDs = append(initIDs, id)
						}
					}

					// 2. Fetch linked beads from portfolio.initiative_link
					initToBeads := make(map[string][]string)
					allBeadIDs := make([]string, 0)

					linkRows, err := p.Query(r.Context(), `
						SELECT initiative_id, ref 
						FROM portfolio.initiative_link 
						WHERE kind = 'bead' AND initiative_id = ANY($1)
					`, initIDs)
					if err == nil {
						defer linkRows.Close()
						for linkRows.Next() {
							var initID, beadID string
							if linkRows.Scan(&initID, &beadID) == nil {
								initToBeads[initID] = append(initToBeads[initID], beadID)
								allBeadIDs = append(allBeadIDs, beadID)
							}
						}
					}

					// 3. Fetch lane labels from beads.labels using solartownPool
					beadLanes := make(map[string]string)
					if len(allBeadIDs) > 0 {
						sp, err := solartownPool()
						if err == nil {
							labelRows, err := sp.Query(r.Context(), `
								SELECT issue_id, label 
								FROM beads.labels 
								WHERE label LIKE 'lane:%' AND deleted_at IS NULL AND issue_id = ANY($1)
							`, allBeadIDs)
							if err == nil {
								defer labelRows.Close()
								for labelRows.Next() {
									var issueID, label string
									if labelRows.Scan(&issueID, &label) == nil {
										laneName := strings.TrimPrefix(label, "lane:")
										beadLanes[issueID] = laneName
									}
								}
							}
						}
					}

					// 4. Fetch status of all verlinked beads in bulk
					beadStatuses := make(map[string]string)
					if len(allBeadIDs) > 0 {
						sp, err := solartownPool()
						if err == nil {
							statusRows, err := sp.Query(r.Context(), `
								SELECT id, status 
								FROM beads.issues 
								WHERE id = ANY($1) AND deleted_at IS NULL
							`, allBeadIDs)
							if err == nil {
								defer statusRows.Close()
								for statusRows.Next() {
									var bID, bStatus string
									if statusRows.Scan(&bID, &bStatus) == nil {
										beadStatuses[bID] = bStatus
									}
								}
							}
						}
					}

					// 5. Fetch latest stage-move events in bulk for Zeit-in-Stage
					stageMoveTimes := make(map[string]time.Time)
					eventRows, err := p.Query(r.Context(), `
						SELECT initiative_id, to_stage, max(at) 
						FROM portfolio.initiative_event 
						WHERE kind = 'moved' AND initiative_id = ANY($1)
						GROUP BY initiative_id, to_stage
					`, initIDs)
					if err == nil {
						defer eventRows.Close()
						for eventRows.Next() {
							var initID, toStage string
							var at time.Time
							if eventRows.Scan(&initID, &toStage, &at) == nil {
								key := initID + ":" + toStage
								stageMoveTimes[key] = at
							}
						}
					}

					// 6. Calculate company WIP counts in 'now'
					wipCounts := make(map[string]int)
					for _, item := range items {
						stage, _ := item["stage"].(string)
						firma, _ := item["firma"].(string)
						if stage == "now" {
							wipCounts[firma]++
						}
					}

					// 7. Calculate majority lane and flow signals for each initiative
					for _, item := range items {
						initID, _ := item["id"].(string)
						beads := initToBeads[initID]

						laneCounts := make(map[string]int)
						for _, beadID := range beads {
							if lane, ok := beadLanes[beadID]; ok {
								laneCounts[lane]++
							}
						}

						majorityLane := "untriagiert"
						maxCount := 0
						for lane, count := range laneCounts {
							if count > maxCount {
								maxCount = count
								majorityLane = lane
							} else if count == maxCount {
								if lane < majorityLane {
									majorityLane = lane
								}
							}
						}
						item["lane"] = majorityLane

						// Calculate the 4 flow signals
						currentStage, _ := item["stage"].(string)
						firma, _ := item["firma"].(string)
						updatedAt := parseTime(item["updated_at"])

						// Signal 1: Zeit-in-Stage
						var entryTime time.Time
						if t, ok := stageMoveTimes[initID+":"+currentStage]; ok {
							entryTime = t
						} else {
							entryTime = updatedAt
						}
						timeInStageDays := time.Since(entryTime).Hours() / 24.0
						if timeInStageDays < 0 {
							timeInStageDays = 0
						}

						// Signal 2: Aktivitäts-Stille
						var lastActivityTime time.Time
						if item["last_activity"] != nil {
							lastActivityTime = parseTime(item["last_activity"])
						} else {
							lastActivityTime = updatedAt
						}
						activityStillnessDays := time.Since(lastActivityTime).Hours() / 24.0
						if activityStillnessDays < 0 {
							activityStillnessDays = 0
						}

						// Signal 3: Bead-Fortschritt
						closedCount := 0
						totalCount := len(beads)
						for _, beadID := range beads {
							if status, ok := beadStatuses[beadID]; ok && status == "closed" {
								closedCount++
							}
						}

						// Signal 4: WIP-vs-Limit
						companyWip := wipCounts[firma]
						companyLimit, _ := getWIPLimits(firma)

						item["flow_signals"] = map[string]any{
							"time_in_stage_days":      timeInStageDays,
							"activity_stillness_days": activityStillnessDays,
							"bead_progress": map[string]any{
								"closed": closedCount,
								"total":  totalCount,
							},
							"wip_vs_limit": map[string]any{
								"wip":   companyWip,
								"limit": companyLimit,
							},
						}
					}
				}

				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				if items == nil {
					items = []map[string]any{}
				}
				json.NewEncoder(w).Encode(items)
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

				var rawObj map[string]any
				if err := json.Unmarshal(j, &rawObj); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}

				// Extract initiative fields
				initMap, _ := rawObj["initiative"].(map[string]any)
				if initMap != nil {
					initID, _ := initMap["id"].(string)
					currentStage, _ := initMap["stage"].(string)
					firma, _ := initMap["firma"].(string)
					updatedAt := parseTime(initMap["updated_at"])

					// 1. Zeit-in-Stage
					var entryTime time.Time
					err := p.QueryRow(r.Context(), `
						SELECT at FROM portfolio.initiative_event 
						WHERE initiative_id = $1 AND kind = 'moved' AND to_stage = $2 
						ORDER BY at DESC LIMIT 1
					`, initID, currentStage).Scan(&entryTime)
					if err != nil {
						entryTime = updatedAt
					}
					timeInStageDays := time.Since(entryTime).Hours() / 24.0
					if timeInStageDays < 0 {
						timeInStageDays = 0
					}

					// 2. Aktivitäts-Stille
					var lastActivityTime time.Time
					if initMap["last_activity"] != nil {
						lastActivityTime = parseTime(initMap["last_activity"])
					} else {
						lastActivityTime = updatedAt
					}
					activityStillnessDays := time.Since(lastActivityTime).Hours() / 24.0
					if activityStillnessDays < 0 {
						activityStillnessDays = 0
					}

					// 3. Bead-Fortschritt
					var beadIDs []string
					if links, ok := rawObj["links"].([]any); ok {
						for _, l := range links {
							if lMap, ok := l.(map[string]any); ok {
								if lMap["kind"] == "bead" {
									if ref, ok := lMap["ref"].(string); ok {
										beadIDs = append(beadIDs, ref)
									}
								}
							}
						}
					}

					beadStatuses := make(map[string]string)
					if len(beadIDs) > 0 {
						sp, err := solartownPool()
						if err == nil {
							statusRows, err := sp.Query(r.Context(), `
								SELECT id, status 
								FROM beads.issues 
								WHERE id = ANY($1) AND deleted_at IS NULL
							`, beadIDs)
							if err == nil {
								defer statusRows.Close()
								for statusRows.Next() {
									var bID, bStatus string
									if statusRows.Scan(&bID, &bStatus) == nil {
										beadStatuses[bID] = bStatus
									}
								}
							}
						}
					}

					closedCount := 0
					totalCount := len(beadIDs)
					for _, bID := range beadIDs {
						if status, ok := beadStatuses[bID]; ok && status == "closed" {
							closedCount++
						}
					}

					// 4. WIP-vs-Limit
					var companyWip int
					_ = p.QueryRow(r.Context(), `
						SELECT count(*) FROM portfolio.initiative 
						WHERE stage = 'now' AND firma = $1 AND archived_at IS NULL
					`, firma).Scan(&companyWip)
					companyLimit, _ := getWIPLimits(firma)

					signals := map[string]any{
						"time_in_stage_days":      timeInStageDays,
						"activity_stillness_days": activityStillnessDays,
						"bead_progress": map[string]any{
							"closed": closedCount,
							"total":  totalCount,
						},
						"wip_vs_limit": map[string]any{
							"wip":   companyWip,
							"limit": companyLimit,
						},
					}

					initMap["flow_signals"] = signals
					rawObj["flow_signals"] = signals
				}

				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				json.NewEncoder(w).Encode(rawObj)
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
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Auth-Request-Email, X-Api-Key")
				if r.Method == "OPTIONS" {
					return
				}
				if !checkAuth(r) {
					http.Error(w, "unauthorized", 401)
					return
				}
				var body struct{ Id, Stage string }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				_, err := p.Exec(r.Context(), `UPDATE portfolio.initiative SET stage = $2, stage_locked_by_human = true WHERE id = $1`, body.Id, body.Stage)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				fmt.Fprintln(w, `{"ok":true}`)
			})
			http.HandleFunc("/api/capture", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Access-Control-Allow-Origin", "*")
				w.Header().Set("Access-Control-Allow-Methods", "POST,OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Auth-Request-Email, X-Api-Key")
				if r.Method == "OPTIONS" {
					return
				}
				if !checkAuth(r) {
					http.Error(w, "unauthorized", 401)
					return
				}
				var body struct {
					Text  string `json:"text"`
					Firma string `json:"firma"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				if body.Text == "" {
					http.Error(w, "text ist erforderlich", 400)
					return
				}

				actor := actorFrom(r)
				matchedID, skipped, err := captureEvent(r.Context(), p, body.Text, body.Firma, actor)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}

				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"ok":         true,
					"matched_id": matchedID,
					"skipped":    skipped,
				})
			})
			http.HandleFunc("/api/copilot/chat", handleCopilotChat(p))
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
					Lane              string    `json:"lane"`
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
						item.Lane = "untriagiert"
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

			// L4 — Capture Completeness Metric
			http.HandleFunc("/api/completeness", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")

				// 1. Get detector status
				var lastRun time.Time
				var status string
				var unreachableRigs []string
				err := p.QueryRow(r.Context(),
					`SELECT last_run, status, unreachable_rigs FROM portfolio.detector_status WHERE id='leak-detector'`).
					Scan(&lastRun, &status, &unreachableRigs)
				if err == nil {
					if !lastRun.IsZero() && time.Since(lastRun) > 5*time.Minute {
						status = "danger"
					}
				} else {
					// Fallback if not run yet or error
					lastRun = time.Time{}
					status = "danger"
					unreachableRigs = []string{}
				}

				// 2. Query bead statistics
				var linkedBeadsRegular, linkedBeadsCatchall, unlinkedBeads int
				_ = p.QueryRow(r.Context(),
					`SELECT COUNT(*) FROM portfolio.initiative_link WHERE kind='bead' AND NOT (initiative_id LIKE '%-catch-all')`).Scan(&linkedBeadsRegular)
				_ = p.QueryRow(r.Context(),
					`SELECT COUNT(*) FROM portfolio.initiative_link WHERE kind='bead' AND initiative_id LIKE '%-catch-all'`).Scan(&linkedBeadsCatchall)
				_ = p.QueryRow(r.Context(),
					`SELECT COUNT(*) FROM portfolio.unlinked_item WHERE kind='bead'`).Scan(&unlinkedBeads)

				// 3. Query workspace statistics
				var linkedWorkspacesRegular, linkedWorkspacesCatchall, unlinkedWorkspaces int
				_ = p.QueryRow(r.Context(),
					`SELECT COUNT(*) FROM portfolio.initiative_link WHERE kind='vk_workspace' AND NOT (initiative_id LIKE '%-catch-all')`).Scan(&linkedWorkspacesRegular)
				_ = p.QueryRow(r.Context(),
					`SELECT COUNT(*) FROM portfolio.initiative_link WHERE kind='vk_workspace' AND initiative_id LIKE '%-catch-all'`).Scan(&linkedWorkspacesCatchall)
				_ = p.QueryRow(r.Context(),
					`SELECT COUNT(*) FROM portfolio.unlinked_item WHERE kind='vk_workspace'`).Scan(&unlinkedWorkspaces)

				// 3b. Query offline/unreachable rig statistics (Denominator Honesty)
				var unlinkedRigs int
				_ = p.QueryRow(r.Context(),
					`SELECT COUNT(*) FROM portfolio.unlinked_item WHERE kind='rig'`).Scan(&unlinkedRigs)

				// 4. Calculate totals (including offline/skipped rigs in denominator for honesty)
				totalBeads := linkedBeadsRegular + linkedBeadsCatchall + unlinkedBeads
				totalWorkspaces := linkedWorkspacesRegular + linkedWorkspacesCatchall + unlinkedWorkspaces
				totalRigs := unlinkedRigs
				totalWorkItems := totalBeads + totalWorkspaces + totalRigs
				linkedWorkItems := (linkedBeadsRegular + linkedBeadsCatchall) + (linkedWorkspacesRegular + linkedWorkspacesCatchall)
				catchallWorkItems := linkedBeadsCatchall + linkedWorkspacesCatchall

				completenessPercentage := 0.0
				if totalWorkItems > 0 {
					completenessPercentage = (float64(linkedWorkItems) / float64(totalWorkItems)) * 100.0
				}

				catchallPercentage := 0.0
				if totalWorkItems > 0 {
					catchallPercentage = (float64(catchallWorkItems) / float64(totalWorkItems)) * 100.0
				}

				response := map[string]any{
					"detector_last_run": lastRun,
					"detector_status":   status,
					"unreachable_rigs":  unreachableRigs,
					"beads": map[string]any{
						"linked_regular":  linkedBeadsRegular,
						"linked_catchall": linkedBeadsCatchall,
						"unlinked":        unlinkedBeads,
						"total":           totalBeads,
					},
					"workspaces": map[string]any{
						"linked_regular":  linkedWorkspacesRegular,
						"linked_catchall": linkedWorkspacesCatchall,
						"unlinked":        unlinkedWorkspaces,
						"total":           totalWorkspaces,
					},
					"rigs": map[string]any{
						"unlinked": unlinkedRigs,
						"total":    totalRigs,
					},
					"total_work_items":        totalWorkItems,
					"linked_work_items":       linkedWorkItems,
					"completeness_percentage": completenessPercentage,
					"catchall_percentage":     catchallPercentage,
				}

				json.NewEncoder(w).Encode(response)
			})

			// L3 — Link an unlinked item to an initiative
			http.HandleFunc("/api/link", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
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
					InitiativeID string `json:"initiative_id"`
					Kind         string `json:"kind"`
					Ref          string `json:"ref"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				if body.InitiativeID == "" || body.Kind == "" || body.Ref == "" {
					http.Error(w, "initiative_id, kind und ref sind Pflichtfelder", 400)
					return
				}

				_, err := p.Exec(r.Context(),
					`INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
					 VALUES ($1, $2, $3)
					 ON CONFLICT (initiative_id, kind, ref) DO NOTHING`,
					body.InitiativeID, body.Kind, body.Ref)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}

				// Log event
				logEvent(p, body.InitiativeID, "linked", "master", "", "", fmt.Sprintf(`{"kind":"%s","ref":"%s"}`, body.Kind, escape(body.Ref)))

				// Remove from unlinked table (cleanup so it disappears immediately from UI)
				_, _ = p.Exec(r.Context(), `DELETE FROM portfolio.unlinked_item WHERE id=$1`, body.Ref)

				fmt.Fprintln(w, `{"ok":true}`)
			})

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

			// P2.1 — Host-Kapazitätsdaten für das Cockpit
			http.HandleFunc("/api/capacity-host", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")
				capData, err := getHostCapacityGo(r.Context())
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				json.NewEncoder(w).Encode(capData)
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

				// Trigger Sage Sweep on incoming events (edge-trigger)
				select {
				case sageSweepChan <- struct{}{}:
				default:
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
				if ev.Kind == "completed" {
					go checkAndMoveToWatching(context.Background(), p, ev.InitiativeId)
				}
				if ev.Kind == "completed" || ev.Kind == "activity" {
					go func() { _ = runSageSweep(p, false, false) }()
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
						if kind == "completed" {
							go checkAndMoveToWatching(context.Background(), p, id)
						}
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
			// /api/unlinked - Unlinked-Lane endpoint showing work-items without initiative-link (Capture-Completeness, L1)
			http.HandleFunc("/api/unlinked", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")

				// Query unlinked items from portfolio.unlinked_item
				rows, err := p.Query(r.Context(),
					`SELECT id, kind, title, firma, rig_prefix, COALESCE(join_key, ''), discovered_at
					 FROM portfolio.unlinked_item
					 ORDER BY discovered_at ASC`)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				defer rows.Close()

				type UnlinkedJSONItem struct {
					ID           string    `json:"id"`
					Kind         string    `json:"kind"`
					Title        string    `json:"title"`
					Firma        string    `json:"firma"`
					RigPrefix    string    `json:"rig_prefix"`
					JoinKey      string    `json:"join_key"`
					DiscoveredAt time.Time `json:"discovered_at"`
				}

				items := []UnlinkedJSONItem{}
				for rows.Next() {
					var item UnlinkedJSONItem
					if err := rows.Scan(&item.ID, &item.Kind, &item.Title, &item.Firma, &item.RigPrefix, &item.JoinKey, &item.DiscoveredAt); err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					items = append(items, item)
				}

				// Query detector status from portfolio.detector_status
				type DetectorStatusJSON struct {
					LastRun         time.Time `json:"last_run"`
					Status          string    `json:"status"`
					UnreachableRigs []string  `json:"unreachable_rigs"`
					ErrorMessage    *string   `json:"error_message"`
				}

				var det DetectorStatusJSON
				err = p.QueryRow(r.Context(),
					`SELECT last_run, status, unreachable_rigs, error_message
					 FROM portfolio.detector_status
					 WHERE id = 'leak-detector'`).
					Scan(&det.LastRun, &det.Status, &det.UnreachableRigs, &det.ErrorMessage)
				if err == nil {
					if !det.LastRun.IsZero() && time.Since(det.LastRun) > 5*time.Minute {
						det.Status = "danger"
					}
				} else {
					// It's possible the status hasn't been written yet or table is empty.
					det.Status = "danger"
					det.UnreachableRigs = []string{}
				}

				var sage DetectorStatusJSON
				err = p.QueryRow(r.Context(),
					`SELECT last_run, status, unreachable_rigs, error_message
					 FROM portfolio.detector_status
					 WHERE id = 'sage'`).
					Scan(&sage.LastRun, &sage.Status, &sage.UnreachableRigs, &sage.ErrorMessage)
				if err == nil {
					if !sage.LastRun.IsZero() && time.Since(sage.LastRun) > 5*time.Minute {
						sage.Status = "danger"
					}
				} else {
					// Fallback if sage status is not in database yet or is missing.
					sage.Status = "danger"
					sage.UnreachableRigs = []string{}
				}

				danglingCount, err := countDanglingWorkspaces()
				if err != nil {
					danglingCount = 0
				}

				response := map[string]any{
					"items":                     items,
					"detector_status":           det,
					"sage_status":               sage,
					"dangling_workspaces_count": danglingCount,
				}

				json.NewEncoder(w).Encode(response)
			})
			// P2 — Dispatch aus der Karte (st-bopm)
			http.HandleFunc("/api/dispatch", handleDispatch(p))

			// Expose WIP limits to cockpit UI (P2.3)
			http.HandleFunc("/api/wip-limits", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")

				limits := map[string]map[string]int{}
				firmas := []string{"stayawesome", "solartown", "quantbot", "mariobrain", "stack", "angeloos"}
				for _, f := range firmas {
					nowLim, soonLim := getWIPLimits(f)
					limits[f] = map[string]int{
						"now":  nowLim,
						"soon": soonLim,
					}
				}
				json.NewEncoder(w).Encode(limits)
			})

			// Expose Flow thresholds to cockpit UI (P1.2)
			http.HandleFunc("/api/flow-thresholds", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")

				thresholds := map[string]map[string]string{}
				firmas := []string{"stayawesome", "solartown", "quantbot", "mariobrain", "stack", "angeloos"}
				stages := []string{"now", "soon", "idea", "watching", "done"}
				for _, f := range firmas {
					thresholds[f] = map[string]string{}
					for _, s := range stages {
						thresholds[f][s] = GetStageThreshold(f, s).String()
					}
				}
				json.NewEncoder(w).Encode(thresholds)
			})

			// POST /api/proposal/accept
			http.HandleFunc("/api/proposal/accept", func(w http.ResponseWriter, r *http.Request) {
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

				tx, err := p.Begin(r.Context())
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				defer tx.Rollback(r.Context())

				var title, firma, statusDot string
				err = tx.QueryRow(r.Context(), `SELECT title, firma, COALESCE(status_dot, '') FROM portfolio.initiative WHERE id = $1`, body.Id).Scan(&title, &firma, &statusDot)
				if err != nil {
					http.Error(w, "Proposal not found: "+err.Error(), 404)
					return
				}

				var pData struct {
					Proposed  bool   `json:"proposed"`
					BeadID    string `json:"bead_id"`
					Reasoning string `json:"reasoning"`
				}
				if err := json.Unmarshal([]byte(statusDot), &pData); err != nil || !pData.Proposed || pData.BeadID == "" {
					http.Error(w, "Invalid proposal status_dot data", 400)
					return
				}

				// 1. Delete proposal
				_, err = tx.Exec(r.Context(), `DELETE FROM portfolio.initiative WHERE id = $1`, body.Id)
				if err != nil {
					http.Error(w, "Failed to delete proposal: "+err.Error(), 500)
					return
				}

				// 2. Insert real initiative card (using bead ID as card ID)
				cardID := pData.BeadID
				_, err = tx.Exec(r.Context(),
					`INSERT INTO portfolio.initiative (id, firma, stage, title, status_dot, primary_backend)
					 VALUES ($1, $2, 'idea', $3, NULL, 'solartown')`,
					cardID, firma, title)
				if err != nil {
					http.Error(w, "Failed to insert initiative: "+err.Error(), 500)
					return
				}

				// 3. Insert initiative link
				_, err = tx.Exec(r.Context(),
					`INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
					 VALUES ($1, 'bead', $2)`,
					cardID, cardID)
				if err != nil {
					http.Error(w, "Failed to insert initiative link: "+err.Error(), 500)
					return
				}

				// Commit transaction so portfolio state is stored
				if err := tx.Commit(r.Context()); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}

				// 4. Set lane:plan on the bead using solartownPool() and similar logic as /api/triage
				sp, err := solartownPool()
				if err != nil {
					http.Error(w, "Failed to connect to solartown: "+err.Error(), 500)
					return
				}

				var rig string
				if err := sp.QueryRow(r.Context(),
					`SELECT rig FROM beads.issues WHERE id=$1 AND deleted_at IS NULL`, cardID).Scan(&rig); err != nil {
					http.Error(w, "Bead not found: "+cardID, 404)
					return
				}

				// Soft delete old lane:*-labels and insert lane:plan
				_, err = sp.Exec(r.Context(),
					`UPDATE beads.labels SET deleted_at=now()
					 WHERE issue_id=$1 AND label LIKE 'lane:%' AND deleted_at IS NULL AND label <> 'lane:plan'`,
					cardID)
				if err != nil {
					http.Error(w, "Failed to clear old lane label: "+err.Error(), 500)
					return
				}

				_, err = sp.Exec(r.Context(),
					`INSERT INTO beads.labels (issue_id, rig, label) VALUES ($1,$2,'lane:plan')
					 ON CONFLICT (issue_id, label) DO UPDATE SET deleted_at=NULL`,
					cardID, rig)
				if err != nil {
					http.Error(w, "Failed to set lane:plan label: "+err.Error(), 500)
					return
				}

				fmt.Fprintln(w, `{"ok":true}`)
			})

			// POST /api/proposal/reject
			http.HandleFunc("/api/proposal/reject", func(w http.ResponseWriter, r *http.Request) {
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
				// Verwerfen löscht spurlos
				_, err := p.Exec(r.Context(), `DELETE FROM portfolio.initiative WHERE id = $1`, body.Id)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				fmt.Fprintln(w, `{"ok":true}`)
			})

			// Start background listeners
			startStageChangeListener(p)
			startProposalAgentListener(p)
			startSageSteward(p)

			// GET /api/sage/status
			http.HandleFunc("/api/sage/status", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")

				var lastRun time.Time
				var status string
				var errMsg *string
				err := p.QueryRow(r.Context(),
					`SELECT last_run, status, error_message FROM portfolio.sage_status WHERE id = 'sage-steward'`).
					Scan(&lastRun, &status, &errMsg)
				if err != nil {
					lastRun = time.Now()
					status = "unknown"
				}

				// If last run is older than 30 seconds, it's an alarm!
				if time.Since(lastRun) > 30*time.Second {
					status = "alarm"
				}

				dangling, err := getDanglingWorkspaces()
				if err != nil {
					dangling = []DanglingWorkspace{}
				}

				resp := map[string]any{
					"last_run":            lastRun,
					"status":              status,
					"error_message":       errMsg,
					"dangling_count":      len(dangling),
					"dangling_baseline":   4,
					"outage_simulated":    sageOutageSimulated,
					"dangling_workspaces": dangling,
				}
				json.NewEncoder(w).Encode(resp)
			})

			// POST /api/sage/handover
			// Defined and implemented handover path Manager -> vk-Sage for workspace-based stagnation (R-D)
			http.HandleFunc("/api/sage/handover", func(w http.ResponseWriter, r *http.Request) {
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
					InitiativeID string `json:"initiative_id"`
					WorkspaceID  string `json:"workspace_id"`
					Reason       string `json:"reason"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}

				if body.InitiativeID == "" || body.WorkspaceID == "" {
					http.Error(w, "missing initiative_id or workspace_id", 400)
					return
				}

				// 1. Log card symptom by writing a 'sage_action' event with action='handover' on the Initiative
				payloadMap := map[string]any{
					"workspace_id":    body.WorkspaceID,
					"action":          "handover",
					"reason":          body.Reason,
					"source":          "manager",
				}
				payloadBytes, err := json.Marshal(payloadMap)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}

				_, err = p.Exec(r.Context(), `
					INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
					VALUES ($1, 'sage_action', 'sage', $2, 'flow-manager')
				`, body.InitiativeID, string(payloadBytes))
				if err != nil {
					http.Error(w, fmt.Sprintf("failed to log handover event: %v", err), 500)
					return
				}

				// 2. Explicitly notify / trigger vk-Sage to run a sweep of the workspace and handle it
				select {
				case sageSweepChan <- struct{}{}:
				default:
				}

				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"ok":true,"handover_status":"received"}`))
			})

			// POST /api/sage/simulate-outage
			http.HandleFunc("/api/sage/simulate-outage", func(w http.ResponseWriter, r *http.Request) {
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
					Simulate bool `json:"simulate"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}

				if body.Simulate {
					sageOutageSimulated = true
					// Back-date last_run to 10 minutes ago to trigger alarm instantly
					_, _ = p.Exec(r.Context(),
						`UPDATE portfolio.sage_status 
						 SET last_run = now() - interval '10 minutes', status = 'alarm' 
						 WHERE id = 'sage-steward'`)
				} else {
					sageOutageSimulated = false
					// Restore healthy status
					_, _ = p.Exec(r.Context(),
						`UPDATE portfolio.sage_status 
						 SET last_run = now(), status = 'healthy', error_message = NULL 
						 WHERE id = 'sage-steward'`)
				}

				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintln(w, `{"ok":true}`)
			})

			// P2.1 — Host-Kapazitätsanzeige (RAM/CPU/Swap/PSI + Headroom + Governor-Verdict + Freeze-Marge)
			http.HandleFunc("/api/host-capacity", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Access-Control-Allow-Origin", "*")

				qbp, err := quantbotPool()
				var dbCPU, dbRAM, dbDisk float64
				var lastUpdated time.Time
				dbConnected := false
				if err == nil {
					dbConnected = true
					var cpuTime, ramTime, diskTime time.Time
					_ = qbp.QueryRow(r.Context(),
						`SELECT (payload->>'value')::float, published_at 
						 FROM public.kpi_events 
						 WHERE owner='infra' AND name='cpu_nuernberg' 
						 ORDER BY id DESC LIMIT 1`).Scan(&dbCPU, &cpuTime)

					_ = qbp.QueryRow(r.Context(),
						`SELECT (payload->>'value')::float, published_at 
						 FROM public.kpi_events 
						 WHERE owner='infra' AND name='ram_nuernberg' 
						 ORDER BY id DESC LIMIT 1`).Scan(&dbRAM, &ramTime)

					_ = qbp.QueryRow(r.Context(),
						`SELECT (payload->>'value')::float, published_at 
						 FROM public.kpi_events 
						 WHERE owner='infra' AND name='disk_nuernberg' 
						 ORDER BY id DESC LIMIT 1`).Scan(&dbDisk, &diskTime)

					if cpuTime.After(ramTime) {
						lastUpdated = cpuTime
					} else {
						lastUpdated = ramTime
					}
					if diskTime.After(lastUpdated) {
						lastUpdated = diskTime
					}
				}

				mem := make(map[string]int64)
				if data, err := os.ReadFile("/proc/meminfo"); err == nil {
					for _, line := range strings.Split(string(data), "\n") {
						parts := strings.SplitN(line, ":", 2)
						if len(parts) == 2 {
							key := strings.TrimSpace(parts[0])
							fields := strings.Fields(parts[1])
							if len(fields) > 0 {
								if val, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
									mem[key] = val
								}
							}
						}
					}
				}

				memTotal := float64(mem["MemTotal"]) / 1024 / 1024
				memAvailable := float64(mem["MemAvailable"]) / 1024 / 1024
				swapTotal := float64(mem["SwapTotal"]) / 1024 / 1024
				swapFree := float64(mem["SwapFree"]) / 1024 / 1024
				swapUsed := swapTotal - swapFree
				swapPct := 0.0
				if swapTotal > 0 {
					swapPct = (swapUsed / swapTotal) * 100
				}

				commitLimit := mem["CommitLimit"]
				if commitLimit == 0 {
					commitLimit = 1
				}
				committedRatio := float64(mem["Committed_AS"]) / float64(commitLimit)
				freezeMarge := (1.0 - committedRatio) * 100

				psi := 0.0
				if data, err := os.ReadFile("/proc/pressure/memory"); err == nil {
					for _, line := range strings.Split(string(data), "\n") {
						if strings.HasPrefix(line, "some") {
							for _, tok := range strings.Fields(line) {
								if strings.HasPrefix(tok, "avg10=") {
									if v, err := strconv.ParseFloat(strings.SplitN(tok, "=", 2)[1], 64); err == nil {
										psi = v
									}
								}
							}
						}
					}
				}

				load1 := 0.0
				if data, err := os.ReadFile("/proc/loadavg"); err == nil {
					fields := strings.Fields(string(data))
					if len(fields) > 0 {
						if v, err := strconv.ParseFloat(fields[0], 64); err == nil {
							load1 = v
						}
					}
				}

				nproc := runtime.NumCPU()

				if !dbConnected || dbCPU == 0 {
					dbCPU = (load1 / float64(nproc)) * 100
					if dbCPU > 100 {
						dbCPU = 100
					}
				}
				if !dbConnected || dbRAM == 0 {
					if memTotal > 0 {
						dbRAM = ((memTotal - memAvailable) / memTotal) * 100
					}
				}

				stressed := false
				var reasons []string
				if load1 > float64(nproc) {
					stressed = true
					reasons = append(reasons, fmt.Sprintf("load %.1f > %d", load1, nproc))
				}
				if memAvailable < 4.0 {
					stressed = true
					reasons = append(reasons, fmt.Sprintf("memavail %.1fG < 4.0G", memAvailable))
				}
				if committedRatio > 0.90 {
					stressed = true
					reasons = append(reasons, fmt.Sprintf("committed %.2f > 0.90", committedRatio))
				}
				if psi > 30.0 {
					stressed = true
					reasons = append(reasons, fmt.Sprintf("psi %.1f > 30.0", psi))
				}

				governorVerdict := "OK"
				if stressed {
					governorVerdict = "STRESS-THROTTLE"
				}

				swapTrend := "stable"
				if swapUsed > 0 && swapPct > 15 {
					swapTrend = "moderate-use"
				}

				cpuHeadroom := 100.0 - dbCPU
				if cpuHeadroom < 0 {
					cpuHeadroom = 0
				}

				secondsAgo := 0
				liveness := "unhealthy"
				if !lastUpdated.IsZero() {
					secondsAgo = int(time.Since(lastUpdated).Seconds())
					if secondsAgo < 60 {
						liveness = "healthy"
					}
				} else {
					lastUpdated = time.Now()
					liveness = "healthy"
				}

				resp := map[string]any{
					"cpu_pct":                  dbCPU,
					"ram_pct":                  dbRAM,
					"disk_pct":                 dbDisk,
					"mem_total_gb":             memTotal,
					"mem_avail_gb":             memAvailable,
					"swap_total_gb":            swapTotal,
					"swap_free_gb":             swapFree,
					"swap_used_gb":             swapUsed,
					"swap_pct":                 swapPct,
					"swap_trend":               swapTrend,
					"psi_mem_some_avg10":       psi,
					"committed_ratio":          committedRatio,
					"freeze_marge":             freezeMarge,
					"load1":                    load1,
					"nproc":                    nproc,
					"cpu_headroom_pct":         cpuHeadroom,
					"governor_verdict":         governorVerdict,
					"governor_reasons":         strings.Join(reasons, ", "),
					"last_updated_seconds_ago": secondsAgo,
					"collector_liveness":       liveness,
				}

				json.NewEncoder(w).Encode(resp)
			})

			// Bootup Catchup — both Pull-Regel and Proposal-Agent
			firmas := []string{"stayawesome", "solartown", "quantbot", "mariobrain", "stack", "angeloos"}
			for _, f := range firmas {
				go checkAndPull(context.Background(), p, f)
			}

			// Catch-up on startup for Vorschlags-Agent
			go func() {
				// Wait a moment for server to start up and connect cleanly
				time.Sleep(2 * time.Second)
				for _, f := range firmas {
					checkFirmaProposals(p, f)
				}
			}()

			fmt.Println("master-kanban serve auf :" + port)
			fmt.Println("  GET  /api/initiatives  — initiative_summary VIEW")
			fmt.Println("  GET  /api/initiative   — Karten-Detail (?id=…)")
			fmt.Println("  GET  /api/unlinked     — List unlinked items and detector status")
			fmt.Println("  POST /api/move         — {id, stage}")
			fmt.Println("  POST /api/events       — Adapter-Endpoint (X-Api-Key)")
			fmt.Println("  POST /api/github-webhook — GitHub pull_request (HMAC)")
			fmt.Println("  GET  /api/wip-limits   — Configurable WIP limits")
			fmt.Println("  GET  /api/flow-thresholds — Configurable flow thresholds")
			fmt.Println("  POST /api/proposal/accept — Accept proposed card")
			fmt.Println("  POST /api/proposal/reject — Reject/delete proposed card")
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

// SPECIFICATION: Dreiräumiger Identity Join Key (Three-space Identity Join Key)
//
// Dieser dreiräumige Join spezifiziert das Mapping entlang der Kette:
// Space 1 (Laufzeit / Session):
//
//	Ein laufender Prozess/Workspace wird eindeutig identifiziert über seine PID und seine CWD (Current Working Directory).
//	Jede ausgeführte Session/Agenten-Session protokolliert Setup- und Stop-Events in das globale Log unter
//	/var/log/vk-sessions.jsonl.
//	Die Brücke [Session-Log-UUID ↔ PID/Workspace] löst den Join über das Feld "workspace_id" (UUID) auf:
//	  PID -> /proc/<PID>/cwd -> Pfad-Präfix (z. B. "1134-sol-st-4aibw") -> Suche in /var/log/vk-sessions.jsonl -> Workspace UUID (R2)
//
// Space 2 (Workspace / Vibe-Kanban):
//
//	Die Workspace-UUID aus dem Session-Log verbindet sich mit dem Vibe-Kanban SQLite-Datenbankschema:
//	  Workspace-UUID -> workspaces Table (hex(id) match) -> Workspace Name (z. B. "sol-st-4aibw") & extrahierter Bead ID (z. B. "st-4aibw").
//
// Space 3 (Master-Kanban / Portfolio):
//
//	Die extrahierte Bead ID verbindet den lokalen Task/Bead mit dem übergeordneten Master-Kanban Board:
//	  - Dolt-Postgres (Port 5433 - beads): Bead ID -> beads.issues.id -> Bead Status (z. B. 'hooked', 'open')
//	  - Board-Postgres (Port 5434 - portfolio): Bead ID -> portfolio.initiative_link (kind='bead', ref=Bead ID) -> initiative_id (Kanban-Karte, R3/R4)
//
// Die 5 Kanban-Slices / Provider (Domain-Objekte des Kanban-Tools für die Ressourcenverteilung):
//
//	Jeder Provider (Firma) repräsentiert einen logischen Ressourcen-Kanal (Domain-Slice) auf dem Board.
//	Die Abbildung von Provider auf das entsprechende Code-Repository und Präfix erfolgt über:
//	  1. "stayawesome" -> /root/stayawesomeOS -> Präfix "sa"
//	  2. "quantbot"    -> /opt/quantbot        -> Präfix "qb"
//	  3. "solartown"   -> /root/solartown       -> Präfix "st"
//	  4. "mariobrain"  -> /root/mario-brain     -> Präfix "mb"
//	  5. "angeloos"    -> /opt/stack            -> Präfix "ag" (mit Fallback/Alias "stack" -> "sk")
//
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

		if body.Lane == "hack" {
			// Determine repo from plan_item or fallback to company default
			var repo string
			_ = p.QueryRow(r.Context(),
				`SELECT repo FROM portfolio.plan_item WHERE initiative_id = $1 LIMIT 1`, body.Id).
				Scan(&repo)

			if repo == "" {
				firmaRepo := map[string]string{
					"stayawesome": "/root/stayawesomeOS",
					"solartown":   "/root/solartown",
					"quantbot":    "/opt/quantbot",
					"mariobrain":  "/root/mario-brain",
					"stack":       "/opt/stack",
				}
				repo = firmaRepo[info.Firma]
			}
			if repo == "" {
				repo = "/root/solartown" // fallback
			}

			// Execute vk-delegate to spawn workspace
			exe := findVkDelegate()

			prompt := body.Note
			if prompt == "" {
				prompt = info.Title
			}

			cmd := exec.Command(exe,
				"--repo", repo,
				"--name", info.Title,
				"--prompt", prompt,
			)

			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			if err := cmd.Run(); err != nil {
				errMsg := fmt.Sprintf("vk-delegate failed: %v, stderr: %s", err, stderr.String())
				http.Error(w, errMsg, 500)
				return
			}

			// Parse workspace_id from stdout
			re := regexp.MustCompile(`workspace_id:\s+([a-f0-9\-]+)`)
			matches := re.FindStringSubmatch(stdout.String())
			if len(matches) < 2 {
				http.Error(w, "failed to parse workspace_id from vk-delegate output: "+stdout.String(), 500)
				return
			}
			wsID := matches[1]

			// Write dispatched event to initiative_event
			payloadBytes, _ := json.Marshal(map[string]string{
				"lane": body.Lane,
				"ref":  wsID,
			})
			_, err = p.Exec(r.Context(),
				`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
				 VALUES ($1, 'dispatched', 'vk', $2::jsonb, 'master-kanban')`,
				body.Id, string(payloadBytes))
			if err != nil {
				http.Error(w, "failed to write initiative_event: "+err.Error(), 500)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"ok":           true,
				"workspace_id": wsID,
			})
			return
		}

		var canonicalRef, filePath string
		if body.Lane == "plan" || body.Lane == "plan-deep" {
			// Check capacity governor for 429 stress admission criterion
			throttled, reason, err := IsProviderStressThrottled(r.Context(), "anthropic")
			if err != nil {
				http.Error(w, "stress check failed: "+err.Error(), 500)
				return
			}
			if throttled {
				http.Error(w, "Admission stress: "+reason, http.StatusTooManyRequests)
				return
			}

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

func captureEvent(ctx context.Context, p *pgxpool.Pool, text string, firma string, actor string) (string, bool, error) {
	// 1. Fetch all active non-archived initiative IDs
	rows, err := p.Query(ctx, `SELECT id, firma FROM portfolio.initiative WHERE archived_at IS NULL`)
	if err != nil {
		return "", false, fmt.Errorf("fehler beim laden der initiativen: %w", err)
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
			return "", false, err
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
			err := p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM portfolio.initiative WHERE id=$1)`, candidateID).Scan(&exists)
			if err == nil && exists {
				matchedID = candidateID
			}
		}

		// Global fallback searching for any available %-catch-all in the database
		if matchedID == "" {
			var fallbackID string
			err := p.QueryRow(ctx, `SELECT id FROM portfolio.initiative WHERE id LIKE '%-catch-all' LIMIT 1`).Scan(&fallbackID)
			if err == nil && fallbackID != "" {
				matchedID = fallbackID
			} else {
				return "", false, fmt.Errorf("keine passende Initiative gefunden und keine Catch-all-Initiative in der Datenbank vorhanden")
			}
		}
	}

	// 4. Ensure idempotence (check if an identical 'activity' event exists for this initiative)
	var eventExists bool
	err = p.QueryRow(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM portfolio.initiative_event
			WHERE initiative_id = $1
			  AND kind = 'activity'
			  AND source_backend = 'master'
			  AND payload->>'title' = $2
		)`, matchedID, text).Scan(&eventExists)
	if err != nil {
		return "", false, fmt.Errorf("fehler beim idempotenz-check: %w", err)
	}

	if eventExists {
		return matchedID, true, nil
	}

	// 5. Insert event into portfolio.initiative_event
	payload, err := json.Marshal(map[string]any{"title": text})
	if err != nil {
		return "", false, fmt.Errorf("fehler beim serialisieren des payloads: %w", err)
	}

	if actor == "" {
		actor = "cli"
	}

	_, err = p.Exec(ctx,
		`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
		 VALUES ($1, 'activity', 'master', $2::jsonb, $3)`,
		matchedID, string(payload), actor)
	if err != nil {
		return "", false, fmt.Errorf("fehler beim schreiben des events: %w", err)
	}

	return matchedID, false, nil
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

			matchedID, skipped, err := captureEvent(context.Background(), p, text, firma, "cli")
			if err != nil {
				return err
			}

			if skipped {
				fmt.Printf("✓ Event bereits vorhanden (idempotent übersprungen) für Initiative: %s\n", matchedID)
			} else {
				fmt.Printf("✓ Event erfolgreich erfasst für Initiative: %s\n", matchedID)
			}
			return nil
		},
	}
	c.Flags().StringVarP(&firma, "firma", "f", "", "Firma (stayawesome|solartown|quantbot|mariobrain|angeloos|stack)")
	return c
}

type StageChangeNotification struct {
	ID    string `json:"id"`
	Firma string `json:"firma"`
	From  string `json:"from"`
	To    string `json:"to"`
}

func getWIPLimits(firma string) (int, int) {
	nowLimit := 3
	soonLimit := 5
	envNow := os.Getenv("PORTFOLIO_WIP_NOW_" + strings.ToUpper(firma))
	if envNow != "" {
		fmt.Sscanf(envNow, "%d", &nowLimit)
	}
	envSoon := os.Getenv("PORTFOLIO_WIP_SOON_" + strings.ToUpper(firma))
	if envSoon != "" {
		fmt.Sscanf(envSoon, "%d", &soonLimit)
	}
	return nowLimit, soonLimit
}

func checkAndMoveToWatching(ctx context.Context, p *pgxpool.Pool, initiativeID string) {
	rows, err := p.Query(ctx, `SELECT ref FROM portfolio.initiative_link WHERE initiative_id=$1 AND kind='bead'`, initiativeID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error querying links: %v\n", err)
		return
	}
	defer rows.Close()
	var beads []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err == nil {
			beads = append(beads, ref)
		}
	}
	if len(beads) == 0 {
		return
	}

	sp, err := solartownPool()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to solartown pool: %v\n", err)
		return
	}
	var openCount int
	err = sp.QueryRow(ctx, `SELECT count(*) FROM beads.issues WHERE id=ANY($1) AND status<>'closed' AND deleted_at IS NULL`, beads).Scan(&openCount)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error counting open beads: %v\n", err)
		return
	}

	if openCount == 0 {
		var exists bool
		_ = p.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM portfolio.initiative_event
				WHERE initiative_id = $1 AND kind = 'sage_action' AND (payload->>'classification') = 'all-beads-closed'
			)
		`, initiativeID).Scan(&exists)
		if !exists {
			payloadBytes, _ := json.Marshal(map[string]any{
				"classification":  "all-beads-closed",
				"proposed_action": "stage-promotion",
				"to_stage":        "watching",
				"reason":          "Alle verknüpften Beads geschlossen (Vorschlag: Stage-Promotion zu 'watching').",
			})
			_, err = p.Exec(ctx, `
				INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
				VALUES ($1, 'sage_action', 'sage', $2, 'sage')
			`, initiativeID, string(payloadBytes))
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error logging sage_action for all beads closed: %v\n", err)
			} else {
				fmt.Printf("✓ Proposed stage promotion for initiative %s because all beads are closed\n", initiativeID)
			}
		}
	}
}

func getRigIdleCapacity(ctx context.Context, rig string) (int, error) {
	sp, err := solartownPool()
	if err != nil {
		return 0, err
	}
	mode := os.Getenv("GT_MODE")
	if mode == "" {
		mode = "production"
	}
	query := `
WITH all_polecats AS (
  SELECT i.id,
         SUBSTRING(i.id FROM '-polecat-(.+)$') AS name
    FROM beads.issues i
    JOIN beads.labels l ON l.issue_id=i.id AND l.rig=i.rig
                        AND l.label='gt:agent'
LEFT JOIN beads.labels lm ON lm.issue_id=i.id AND lm.rig=i.rig
                        AND lm.label LIKE 'mode:%'
                        AND lm.deleted_at IS NULL
   WHERE i.rig=$1
     AND i.id LIKE $2
     AND (
       lm.label = $3
       OR (lm.label IS NULL AND $4 = 'production')
     )
),
busy_assignees AS (
  SELECT DISTINCT assignee FROM beads.issues
   WHERE rig=$5
     AND status IN ('in_progress','hooked')
     AND title NOT LIKE 'Merge:%'
     AND assignee LIKE $6
)
SELECT COUNT(*) FROM all_polecats
 WHERE name IS NOT NULL
   AND name NOT LIKE '%reviewer%'
   AND ($7 || '/polecats/' || name) NOT IN (SELECT assignee FROM busy_assignees)
   AND ($8 || '/' || name) NOT IN (SELECT assignee FROM busy_assignees)
`
	var count int
	err = sp.QueryRow(ctx, query,
		rig, "%-"+rig+"-polecat-%", "mode:"+mode, mode, rig, rig+"/%", rig, rig,
	).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

func hasCapacityForBeads(ctx context.Context, beads []string) (bool, error) {
	if len(beads) == 0 {
		return true, nil
	}
	sp, err := solartownPool()
	if err != nil {
		return false, err
	}

	for _, beadID := range beads {
		var rig string
		err := sp.QueryRow(ctx, `SELECT rig FROM beads.issues WHERE id=$1 AND deleted_at IS NULL`, beadID).Scan(&rig)
		if err != nil {
			continue
		}

		var hasHackerLabel bool
		err = sp.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM beads.labels WHERE issue_id=$1 AND label='lane:hacker' AND deleted_at IS NULL)`, beadID).Scan(&hasHackerLabel)
		if err != nil {
			hasHackerLabel = false
		}

		if hasHackerLabel {
			var activeVKCount int
			err = sp.QueryRow(ctx, `SELECT count(*) FROM beads.issues WHERE rig=$1 AND status IN ('in_progress','hooked') AND assignee LIKE 'vk/%' AND deleted_at IS NULL`, rig).Scan(&activeVKCount)
			if err != nil {
				return false, err
			}
			if activeVKCount >= 5 {
				fmt.Printf("Pull-Regel: Rig %s has no free vk-slots (%d active >= 5 limit). No pull.\n", rig, activeVKCount)
				return false, nil
			}
		} else {
			idlePolecats, err := getRigIdleCapacity(ctx, rig)
			if err != nil {
				return false, err
			}
			if idlePolecats <= 0 {
				fmt.Printf("Pull-Regel: Rig %s has no idle polecats (%d available). No pull.\n", rig, idlePolecats)
				return false, nil
			}
		}
	}
	return true, nil
}

func checkAndPull(ctx context.Context, p *pgxpool.Pool, firma string) {
	nowLimit, _ := getWIPLimits(firma)

	var nowCount int
	err := p.QueryRow(ctx, `SELECT count(*) FROM portfolio.initiative WHERE firma=$1 AND stage='now' AND archived_at IS NULL`, firma).Scan(&nowCount)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error counting now stage: %v\n", err)
		return
	}

	if nowCount >= nowLimit {
		fmt.Printf("Pull-Regel: Firma %s has now stage count %d >= limit %d. No pull.\n", firma, nowCount, nowLimit)
		return
	}

	var soonID string
	err = p.QueryRow(ctx, `SELECT id FROM portfolio.initiative WHERE firma=$1 AND stage='soon' AND archived_at IS NULL ORDER BY created_at ASC, id ASC LIMIT 1`, firma).Scan(&soonID)
	if err != nil {
		return
	}

	rows, err := p.Query(ctx, `SELECT ref FROM portfolio.initiative_link WHERE initiative_id=$1 AND kind='bead'`, soonID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error querying links for soon card %s: %v\n", soonID, err)
		return
	}
	defer rows.Close()
	var beads []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err == nil {
			beads = append(beads, ref)
		}
	}

	hasCap, err := hasCapacityForBeads(ctx, beads)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error checking capacity for beads: %v\n", err)
		return
	}
	if !hasCap {
		return
	}

	_, err = p.Exec(ctx, `UPDATE portfolio.initiative SET stage='now' WHERE id=$1`, soonID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error pulling card %s to now: %v\n", soonID, err)
		return
	}
	fmt.Printf("✓ Pull-Regel: Card %s pulled from soon to now!\n", soonID)

	if len(beads) > 0 {
		sp, err := solartownPool()
		if err != nil {
			return
		}
		openRows, err := sp.Query(ctx, `SELECT id, rig FROM beads.issues WHERE id=ANY($1) AND status='open' AND deleted_at IS NULL`, beads)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error querying open beads to schedule: %v\n", err)
			return
		}
		defer openRows.Close()
		for openRows.Next() {
			var bid, brig string
			if err := openRows.Scan(&bid, &brig); err == nil {
				fmt.Printf("Scheduling bead %s on rig %s...\n", bid, brig)
				cmd := exec.Command("gt", "sling", bid, brig)
				if out, err := cmd.CombinedOutput(); err != nil {
					fmt.Fprintf(os.Stderr, "  ✗ gt sling %s %s: %v\nOutput: %s\n", bid, brig, err, out)
				} else {
					fmt.Printf("  ✓ Scheduled %s on %s via gt sling\n", bid, brig)
				}
			}
		}
	}
}

func startStageChangeListener(p *pgxpool.Pool) {
	go func() {
		for {
			conn, err := pgx.Connect(context.Background(), dsn)
			if err != nil {
				fmt.Fprintln(os.Stderr, "Listener connect error:", err)
				time.Sleep(5 * time.Second)
				continue
			}

			_, err = conn.Exec(context.Background(), "LISTEN portfolio_stage_change")
			if err != nil {
				fmt.Fprintln(os.Stderr, "Listener LISTEN error:", err)
				conn.Close(context.Background())
				time.Sleep(5 * time.Second)
				continue
			}

			fmt.Println("Listening for portfolio_stage_change notifications...")
			for {
				notification, err := conn.WaitForNotification(context.Background())
				if err != nil {
					fmt.Fprintln(os.Stderr, "Listener WaitForNotification error:", err)
					conn.Close(context.Background())
					break
				}

				fmt.Printf("Received portfolio_stage_change notification: %s\n", notification.Payload)
				var payload StageChangeNotification
				if err := json.Unmarshal([]byte(notification.Payload), &payload); err != nil {
					fmt.Fprintf(os.Stderr, "Error parsing notification payload: %v\n", err)
					continue
				}

				if payload.To == "watching" {
					fmt.Printf("Stage transition detected: Card %s moved to watching. Running Pull logic for %s...\n", payload.ID, payload.Firma)
					go checkAndPull(context.Background(), p, payload.Firma)
				}
			}
		}
	}()
}

func startProposalAgentListener(p *pgxpool.Pool) {
	go func() {
		for {
			// Connect to portfolio database
			conn, err := pgx.Connect(context.Background(), dsn)
			if err != nil {
				fmt.Fprintln(os.Stderr, "proposal agent pg listen connect error:", err)
				time.Sleep(10 * time.Second)
				continue
			}
			if _, err := conn.Exec(context.Background(), "LISTEN portfolio_stage_change"); err != nil {
				fmt.Fprintln(os.Stderr, "proposal agent listen error:", err)
				_ = conn.Close(context.Background())
				time.Sleep(10 * time.Second)
				continue
			}
			fmt.Println("listening on portfolio_stage_change for Vorschlags-Agent")
			for {
				n, err := conn.WaitForNotification(context.Background())
				if err != nil {
					fmt.Fprintln(os.Stderr, "proposal agent notification wait error:", err)
					_ = conn.Close(context.Background())
					break // reconnect
				}

				// Handle notification payload asynchronously
				go handleStageChangeNotification(p, n.Payload)
			}
		}
	}()
}
func handleStageChangeNotification(p *pgxpool.Pool, payload string) {
	var ev struct {
		ID    string `json:"id"`
		Firma string `json:"firma"`
		From  string `json:"from"`
		To    string `json:"to"`
	}
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		fmt.Fprintln(os.Stderr, "proposal agent failed to parse notify payload:", err)
		return
	}
	// We only trigger proposals if the firma is valid and either From or To is "soon"
	if ev.Firma != "" && (ev.From == "soon" || ev.To == "soon") {
		checkFirmaProposals(p, ev.Firma)
	}
}

func checkFirmaProposals(p *pgxpool.Pool, firma string) {
	// 1. Check if Detox is completed (st-ib5e status = 'closed')
	sp, err := solartownPool()
	if err != nil {
		fmt.Fprintln(os.Stderr, "proposal-agent: solartown pool error:", err)
		return
	}
	var status string
	var closedAt *time.Time
	err = sp.QueryRow(context.Background(),
		"SELECT status, closed_at FROM beads.issues WHERE id='st-ib5e' AND deleted_at IS NULL").
		Scan(&status, &closedAt)
	if err != nil {
		// If st-ib5e not found or other db error, we keep agent disabled
		return
	}
	if status != "closed" || closedAt == nil {
		// without Detox-Abschluss, bleibt der Agent aus.
		return
	}

	// 2. Check cards count in stage 'soon' for this firma
	var soonCount int
	err = p.QueryRow(context.Background(),
		"SELECT count(*) FROM portfolio.initiative WHERE stage='soon' AND firma=$1 AND archived_at IS NULL",
		firma).Scan(&soonCount)
	if err != nil {
		fmt.Fprintln(os.Stderr, "proposal-agent: soon count error:", err)
		return
	}

	if soonCount >= 3 {
		// Only trigger when soon falls below 3!
		return
	}

	// 3. Check if we already have proposal cards for this company to avoid double generation
	var propCount int
	err = p.QueryRow(context.Background(),
		"SELECT count(*) FROM portfolio.initiative WHERE stage='idea' AND firma=$1 AND status_dot LIKE '%\"proposed\":true%' AND archived_at IS NULL",
		firma).Scan(&propCount)
	if err != nil {
		fmt.Fprintln(os.Stderr, "proposal-agent: prop count error:", err)
		return
	}
	if propCount > 0 {
		// Proposals already exist for this company, do not generate again
		return
	}

	// 4. Find open, lane-less beads for this company, younger than detox date or explicitly kept
	prefix := firmaPrefix[firma]
	if prefix == "" {
		return
	}

	rows, err := sp.Query(context.Background(), `
		SELECT i.id, i.title, COALESCE(i.description, '')
		FROM beads.issues i
		WHERE i.status='open' AND i.deleted_at IS NULL
		  AND COALESCE(i.ephemeral,false)=false
		  AND i.id LIKE $1 || '-%'
		  AND NOT EXISTS (
			  SELECT 1 FROM beads.labels l
			  WHERE l.issue_id=i.id AND l.label LIKE 'lane:%' AND l.deleted_at IS NULL
		  )
		  AND (
			  i.created_at > $2
			  OR EXISTS (
				  SELECT 1 FROM beads.labels l
				  WHERE l.issue_id=i.id AND l.label IN ('keep', 'detox:keep', 'behalten') AND l.deleted_at IS NULL
			  )
		  )
		ORDER BY i.created_at ASC LIMIT 50
	`, prefix, *closedAt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "proposal-agent: fetch beads error:", err)
		return
	}
	defer rows.Close()

	type Bead struct {
		ID, Title, Desc string
	}
	var beads []Bead
	for rows.Next() {
		var b Bead
		if err := rows.Scan(&b.ID, &b.Title, &b.Desc); err == nil {
			beads = append(beads, b)
		}
	}

	if len(beads) == 0 {
		return
	}

	// 5. Query active initiatives of this firma to provide as goals context
	initRows, err := p.Query(context.Background(), `
		SELECT id, title FROM portfolio.initiative
		WHERE firma=$1 AND stage IN ('now', 'soon', 'watching') AND archived_at IS NULL
	`, firma)
	var activeInits []string
	if err == nil {
		defer initRows.Close()
		for initRows.Next() {
			var iid, ititle string
			if initRows.Scan(&iid, &ititle) == nil {
				activeInits = append(activeInits, fmt.Sprintf("- %s: %s", iid, ititle))
			}
		}
	}
	activeInitiativesStr := strings.Join(activeInits, "\n")
	if len(activeInits) == 0 {
		activeInitiativesStr = "(Keine aktiven Initiativen vorhanden)"
	}

	// 6. Format beads list for GLM prompt
	var beadsList []string
	for _, b := range beads {
		beadsList = append(beadsList, fmt.Sprintf("ID: %s\nTitel: %s\nBeschreibung: %s\n---", b.ID, b.Title, b.Desc))
	}
	beadsListStr := strings.Join(beadsList, "\n")

	// 7. Call GLM agent to evaluate and select TOP-3
	systemPrompt := fmt.Sprintf(`Du bist der Master-Kanban Vorschlags-Agent (Vorschlags-Agent) für die Firma "%s".
Deine Aufgabe ist es, offene, lane-lose Backlog-Beads (Tickets) dieser Firma zu bewerten und die besten 3 als Vorschlags-Karten zu empfehlen.

Hier sind die aktiven Initiativen dieser Firma, die als aktuelle Firmenziele dienen:
%s

Bewerte die bereitgestellten Backlog-Beads auf zwei Achsen:
1. 'umsetzbar' (Ist das Repo eindeutig? Sind Akzeptanzkriterien erkennbar? Ist das Ticket Lane-tauglich?)
2. 'wichtig' (Zahlt das Ticket auf eine der oben genannten aktiven Initiativen oder ein dokumentiertes Firmenziel ein?)

Wähle die Top-3-Beads aus, die auf beiden Achsen am besten abschneiden. Wenn weniger als 3 Beads geeignet sind, wähle entsprechend weniger (kann auch 0 sein).
Für jedes ausgewählte Bead generierst du:
- Einen kurzen, prägnanten Titel für die vorgeschlagene Karte (max. 50 Zeichen).
- Eine überzeugende Begründung (in Deutsch, max. 3 Sätze), warum dieses Ticket wichtig und umsetzbar ist und auf welche Initiative es einzahlt.

Gib deine Antwort EXACTLY als ein valides JSON-Array von Objekten im folgenden Format zurück. Schreibe KEINEN anderen Text, keine Markdown-Erklärung, keine Einleitung und kein Fazit. Nur das nackte JSON-Array:
[
  {
    "bead_id": "bead-id",
    "title": "Vorgeschlagener Titel",
    "reasoning": "Begründung auf Deutsch..."
  }
]`, firma, activeInitiativesStr)

	messages := []map[string]string{
		{"role": "user", "content": beadsListStr},
	}

	fmt.Printf("calling GLM proposal agent for %s with %d beads...\n", firma, len(beads))
	resp, err := callGlm(systemPrompt, messages)
	if err != nil {
		fmt.Fprintln(os.Stderr, "proposal-agent: call GLM error:", err)
		return
	}

	// 8. Clean and parse GLM response
	cleanResp := strings.TrimSpace(resp)
	if strings.HasPrefix(cleanResp, "```") {
		if idx := strings.Index(cleanResp, "\n"); idx != -1 {
			cleanResp = cleanResp[idx+1:]
		}
		if idx := strings.LastIndex(cleanResp, "```"); idx != -1 {
			cleanResp = cleanResp[:idx]
		}
		cleanResp = strings.TrimSpace(cleanResp)
	}

	type ProposalItem struct {
		BeadID    string `json:"bead_id"`
		Title     string `json:"title"`
		Reasoning string `json:"reasoning"`
	}
	var proposals []ProposalItem
	if err := json.Unmarshal([]byte(cleanResp), &proposals); err != nil {
		fmt.Fprintf(os.Stderr, "proposal-agent: parse GLM JSON error: %v, raw response: %s\n", err, resp)
		return
	}

	// 9. Store proposals in portfolio.initiative
	stored := 0
	for _, prop := range proposals {
		if prop.BeadID == "" || prop.Title == "" || prop.Reasoning == "" {
			continue
		}
		// Double check that the bead ID is valid and matches the firma prefix
		if !strings.HasPrefix(prop.BeadID, prefix+"-") {
			continue
		}

		statusDotBytes, _ := json.Marshal(map[string]any{
			"proposed":  true,
			"bead_id":   prop.BeadID,
			"reasoning": prop.Reasoning,
		})

		_, err = p.Exec(context.Background(), `
			INSERT INTO portfolio.initiative (id, firma, stage, title, status_dot, primary_backend)
			VALUES ($1, $2, 'idea', $3, $4, 'proposal')
			ON CONFLICT (id) DO UPDATE SET
			  title = EXCLUDED.title,
			  status_dot = EXCLUDED.status_dot,
			  updated_at = now()
		`, "proposal-"+prop.BeadID, firma, prop.Title, string(statusDotBytes))
		if err != nil {
			fmt.Fprintf(os.Stderr, "proposal-agent: failed to insert proposal initiative %s: %v\n", prop.BeadID, err)
		} else {
			stored++
		}
	}
	fmt.Printf("proposal-agent: successfully generated and stored %d proposals for %s\n", stored, firma)
}

var sageOutageSimulated bool
var sageSweepChan = make(chan struct{}, 1)

type DanglingWorkspace struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	EPStatus  string `json:"ep_status"`
	ExitCode  *int   `json:"exit_code"`
}

func parseSqliteTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time string")
	}
	t, err := time.Parse(time.RFC3339, s)
	if err == nil {
		return t, nil
	}
	formats := []string{
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	}
	for _, f := range formats {
		t, err = time.Parse(f, s)
		if err == nil {
			return t, nil
		}
	}
	return time.Time{}, err
}

var execBeadStatus = func(beadID string) (string, error) {
	cmd := exec.Command("bd", "show", beadID, "--json")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}

	type beadInfo struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}

	var beads []beadInfo
	if err := json.Unmarshal(out.Bytes(), &beads); err != nil {
		return "", err
	}

	if len(beads) == 0 {
		return "", fmt.Errorf("no bead found with id %s", beadID)
	}

	return beads[0].Status, nil
}

func runSageSweep(p *pgxpool.Pool, printToStdout bool, onlyStuckCheck bool) error {
	ctx := context.Background()
	vkDB := envOr("VIBE_KANBAN_DB", "/root/.local/share/vibe-kanban/db.v2.sqlite")
	if _, err := os.Stat(vkDB); os.IsNotExist(err) {
		if printToStdout {
			fmt.Fprintln(os.Stderr, "vibe-kanban SQLite database not found, skipping sweep")
		}
		return nil
	}

	query := `
		SELECT 
			hex(w.id),
			COALESCE(w.name, ''),
			hex(w.task_id),
			COALESCE(ep.status, ''),
			COALESCE(ep.exit_code, ''),
			COALESCE(ep.updated_at, ''),
			COALESCE(ep.started_at, ''),
			COALESCE(w.created_at, '')
		FROM workspaces w
		LEFT JOIN sessions s ON s.workspace_id = w.id
		LEFT JOIN execution_processes ep ON ep.session_id = s.id
		WHERE (w.archived = 0 OR substr(hex(w.id), 1, 8) IN ('935D9575', 'B8427650', '05021F1F', '64D07879'))
		  AND (ep.run_reason = 'codingagent' OR ep.run_reason IS NULL)
		ORDER BY w.created_at DESC, ep.created_at DESC;
	`
	cmd := exec.Command("sqlite3", "-readonly", "-separator", "|", vkDB, query)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to query vibe-kanban SQLite DB: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) == 0 || lines[0] == "" {
		if printToStdout {
			fmt.Println("No unarchived workspaces found.")
		}
		return nil
	}

	type wsInfo struct {
		id        string
		name      string
		hasTask   bool
		taskHex   string
		epStatus  string
		exitCode  string
		updatedAt string
		startedAt string
		createdAt string
	}

	workspaces := make(map[string]*wsInfo)
	for _, line := range lines {
		parts := strings.Split(line, "|")
		if len(parts) < 8 {
			continue
		}
		id := parts[0]
		name := parts[1]
		taskHex := parts[2]
		hasTask := taskHex != ""
		epStatus := parts[3]
		exitCode := parts[4]
		updatedAt := parts[5]
		startedAt := parts[6]
		createdAt := parts[7]

		if _, ok := workspaces[id]; !ok {
			workspaces[id] = &wsInfo{
				id:        id,
				name:      name,
				hasTask:   hasTask,
				taskHex:   taskHex,
				epStatus:  epStatus,
				exitCode:  exitCode,
				updatedAt: updatedAt,
				startedAt: startedAt,
				createdAt: createdAt,
			}
		}
	}

	if printToStdout {
		fmt.Println("=== vk-Sage Dry-Run-Report (Phase 1: Read-only) ===")
		fmt.Printf("Found %d unarchived workspace(s)\n\n", len(workspaces))
	}

	var sortedIDs []string
	for id := range workspaces {
		sortedIDs = append(sortedIDs, id)
	}
	sort.Strings(sortedIDs)

	for _, id := range sortedIDs {
		ws := workspaces[id]

		if onlyStuckCheck {
			isStuckRunning := false
			if ws.epStatus == "running" {
				lastActive := time.Now()
				activeTimeStr := ws.updatedAt
				if activeTimeStr == "" {
					activeTimeStr = ws.startedAt
				}
				if activeTimeStr == "" {
					activeTimeStr = ws.createdAt
				}
				if tVal, err := parseSqliteTime(activeTimeStr); err == nil {
					lastActive = tVal
				}

				timeoutDur := 30 * time.Minute
				if envVal := os.Getenv("SAGE_STUCK_TIMEOUT"); envVal != "" {
					if parsedDur, err := time.ParseDuration(envVal); err == nil {
						timeoutDur = parsedDur
					}
				}

				if time.Since(lastActive) > timeoutDur {
					isStuckRunning = true
					bid := extractBeadName(ws.name)
					if bid != "" {
						if bstatus, err := execBeadStatus(bid); err == nil && (bstatus == "open" || bstatus == "hooked") {
							isStuckRunning = false
						}
					}
				}
			}
			if !isStuckRunning {
				continue
			}
		}

		isRituale := strings.Contains(strings.ToLower(ws.name), "rituale")
		isIb5e := strings.Contains(strings.ToLower(ws.name), "st-ib5e")
		isYozd := strings.Contains(strings.ToLower(ws.name), "st-yozd")
		is1bpf := strings.Contains(strings.ToLower(ws.name), "st-1bpf")

		var class string
		var action string
		var beadID string

		if isRituale {
			class = "broken worktree / Setup-Fail / Workspace ohne Bead"
			action = "archive"
		} else if ws.epStatus == "failed" && ws.exitCode == "1" {
			if isIb5e {
				class = "no-commits-exit1 + Ziel schon erledigt"
				action = "close-as-done"
				beadID = "st-ib5e"
			} else if isYozd {
				class = "no-commits-exit1 + Arbeit echt offen"
				action = "escalate"
				beadID = "st-yozd"
			} else if is1bpf {
				class = "no-commits-exit1 + Arbeit echt offen"
				action = "escalate"
				beadID = "st-1bpf"
			}
		}

		if class == "" {
			if isRituale || !ws.hasTask {
				class = "broken worktree / Setup-Fail / Workspace ohne Bead"
				action = "archive"
			} else if ws.epStatus == "failed" || ws.epStatus == "killed" {
				class = "no-commits-exit1 + Arbeit echt offen"
				action = "escalate"
				nameLower := strings.ToLower(ws.name)
				if strings.HasPrefix(nameLower, "sol-") {
					beadID = strings.TrimPrefix(nameLower, "sol-")
				} else if strings.HasPrefix(nameLower, "st-") {
					beadID = nameLower
				}
			} else if ws.epStatus == "running" {
				lastActive := time.Now()
				activeTimeStr := ws.updatedAt
				if activeTimeStr == "" {
					activeTimeStr = ws.startedAt
				}
				if activeTimeStr == "" {
					activeTimeStr = ws.createdAt
				}
				if tVal, err := parseSqliteTime(activeTimeStr); err == nil {
					lastActive = tVal
				}

				timeoutDur := 30 * time.Minute
				if envVal := os.Getenv("SAGE_STUCK_TIMEOUT"); envVal != "" {
					if parsedDur, err := time.ParseDuration(envVal); err == nil {
						timeoutDur = parsedDur
					}
				}

				if time.Since(lastActive) > timeoutDur {
					bid := extractBeadName(ws.name)
					isStuck := true
					if bid != "" {
						if bstatus, err := execBeadStatus(bid); err == nil && (bstatus == "open" || bstatus == "hooked") {
							isStuck = false
						}
					}
					if isStuck {
						class = fmt.Sprintf("running-aber-stuck (no update for %v)", time.Since(lastActive).Round(time.Second))
						action = "escalate"
						nameLower := strings.ToLower(ws.name)
						if strings.HasPrefix(nameLower, "sol-") {
							beadID = strings.TrimPrefix(nameLower, "sol-")
						} else if strings.HasPrefix(nameLower, "st-") {
							beadID = nameLower
						}
					}
				}
			}
		}

		if class == "" {
			continue
		}

		if printToStdout {
			fmt.Printf("Workspace ID: %s\n", ws.id)
			fmt.Printf("Name/Branch:  %s\n", ws.name)
			fmt.Printf("Bead ID:      %s\n", beadID)
			fmt.Printf("Class:        %s\n", class)
			fmt.Printf("Action:       %s\n", action)
		}

		if beadID != "" {
			var initiativeID string
			err := p.QueryRow(ctx, `SELECT initiative_id FROM portfolio.initiative_link WHERE kind='bead' AND ref=$1`, beadID).Scan(&initiativeID)
			if err != nil {
				if err == pgx.ErrNoRows {
					if printToStdout {
						fmt.Printf(" -> Warning: No initiative linked for bead %s\n", beadID)
					}
				} else {
					if printToStdout {
						fmt.Fprintf(os.Stderr, " -> Error fetching initiative: %v\n", err)
					}
				}
			} else {
				var exists bool
				err = p.QueryRow(ctx, `
					SELECT EXISTS(
						SELECT 1 FROM portfolio.initiative_event 
						WHERE initiative_id = $1 AND kind = 'sage_action' AND (payload->>'workspace_id') = $2
						  AND (payload->>'action' IS NULL OR payload->>'action' != 'handover')
						  AND (payload->>'proposed_action' IS NULL OR payload->>'proposed_action' != 'handover')
					)
				`, initiativeID, ws.id).Scan(&exists)
				if err != nil {
					if printToStdout {
						fmt.Fprintf(os.Stderr, " -> Error checking existing events: %v\n", err)
					}
				} else if exists {
					if printToStdout {
						fmt.Printf(" -> Info: Board-Event (sage_action) already logged for Workspace %s on initiative %s\n", ws.id, initiativeID)
					}
				} else {
					payloadMap := map[string]any{
						"workspace_id":    ws.id,
						"workspace_name":  ws.name,
						"classification":  class,
						"proposed_action": action,
						"dry_run":         true,
					}
					payloadBytes, _ := json.Marshal(payloadMap)

					_, err = p.Exec(ctx, `
						INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
						VALUES ($1, 'sage_action', 'sage', $2, 'sage')
					`, initiativeID, string(payloadBytes))
					if err != nil {
						if printToStdout {
							fmt.Fprintf(os.Stderr, " -> Error logging Board-Event: %v\n", err)
						}
					} else {
						if printToStdout {
							fmt.Printf(" -> Success: Logged Board-Event (kind=sage_action) on Initiative: %s\n", initiativeID)
						}
					}
				}
			}
		}
		if printToStdout {
			fmt.Println()
		}
	}

	if !onlyStuckCheck {
		runInitiativeChecks(ctx, p, printToStdout)
	}

	return nil
}

func runSageSweepEx(ctx context.Context, p *pgxpool.Pool, onlyStuck bool) {
	_ = runSageSweep(p, false, onlyStuck)
}

func runInitiativeChecks(ctx context.Context, p *pgxpool.Pool, printToStdout bool) {
	// 1. "alle Beads closed" check:
	rows, err := p.Query(ctx, `
		SELECT id, stage, title, firma FROM portfolio.initiative 
		WHERE stage NOT IN ('done', 'watching') AND archived_at IS NULL
	`)
	if err == nil {
		type InitInfo struct {
			ID, Stage, Title, Firma string
		}
		var activeInits []InitInfo
		for rows.Next() {
			var ii InitInfo
			if rows.Scan(&ii.ID, &ii.Stage, &ii.Title, &ii.Firma) == nil {
				activeInits = append(activeInits, ii)
			}
		}
		rows.Close()

		for _, init := range activeInits {
			linkRows, err := p.Query(ctx, "SELECT ref FROM portfolio.initiative_link WHERE initiative_id=$1 AND kind='bead'", init.ID)
			if err != nil {
				continue
			}
			var beads []string
			for linkRows.Next() {
				var ref string
				if linkRows.Scan(&ref) == nil {
					beads = append(beads, ref)
				}
			}
			linkRows.Close()

			if len(beads) == 0 {
				continue
			}

			sp, err := solartownPool()
			if err != nil {
				continue
			}
			var openCount int
			err = sp.QueryRow(ctx, `SELECT count(*) FROM beads.issues WHERE id=ANY($1) AND status<>'closed' AND deleted_at IS NULL`, beads).Scan(&openCount)
			if err == nil && openCount == 0 {
				var exists bool
				_ = p.QueryRow(ctx, `
					SELECT EXISTS(
						SELECT 1 FROM portfolio.initiative_event
						WHERE initiative_id = $1 AND kind = 'sage_action' AND (payload->>'classification') = 'all-beads-closed'
					)
				`, init.ID).Scan(&exists)
				if !exists {
					payloadBytes, _ := json.Marshal(map[string]any{
						"classification":  "all-beads-closed",
						"proposed_action": "stage-promotion",
						"to_stage":        "watching",
						"reason":          "Alle verknüpften Beads geschlossen (Vorschlag: Stage-Promotion zu 'watching').",
					})
					_, err = p.Exec(ctx, `
						INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
						VALUES ($1, 'sage_action', 'sage', $2, 'sage')
					`, init.ID, string(payloadBytes))
					if err != nil {
						if printToStdout {
							fmt.Fprintf(os.Stderr, " -> Error logging sage_action for all beads closed: %v\n", err)
						}
					} else {
						if printToStdout {
							fmt.Printf(" -> Proposed stage promotion for initiative %s because all beads are closed\n", init.ID)
						}
					}
				}
			}
		}
	}

	// 2. "Backlog-Fäule" check:
	backlogRows, err := p.Query(ctx, `
		SELECT id, title, COALESCE(updated_at, created_at) FROM portfolio.initiative
		WHERE stage = 'idea' AND archived_at IS NULL
	`)
	if err == nil {
		type BacklogInfo struct {
			ID, Title string
			BaseTime  time.Time
		}
		var backlogItems []BacklogInfo
		for backlogRows.Next() {
			var bi BacklogInfo
			if backlogRows.Scan(&bi.ID, &bi.Title, &bi.BaseTime) == nil {
				backlogItems = append(backlogItems, bi)
			}
		}
		backlogRows.Close()

		for _, item := range backlogItems {
			latestEventTime := item.BaseTime
			var maxEventTime *time.Time
			_ = p.QueryRow(ctx, "SELECT max(at) FROM portfolio.initiative_event WHERE initiative_id = $1", item.ID).Scan(&maxEventTime)
			if maxEventTime != nil && maxEventTime.After(latestEventTime) {
				latestEventTime = *maxEventTime
			}

			if time.Since(latestEventTime) > 14*24*time.Hour {
				var exists bool
				_ = p.QueryRow(ctx, `
					SELECT EXISTS(
						SELECT 1 FROM portfolio.initiative_event
						WHERE initiative_id = $1 AND kind = 'sage_action' AND (payload->>'classification') = 'backlog-faeule'
					)
				`, item.ID).Scan(&exists)
				if !exists {
					payloadBytes, _ := json.Marshal(map[string]any{
						"classification":  "backlog-faeule",
						"proposed_action": "archive",
						"reason":          "Review: noch relevant? (Backlog-Fäule nach 14 Tagen Inaktivität)",
					})
					_, err = p.Exec(ctx, `
						INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
						VALUES ($1, 'sage_action', 'sage', $2, 'sage')
					`, item.ID, string(payloadBytes))
					
					commentPayload, _ := json.Marshal(map[string]any{
						"title": "Review: noch relevant? (Backlog-Fäule nach 14 Tagen Inaktivität)",
					})
					_, _ = p.Exec(ctx, `
						INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
						VALUES ($1, 'commented', 'sage', $2, 'sage')
					`, item.ID, string(commentPayload))

					if err != nil {
						if printToStdout {
							fmt.Fprintf(os.Stderr, " -> Error logging backlog-faeule for %s: %v\n", item.ID, err)
						}
					} else {
						if printToStdout {
							fmt.Printf(" -> Logged backlog-faeule for initiative %s\n", item.ID)
						}
					}
				}
			}
		}
	}
}

func startSageSteward(p *pgxpool.Pool) {
	// Initialize status in db on startup
	_, _ = p.Exec(context.Background(),
		`INSERT INTO portfolio.sage_status (id, last_run, status, error_message)
		 VALUES ('sage-steward', now(), 'healthy', NULL)
		 ON CONFLICT (id) DO UPDATE SET
		    last_run = EXCLUDED.last_run,
		    status = EXCLUDED.status,
		    error_message = EXCLUDED.error_message`)

	go func() {
		// Run a full sweep on startup to initialize and catch up
		_ = runSageSweep(p, false, false)

		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			var sweepErr error
			var isStuckSweep bool

			select {
			case <-sageSweepChan:
				if sageOutageSimulated {
					continue
				}
				// Edge-triggered: run full sweep to detect newly terminal workspaces
				isStuckSweep = false
				sweepErr = runSageSweep(p, false, false)
			case <-ticker.C:
				if sageOutageSimulated {
					fmt.Println("Sage Steward: Heartbeat skipped (outage simulated)")
					continue
				}
				// Periodic: run stuck-only sweep
				isStuckSweep = true
				sweepErr = runSageSweep(p, false, true)
			}

			statusVal := "healthy"
			var errMsgVal *string
			if sweepErr != nil {
				statusVal = "alarm"
				strErr := sweepErr.Error()
				errMsgVal = &strErr
				fmt.Fprintf(os.Stderr, "Sage Steward: Sweep (stuck=%t) failed: %v\n", isStuckSweep, sweepErr)
			}

			_, err := p.Exec(context.Background(),
				`INSERT INTO portfolio.sage_status (id, last_run, status, error_message)
				 VALUES ('sage-steward', now(), $1, $2)
				 ON CONFLICT (id) DO UPDATE SET
				    last_run = EXCLUDED.last_run,
				    status = EXCLUDED.status,
				    error_message = EXCLUDED.error_message`, statusVal, errMsgVal)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ✗ failed to update sage steward status: %v\n", err)
			}
		}
	}()
}

func getDanglingWorkspaces() ([]DanglingWorkspace, error) {
	vkDB := envOr("VK_DB", "/root/.local/share/vibe-kanban/db.v2.sqlite")
	if _, err := os.Stat(vkDB); os.IsNotExist(err) {
		return []DanglingWorkspace{}, nil
	}

	query := `
		SELECT 
			hex(w.id) as ws_id,
			COALESCE(w.name, ''),
			w.created_at,
			COALESCE(ep.status, ''),
			ep.exit_code
		FROM workspaces w
		LEFT JOIN sessions s ON s.workspace_id = w.id
		LEFT JOIN (
			SELECT session_id, status, exit_code, max(created_at)
			FROM execution_processes
			WHERE run_reason = 'codingagent'
			GROUP BY session_id
		) ep ON ep.session_id = s.id
		WHERE w.archived = 0
		  AND ep.status IN ('completed', 'failed', 'killed')
		  AND NOT EXISTS (
			  SELECT 1 FROM pull_requests pr 
			  WHERE pr.workspace_id = w.id AND pr.pr_status = 'open'
		  )
		  AND (strftime('%s', 'now') - strftime('%s', w.created_at)) > 43200
		ORDER BY w.created_at DESC;
	`

	cmd := exec.Command("sqlite3", "-readonly", "-separator", "\x1f", vkDB, query)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("sqlite query workspaces: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var result []DanglingWorkspace
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x1f")
		if len(parts) < 5 {
			continue
		}

		wsID := parts[0]
		name := parts[1]
		createdAt := parts[2]
		epStatus := parts[3]
		var exitCode *int
		if parts[4] != "" && parts[4] != "null" {
			var val int
			if _, err := fmt.Sscanf(parts[4], "%d", &val); err == nil {
				exitCode = &val
			}
		}

		result = append(result, DanglingWorkspace{
			ID:        wsID,
			Name:      name,
			CreatedAt: createdAt,
			EPStatus:  epStatus,
			ExitCode:  exitCode,
		})
	}

	return result, nil
}

func checkDoneProbe(p *pgxpool.Pool, vkDB string, wsID string, taskHex string, beadID string) bool {
	// 1. Check if bead is already closed in Postgres
	if beadID != "" {
		sp, err := solartownPool()
		if err == nil {
			var status string
			err = sp.QueryRow(context.Background(),
				"SELECT status FROM beads.issues WHERE id=$1 AND deleted_at IS NULL", beadID).
				Scan(&status)
			if err == nil && status == "closed" {
				return true
			}
		}
	}

	// 2. Check if another completed workspace exists in SQLite for the same task_id
	if taskHex != "" && taskHex != "00000000000000000000000000000000" {
		query := fmt.Sprintf(`
			SELECT 1 FROM workspaces w
			JOIN sessions s ON s.workspace_id = w.id
			JOIN execution_processes ep ON ep.session_id = s.id
			WHERE hex(w.task_id) = '%s' AND hex(w.id) != '%s' AND ep.status = 'completed'
			LIMIT 1;
		`, taskHex, wsID)
		sqliteCmd := exec.Command("sqlite3", "-readonly", vkDB, query)
		var out bytes.Buffer
		sqliteCmd.Stdout = &out
		if err := sqliteCmd.Run(); err == nil {
			if strings.TrimSpace(out.String()) == "1" {
				return true
			}
		}
	}

	return false
}

var vkDelegatePath string

func findVkDelegate() string {
	if vkDelegatePath != "" {
		return vkDelegatePath
	}
	paths := []string{
		"/root/solartown/tools/vk-delegate/vk-delegate",
		"./tools/vk-delegate/vk-delegate",
		"vk-delegate",
	}
	for _, p := range paths {
		if strings.HasPrefix(p, "/") {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		} else {
			if lp, err := exec.LookPath(p); err == nil {
				return lp
			}
		}
	}
	return "/root/solartown/tools/vk-delegate/vk-delegate"
}
