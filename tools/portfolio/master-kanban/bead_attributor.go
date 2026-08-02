package main

// bead_attributor.go — Bead-Attribution + Verschleiß-Klassifikation
// (PRD fabrik-token-usage-tracking WP-2).
//
// Attributor summiert usage pro VK-Prozess-JSONL, joint execution_processes
// (Modell) + vk-cost.jsonl (bead, exit) und klassifiziert den Ausgang:
//   merged   — Bead-MR landete in main (beads.issues.status='closed')
//   errored  — exit ≠ 0 / workspace-pruned (aus vk-cost.jsonl)
//   unmerged — Lauf ok, aber nie gemerged (bead status nicht closed)
//   retry    — Folgelauf desselben Beads innerhalb max_retry_gap (Default 48h)
//
// Verschleiß-Definition: leer gelaufen = errored + unmerged + alle retry-Läufe
// außer dem letzten gemergten.
//
// Rein additiv — Schreibpfad nur portfolio.bead_token_usage (upsert, idempotent).
// Rollback = DROP TABLE portfolio.bead_token_usage.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// costEvent — eine Zeile aus vk-cost.jsonl.
// Nur die fuer die Attribution relevanten Felder.
type costEvent struct {
	TS                int64  `json:"ts"`
	Event             string `json:"event"`
	Rig               string `json:"rig"`
	Bead              string `json:"bead"`
	WorkspaceID       string `json:"workspace_id,omitempty"`
	ProcessID         string `json:"process_id,omitempty"`
	ExitCode          *int   `json:"exit_code,omitempty"`
	Status            string `json:"status,omitempty"`
	CompletionSource  string `json:"completion_source,omitempty"`
}

// procRun — aggregierte Sicht auf einen (bead, process_id)-Lauf.
type procRun struct {
	Bead         string
	Rig          string
	ProcessID    string
	WorkspaceID  string
	FirstTS      time.Time
	LastTS       time.Time
	ExitCode     int
	HasOutcome   bool   // true, sobald completed/errored/preflight-red gesehen
	IsErrored    bool   // exit≠0 oder workspace-pruned
	OutcomeLabel string // completed/errored/preflight-red (roher outcome)
}

// readCostJSONL liest vk-cost.jsonl und baut pro (bead, process_id) die
// aggregierte procRun-Sicht. Fehlerhafte Zeilen werden uebersprungen
// (best-effort, wie fleet_parser).
func readCostJSONL(path string) (map[string]*procRun, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	runs := make(map[string]*procRun)
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024*4)

	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.Contains(line, []byte(`"process_id"`)) {
			continue
		}
		var ev costEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Bead == "" || ev.ProcessID == "" {
			continue
		}
		key := ev.Bead + "\x00" + ev.ProcessID
		r, ok := runs[key]
		if !ok {
			r = &procRun{Bead: ev.Bead, ProcessID: ev.ProcessID, Rig: ev.Rig, WorkspaceID: ev.WorkspaceID}
			runs[key] = r
		}
		ts := time.Unix(ev.TS, 0).UTC()
		if r.FirstTS.IsZero() || ts.Before(r.FirstTS) {
			r.FirstTS = ts
		}
		if ts.After(r.LastTS) {
			r.LastTS = ts
		}
		if ev.Rig != "" {
			r.Rig = ev.Rig
		}
		switch ev.Event {
		case "completed":
			r.HasOutcome = true
			if ev.ExitCode != nil {
				r.ExitCode = *ev.ExitCode
			}
		case "errored":
			r.HasOutcome = true
			r.IsErrored = true
			r.OutcomeLabel = "errored"
			if ev.ExitCode != nil {
				r.ExitCode = *ev.ExitCode
			}
			if ev.Status == "workspace-pruned" {
				r.IsErrored = true
			}
		case "preflight-red":
			r.HasOutcome = true
			r.IsErrored = true
			r.OutcomeLabel = "preflight-red"
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return runs, nil
}

// vkProcessInfo — Lookup-Ergebnis fuer einen execution_process.
type vkProcessInfo struct {
	Model     string
	SessionID string // als kanonische UUID (mit Bindestrichen)
}

// loadVKProcessModels oeffnet die VK sqlite (read-only) und liefert eine Map
// process_id (hex) -> {model, session_id}. Nur codingagent-Prozesse haben ein
// nennenswertes Transcript — Setup/Cleanup wird hier nicht erfasst.
func loadVKProcessModels(vkDB string) (map[string]vkProcessInfo, error) {
	if _, err := os.Stat(vkDB); err != nil {
		return nil, err
	}
	// lower(hex(id)) weil id BLOB; VK speichert UUIDs als 16-Byte BLOB.
	const q = `
		SELECT lower(hex(id)),
		       COALESCE(json_extract(executor_action, '$.executor_config.model_id'), ''),
		       lower(hex(session_id))
		FROM execution_processes
		WHERE run_reason = 'codingagent'
	`
	cmd := exec.Command("sqlite3", "-readonly", vkDB, q)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	result := make(map[string]vkProcessInfo)
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			continue
		}
		id := strings.TrimSpace(parts[0])
		model := strings.TrimSpace(parts[1])
		sess := hexToUUID(strings.TrimSpace(parts[2]))
		if id != "" {
			result[id] = vkProcessInfo{Model: model, SessionID: sess}
		}
	}
	return result, nil
}

// hexToUUID formt einen 32-stelligen Hex-String in die kanonische UUID-Notation
// (8-4-4-4-12, klein geschrieben). Leerer String bei falscher Laenge.
func hexToUUID(hex string) string {
	hex = strings.ToLower(strings.ReplaceAll(hex, "-", ""))
	if len(hex) != 32 {
		return ""
	}
	return hex[0:8] + "-" + hex[8:12] + "-" + hex[12:16] + "-" + hex[16:20] + "-" + hex[20:32]
}

// indexClaudeProjectJSONL walkt /root/.claude/projects einmal und baut eine
// Map session_id (Dateiname ohne .jsonl) -> voller Pfad. Mehrere Vorkommen
// derselben session_id in unterschiedlichen Werkzeug-Pfaden werden alle
// erfasst (neueste Wins praeferiert).
func indexClaudeProjectJSONL(projectsRoot string) map[string]string {
	paths := make(map[string]string)
	if _, err := os.Stat(projectsRoot); err != nil {
		return paths
	}
	_ = filepath.Walk(projectsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		name := info.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		sess := strings.TrimSuffix(name, ".jsonl")
		paths[sess] = path
		return nil
	})
	return paths
}

// indexProcessJSONL walkt /root/.local/share/vibe-kanban/sessions/**/processes/
// und baut eine Map process_id (ohne .jsonl) -> voller Pfad.
//
// Hinweis: Diese Dateien enthalten Hook-Stdout, KEINE Token-Usage. Sie werden
// nur als Fallback indexiert (z.B. fuer Setup/Cleanup-Prozesse). Die echte
// Modell-Transkription liegt in /root/.claude/projects/<encoded-worktree>/
// <session_id>.jsonl — geliefert via indexClaudeProjectJSONL.
func indexProcessJSONL(sessionsRoot string) map[string]string {
	paths := make(map[string]string)
	root := filepath.Join(sessionsRoot)
	if _, err := os.Stat(root); err != nil {
		return paths
	}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		name := info.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		if !strings.Contains(path, "/processes/") {
			return nil
		}
		pid := strings.TrimSuffix(name, ".jsonl")
		paths[pid] = path
		return nil
	})
	return paths
}

// sumTokensForProcess parst das Prozess-JSONL und summiert usage-Objekte
// (Top-Level „usage" oder message.usage). Besteht kein Fehler-Path, sind
// alle 4 Counter 0 (z.B. Setup-Scripts ohne Token-Verbrauch).
func sumTokensForProcess(path string) (input, output, cacheCreation, cacheRead int64) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024*10)

	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.Contains(line, []byte(`"usage"`)) {
			continue
		}
		var parsed struct {
			Usage *struct {
				InputTokens         *int64 `json:"input_tokens"`
				OutputTokens        *int64 `json:"output_tokens"`
				CacheCreationTokens *int64 `json:"cache_creation_input_tokens"`
				CacheReadTokens     *int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
			Message *struct {
				Usage *struct {
					InputTokens         *int64 `json:"input_tokens"`
					OutputTokens        *int64 `json:"output_tokens"`
					CacheCreationTokens *int64 `json:"cache_creation_input_tokens"`
					CacheReadTokens     *int64 `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(line, &parsed); err != nil {
			continue
		}
		u := parsed.Usage
		if u == nil && parsed.Message != nil {
			u = parsed.Message.Usage
		}
		if u == nil {
			continue
		}
		if u.InputTokens != nil {
			input += *u.InputTokens
		}
		if u.OutputTokens != nil {
			output += *u.OutputTokens
		}
		if u.CacheCreationTokens != nil {
			cacheCreation += *u.CacheCreationTokens
		}
		if u.CacheReadTokens != nil {
			cacheRead += *u.CacheReadTokens
		}
	}
	return
}

// classifyOutcome wendet die Verschleiß-Logik auf einen Lauf an.
// beadRuns muss alle Laufe desselben Beads enthalten (sortiert nach LastTS),
// beadStatus ist der Status aus beads.issues ('closed' => merged).
// maxRetryGap = wie weit auseinander duerfen zwei Laufe liegen, um als Retry zu zaehlen.
//
// Verschleiß-Definition (PRD WP-2.3): errored + unmerged + alle retry-Laeufe
// außer dem letzten gemergten Lauf desselben Beads.
func classifyOutcome(r *procRun, beadRuns []*procRun, beadStatus string, maxRetryGap time.Duration) string {
	if r.IsErrored {
		return "errored"
	}
	if beadStatus != "closed" {
		return "unmerged"
	}
	// Bead ist closed → mindestens der juengste Lauf zaehlt als merged.
	var lastTS time.Time
	for _, x := range beadRuns {
		if x.LastTS.After(lastTS) {
			lastTS = x.LastTS
		}
	}
	if r.LastTS.Equal(lastTS) {
		return "merged"
	}
	// aelterer Lauf: retry, falls innerhalb des Fensters vor dem juengsten
	if lastTS.Sub(r.LastTS) <= maxRetryGap {
		return "retry"
	}
	// aelter als das Fenster → eigenstaendiger Lauf, der aber auch gemergt
	// wurde (Bead ist closed). Zaehlt nicht als Verschleiß.
	return "merged"
}

// cmdBeadAttribute — cobra-Subcommand „bead-attribute".
func cmdBeadAttribute() *cobra.Command {
	var (
		costPath    string
		vkDB        string
		sessionsDir string
		projectsDir string
		maxGapHrs   float64
		dryRun      bool
	)
	c := &cobra.Command{
		Use:   "bead-attribute",
		Short: "Attribuiere Token-Verbrauch pro Bead-Prozess und klassifiziere Verschleiß (WP-2)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			return runBeadAttribute(ctx, costPath, vkDB, sessionsDir, projectsDir, time.Duration(maxGapHrs*float64(time.Hour)), dryRun)
		},
	}
	c.Flags().StringVar(&costPath, "cost-jsonl",
		envOr("VK_COST_JSONL", "/var/log/solartown/vk-cost.jsonl"),
		"vk-cost.jsonl Pfad (Dispatcher-Lifecycle-Ledger)")
	c.Flags().StringVar(&vkDB, "vk-db",
		envOr("VIBE_KANBAN_DB", "/root/.local/share/vibe-kanban/db.v2.sqlite"),
		"VK sqlite (execution_processes für Modell + session_id)")
	c.Flags().StringVar(&sessionsDir, "sessions-root",
		envOr("VK_SESSIONS_ROOT", "/root/.local/share/vibe-kanban/sessions"),
		"VK sessions/ Wurzel (Fallback: Hook-Stdout-JSONLs)")
	c.Flags().StringVar(&projectsDir, "projects-root",
		envOr("CLAUDE_PROJECTS_ROOT", "/root/.claude/projects"),
		"/root/.claude/projects Wurzel (echte Modell-Transkripte, Hauptquelle)")
	c.Flags().Float64Var(&maxGapHrs, "max-retry-gap-hours",
		48.0, "Max. Abstand zwischen zwei Läufen desselben Beads, damit der Folgelauf als retry zählt")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "Nur ausgeben, nicht in portfolio.bead_token_usage schreiben")
	return c
}

func runBeadAttribute(ctx context.Context, costPath, vkDB, sessionsRoot, projectsRoot string, maxRetryGap time.Duration, dryRun bool) error {
	// 1. vk-cost.jsonl → procRuns
	runs, err := readCostJSONL(costPath)
	if err != nil {
		return fmt.Errorf("read vk-cost.jsonl: %w", err)
	}
	if len(runs) == 0 {
		fmt.Println("bead-attribute: keine (bead, process_id)-Laufe in vk-cost.jsonl — nichts zu tun.")
		return nil
	}

	// 2. VK sqlite → model + session_id je process_id
	procInfos, _ := loadVKProcessModels(vkDB)

	// 3a. /root/.claude/projects → session_id -> JSONL-Pfad (Hauptquelle)
	claudePaths := indexClaudeProjectJSONL(projectsRoot)
	// 3b. sessions/**/processes/ → process_id -> JSONL-Pfad (Fallback, Hook-Stdout)
	processPaths := indexProcessJSONL(sessionsRoot)

	// 4. solartown beads.issues → bead status
	sp, err := solartownPool()
	if err != nil {
		return fmt.Errorf("solartown pool: %w", err)
	}
	beadIDs := make([]string, 0, len(runs))
	seen := make(map[string]bool)
	for _, r := range runs {
		if !seen[r.Bead] {
			seen[r.Bead] = true
			beadIDs = append(beadIDs, r.Bead)
		}
	}
	beadStatus := make(map[string]string)
	if len(beadIDs) > 0 {
		srows, serr := sp.Query(ctx, `SELECT id, status FROM beads.issues WHERE id = ANY($1)`, beadIDs)
		if serr == nil {
			for srows.Next() {
				var id, st string
				if srows.Scan(&id, &st) == nil {
					beadStatus[id] = st
				}
			}
			srows.Close()
		}
	}

	// 5. portfolio pool + discovery rules
	p := connect()
	var rules []DiscoveryRule
	rows, err := p.Query(ctx, `
		SELECT process_pattern, executor_pattern, model_pattern, provider_bucket, priority
		FROM portfolio.provider_discovery
		ORDER BY priority DESC
	`)
	if err != nil {
		return fmt.Errorf("query provider_discovery: %w", err)
	}
	for rows.Next() {
		var r DiscoveryRule
		if err := rows.Scan(&r.ProcessPattern, &r.ExecutorPattern, &r.ModelPattern, &r.ProviderBucket, &r.Priority); err != nil {
			rows.Close()
			return fmt.Errorf("scan discovery rule: %w", err)
		}
		rules = append(rules, r)
	}
	rows.Close()

	// 6. Pro Bead: Läufe sammeln + klassifizieren
	runsByBead := make(map[string][]*procRun)
	for _, r := range runs {
		runsByBead[r.Bead] = append(runsByBead[r.Bead], r)
	}
	for _, list := range runsByBead {
		sort.Slice(list, func(i, j int) bool { return list[i].LastTS.Before(list[j].LastTS) })
	}

	// 7. Pro Lauf: Token summiern, Provider-Bucket, Outcome, Upsert.
	type upsertRow struct {
		bead, rig, bucket, model, processID, outcome string
		input, output, cacheCreate, cacheRead        int64
		firstTS, lastTS                              time.Time
	}
	var batch []upsertRow
	skippedNoJSONL := 0
	skippedNoOutcome := 0
	for _, r := range runs {
		if !r.HasOutcome {
			skippedNoOutcome++
			continue
		}
		pi := procInfos[r.ProcessID]
		// Hauptquelle: Transcript in /root/.claude/projects/*/<session_id>.jsonl
		path := ""
		if pi.SessionID != "" {
			path = claudePaths[pi.SessionID]
		}
		// Fallback: Hook-Stdout-JSONL (enthaelt keine Token-Usage, liefert aber
		// einen Pfad fuer die ExtractPEM-Klassifikation).
		if path == "" {
			if p, ok := processPaths[r.ProcessID]; ok {
				path = p
			}
		}
		var in, out, cc, cr int64
		if path != "" {
			in, out, cc, cr = sumTokensForProcess(path)
		} else {
			skippedNoJSONL++
		}
		model := pi.Model
		_, execPat := ExtractPEM(path)
		if model == "" && path != "" {
			model = FirstModel(path)
		}
		bucket := matchBucket(rules, "claude", execPat, model)
		outcome := classifyOutcome(r, runsByBead[r.Bead], beadStatus[r.Bead], maxRetryGap)
		batch = append(batch, upsertRow{
			bead: r.Bead, rig: r.Rig, bucket: bucket, model: model,
			processID: r.ProcessID, outcome: outcome,
			input: in, output: out, cacheCreate: cc, cacheRead: cr,
			firstTS: r.FirstTS, lastTS: r.LastTS,
		})
	}

	if dryRun {
		fmt.Printf("bead-attribute dry-run: %d rows would be upserted (skipped: no-jsonl=%d no-outcome=%d)\n",
			len(batch), skippedNoJSONL, skippedNoOutcome)
		return nil
	}

	// 8. Upsert in portfolio.bead_token_usage (idempotent via PK bead+process_id)
	written := 0
	for _, u := range batch {
		_, err := p.Exec(ctx, `
			INSERT INTO portfolio.bead_token_usage
			  (bead, rig, provider_bucket, model, process_id,
			   input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
			   outcome, first_ts, last_ts, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,now())
			ON CONFLICT (bead, process_id) DO UPDATE SET
			  rig = EXCLUDED.rig,
			  provider_bucket = EXCLUDED.provider_bucket,
			  model = EXCLUDED.model,
			  input_tokens = EXCLUDED.input_tokens,
			  output_tokens = EXCLUDED.output_tokens,
			  cache_creation_tokens = EXCLUDED.cache_creation_tokens,
			  cache_read_tokens = EXCLUDED.cache_read_tokens,
			  outcome = EXCLUDED.outcome,
			  first_ts = EXCLUDED.first_ts,
			  last_ts = EXCLUDED.last_ts,
			  updated_at = now()
		`, u.bead, u.rig, u.bucket, u.model, u.processID,
			u.input, u.output, u.cacheCreate, u.cacheRead,
			u.outcome, u.firstTS, u.lastTS)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bead-attribute: upsert %s/%s failed: %v\n", u.bead, u.processID, err)
			continue
		}
		written++
	}

	// 9. Done-Verifikation (PRD WP-2): ≥90% der completed-Events der letzten 7d
	// aus vk-cost.jsonl sind mit Token-Summen attribuiert.
	completedRecent, completedAttributed := 0, 0
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour)
	for _, u := range batch {
		if !u.lastTS.After(cutoff) {
			continue
		}
		if u.outcome == "errored" || u.outcome == "merged" || u.outcome == "unmerged" || u.outcome == "retry" {
			completedRecent++
			if u.input > 0 || u.output > 0 || u.cacheCreate > 0 || u.cacheRead > 0 {
				completedAttributed++
			}
		}
	}
	ratio := 0.0
	if completedRecent > 0 {
		ratio = float64(completedAttributed) / float64(completedRecent)
	}
	fmt.Printf("bead-attribute: %d rows written (skipped: no-jsonl=%d no-outcome=%d); "+
		"7d attribution: %d/%d completed-events attributed (%.1f%%, PRD-target ≥90%%)\n",
		written, skippedNoJSONL, skippedNoOutcome,
		completedAttributed, completedRecent, ratio*100)
	return nil
}

// matchBucket — dieselbe Logik wie fleet_parser, ohne Content-Substring
// (WP-1.5 Harte-Klassifikation: nur Pfad + model).
func matchBucket(rules []DiscoveryRule, process, executor, model string) string {
	for _, r := range rules {
		if r.Matches(process, executor, model) {
			return r.ProviderBucket
		}
	}
	return "other"
}
