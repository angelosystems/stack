// solartown-adapter — MVP
//
// Liest die initiative_link-Tabelle für kind=bead, ruft `bd show <ref>`
// auf der CLI auf (shell-out, kein direkter solartown-Postgres-Connect),
// vergleicht status, postet einen 'activity'- oder 'completed'-Event ans
// /api/events-Endpoint von master-kanban serve.
//
// Backend-agnostische Konvention: keine direkte DB-Berührung, nur
// HTTP push gemäß Plan Stage 2.5.
//
// Usage:
//   solartown-adapter --once    (einmal scannen, exit)
//   solartown-adapter --watch   (alle 60s scannen, langlaufend — Altmodus)
//   solartown-adapter --listen  (edge-triggered: LISTEN bead_created/bead_closed,
//                                Dawn-Sync beim Connect, kein Polling)

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	dsn             = envOr("PORTFOLIO_DSN", "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable")
	apiURL          = envOr("PORTFOLIO_API", "http://127.0.0.1:7770")
	apiKey          = envOr("PORTFOLIO_API_KEY", "dev-secret")
	bdRig           = envOr("BD_RIG", "/opt/solartown")
	beadsDSN        = envOr("BEADS_DSN", "postgres://remote:remote@127.0.0.1:5433/solartown_clean")
	once            = flag.Bool("once", false, "single scan + exit")
	watch           = flag.Bool("watch", false, "loop forever, scan every interval")
	listen          = flag.Bool("listen", false, "edge-triggered via beads-NOTIFY")
	link            = flag.Bool("link", false, "auto-link mode scanning all rigs")
	interval        = flag.Duration("interval", 60*time.Second, "watch interval")
	Version  string = "dev"
)

type beadStatus struct {
	id     string
	status string // open | closed | in_progress
	title  string
}

func main() {
	flag.Parse()
	initRegistry()
	if !*once && !*watch && !*listen && !*link {
		*once = true
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		die("connect", err)
	}
	defer pool.Close()

	if *link {
		if err := runLink(pool); err != nil {
			die("link error", err)
		}
		return
	}

	if *listen {
		listenLoop(pool)
		return
	}

	for {
		if err := runOnce(pool); err != nil {
			fmt.Fprintln(os.Stderr, "scan error:", err)
		}
		if *once {
			return
		}
		time.Sleep(*interval)
	}
}

// listenLoop: edge-triggered Betrieb. Verbindet zur beads-Postgres, LISTEN
// auf bead_created/bead_closed; jede Notification triggert einen Scan
// (debounced). Beim (Re-)Connect ein Dawn-Sync als Catch-Up. Kein Intervall.
func listenLoop(pool *pgxpool.Pool) {
	for {
		conn, err := pgx.Connect(context.Background(), beadsDSN)
		if err != nil {
			fmt.Fprintln(os.Stderr, "beads connect:", err)
			time.Sleep(5 * time.Second) // Reconnect-Backoff, kein Scan-Timer
			continue
		}
		for _, ch := range []string{"bead_created", "bead_closed"} {
			if _, err := conn.Exec(context.Background(), "LISTEN "+ch); err != nil {
				fmt.Fprintln(os.Stderr, "listen:", err)
			}
		}
		fmt.Println("listening on bead_created/bead_closed — Dawn-Sync")
		if err := runOnce(pool); err != nil {
			fmt.Fprintln(os.Stderr, "dawn-sync:", err)
		}
		for {
			n, err := conn.WaitForNotification(context.Background())
			if err != nil {
				fmt.Fprintln(os.Stderr, "notification:", err)
				_ = conn.Close(context.Background())
				break // außen neu verbinden + Dawn-Sync als Catch-Up
			}
			fmt.Printf("notify %s → scan\n", n.Channel)
			// kurzes Sammelfenster: Bead-Wellen lösen nur einen Scan aus
			time.Sleep(2 * time.Second)
			drainNotifications(conn)
			if err := runOnce(pool); err != nil {
				fmt.Fprintln(os.Stderr, "scan error:", err)
			}
		}
	}
}

func drainNotifications(conn *pgx.Conn) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, err := conn.WaitForNotification(ctx)
		cancel()
		if err != nil {
			return
		}
	}
}

func runOnce(p *pgxpool.Pool) error {
	if err := runLink(p); err != nil {
		fmt.Fprintln(os.Stderr, "auto-link error (skipping):", err)
	}

	rows, err := p.Query(context.Background(),
		`SELECT initiative_id, ref FROM portfolio.initiative_link WHERE kind='bead'`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var pairs []struct{ initiative, beadRef string }
	for rows.Next() {
		var iid, ref string
		if err := rows.Scan(&iid, &ref); err != nil {
			return err
		}
		pairs = append(pairs, struct{ initiative, beadRef string }{iid, ref})
	}
	if len(pairs) == 0 {
		fmt.Println("no bead-links to scan")
		return nil
	}

	fmt.Printf("scanning %d bead-links via bd CLI and Rig-Registry\n", len(pairs))
	pushed := 0
	byInitiative := make(map[string][]*beadStatus)

	for _, x := range pairs {
		st, err := readBead(x.beadRef)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s (%s): %v\n", x.beadRef, x.initiative, err)
			continue
		}
		byInitiative[x.initiative] = append(byInitiative[x.initiative], st)

		kind := "activity"
		if st.status == "closed" {
			kind = "completed"
		}
		payload := map[string]any{"bead_status": st.status, "bead_title": st.title, "ref": x.beadRef}
		if err := postEvent(x.initiative, kind, payload); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ post %s (%s): %v\n", x.beadRef, x.initiative, err)
			continue
		}
		fmt.Printf("  ✓ %s → %s (%s, %s)\n", x.beadRef, x.initiative, kind, st.status)
		pushed++
	}

	// P2.1 Auto-Stage Verdrahtung
	for initID, beads := range byInitiative {
		allClosed := true
		anyInProgress := false
		for _, b := range beads {
			if b.status == "in_progress" {
				anyInProgress = true
			}
			if b.status != "closed" {
				allClosed = false
			}
		}
		var proposed string
		if allClosed && len(beads) > 0 {
			proposed = "watching"
		} else if anyInProgress {
			proposed = "now"
		}

		if proposed != "" {
			_ = postEvent(initID, "stage_proposed", map[string]any{"stage": proposed})
		}
	}

	fmt.Printf("pushed %d/%d events\n", pushed, len(pairs))
	return nil
}

func readBead(id string) (*beadStatus, error) {
	rigDir := bdRig
	if reg != nil {
		if r, ok := reg.Resolve(id); ok {
			rigDir = r.Dir
		}
	}

	cmd := exec.Command("bd", "show", id, "--json")
	cmd.Dir = rigDir
	out, err := cmd.Output()
	if err != nil {
		// Try without --json (not all bd versions support it)
		cmd2 := exec.Command("bd", "show", id)
		cmd2.Dir = rigDir
		out2, err2 := cmd2.Output()
		if err2 != nil {
			return nil, err
		}
		return parsePlain(out2, id), nil
	}
	var meta struct {
		Id, Status, Title string
	}
	if err := json.Unmarshal(out, &meta); err != nil {
		return parsePlain(out, id), nil
	}
	return &beadStatus{id: meta.Id, status: meta.Status, title: meta.Title}, nil
}

func parsePlain(out []byte, id string) *beadStatus {
	s := string(out)
	st := "open"
	if strings.Contains(s, "CLOSED") {
		st = "closed"
	} else if strings.Contains(s, "IN_PROGRESS") {
		st = "in_progress"
	}
	title := id
	for _, line := range strings.Split(s, "\n") {
		// Format example: "○ st-bzi7 · Master-Kanban: ... [● P2 · OPEN]"
		if i := strings.Index(line, "·"); i > 0 {
			rest := line[i+1:]
			if j := strings.Index(rest, "["); j > 0 {
				title = strings.TrimSpace(rest[:j])
				break
			}
		}
	}
	return &beadStatus{id: id, status: st, title: title}
}

func postEvent(initiative, kind string, payload map[string]any) error {
	body := map[string]any{
		"initiative_id":  initiative,
		"kind":           kind,
		"source_backend": "solartown",
		"payload":        payload,
		"actor":          "solartown-adapter",
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", apiURL+"/api/events", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
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

type beadRow struct {
	ID        string   `json:"id"`
	SpecID    string   `json:"spec_id"`
	Labels    []string `json:"labels"`
	Status    string   `json:"status"`
	Ephemeral bool     `json:"ephemeral"`
	Title     string   `json:"title"`
}

func scanRigBeads(p *pgxpool.Pool, slugToInitiative map[string]string, rig Rig, linkedBeads map[string]bool) (total, newly, linked, orphaned int, err error) {
	cmd := exec.Command("bd", "list", "--json")
	cmd.Dir = rig.Dir
	out, err := cmd.Output()
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("bd list error: %w", err)
	}

	var beads []beadRow
	if err := json.Unmarshal(out, &beads); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("json unmarshal error: %w", err)
	}

	for _, b := range beads {
		total++

		beadSlug := getJoinKey(b.SpecID, b.Labels)
		if beadSlug != "" {
			beadSlug = strings.ToLower(beadSlug)

			if initiativeID, ok := slugToInitiative[beadSlug]; ok {
				tag, err := p.Exec(context.Background(),
					`INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
					 VALUES ($1, 'bead', $2)
					 ON CONFLICT (initiative_id, kind, ref) DO NOTHING`,
					initiativeID, b.ID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  ✗ failed to link bead %s to %s: %v\n", b.ID, initiativeID, err)
				} else {
					if tag.RowsAffected() > 0 {
						newly++
						fmt.Printf("  ✓ [%s] auto-linked bead %s to initiative %s (slug: %s)\n", rig.Prefix, b.ID, initiativeID, beadSlug)
					} else {
						linked++
					}
					linkedBeads[b.ID] = true // Mark as linked
				}
			} else {
				orphaned++
				fmt.Printf("[ORPHAN] [%s] Bead %s has join-key %q but matches no initiative\n", rig.Prefix, b.ID, beadSlug)
			}
		}

		// Leak detector: check if bead is unlinked (open, non-ephemeral, and not linked)
		if b.Status != "closed" && !b.Ephemeral {
			if !linkedBeads[b.ID] {
				firma := getFirmaForRig(rig.Prefix)
				title := b.Title
				if title == "" {
					title = b.ID
				}
				_, err = p.Exec(context.Background(),
					`INSERT INTO portfolio.unlinked_item (id, kind, title, firma, rig_prefix, join_key)
					 VALUES ($1, 'bead', $2, $3, $4, $5)
					 ON CONFLICT (id) DO UPDATE SET
					    kind = EXCLUDED.kind,
					    title = EXCLUDED.title,
					    firma = EXCLUDED.firma,
					    rig_prefix = EXCLUDED.rig_prefix,
					    join_key = EXCLUDED.join_key,
					    discovered_at = now()`,
					b.ID, title, firma, rig.Prefix, sqlNullString(beadSlug))
				if err != nil {
					fmt.Fprintf(os.Stderr, "  ✗ failed to record unlinked bead %s: %v\n", b.ID, err)
				}
			}
		}
	}
	return
}

func runLink(p *pgxpool.Pool) error {
	// 1. Clear unlinked_items table
	_, err := p.Exec(context.Background(), `DELETE FROM portfolio.unlinked_item`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠ failed to clear portfolio.unlinked_item: %v\n", err)
	}

	rows, err := p.Query(context.Background(),
		`SELECT initiative_id, ref FROM portfolio.initiative_link WHERE kind='plan_file' ORDER BY added_at ASC`)
	if err != nil {
		return fmt.Errorf("query plan_file links: %w", err)
	}
	defer rows.Close()

	slugToInitiative := make(map[string]string)
	for rows.Next() {
		var initiativeID, ref string
		if err := rows.Scan(&initiativeID, &ref); err != nil {
			return fmt.Errorf("scan plan_file link: %w", err)
		}
		slug := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(ref), ".md"), "-prd")
		slug = strings.ToLower(slug)
		if slug == "" {
			continue
		}
		if existing, ok := slugToInitiative[slug]; ok {
			if existing != initiativeID {
				fmt.Printf("[CONFLICT] Multiple initiatives match slug %q: %q and %q. First one %q wins.\n", slug, existing, initiativeID, existing)
			}
		} else {
			slugToInitiative[slug] = initiativeID
		}
	}

	if reg == nil {
		return fmt.Errorf("rig-registry not initialized")
	}

	// Load existing linked beads and workspaces
	linkedBeads := make(map[string]bool)
	linkedWorkspaces := make(map[string]bool)

	linkRows, err := p.Query(context.Background(), `SELECT kind, ref FROM portfolio.initiative_link WHERE kind IN ('bead', 'vk_workspace')`)
	if err == nil {
		defer linkRows.Close()
		for linkRows.Next() {
			var kind, ref string
			if err := linkRows.Scan(&kind, &ref); err == nil {
				if kind == "bead" {
					linkedBeads[ref] = true
				} else if kind == "vk_workspace" {
					linkedWorkspaces[ref] = true
				}
			}
		}
	} else {
		fmt.Fprintf(os.Stderr, "  ⚠ failed to query existing links: %v\n", err)
	}

	totalBeads := 0
	newlyLinked := 0
	alreadyLinked := 0
	totalOrphaned := 0
	var unreachableRigs []string

	scanOrder := []string{"st", "tr", "qu", "sk", "sa", "so", "cl", "ag", "mb"}
	for _, prefix := range scanOrder {
		rig, ok := reg.Get(prefix)
		if !ok {
			// Skipped/not in registry rig prefix is treated as unreachable (denominator honesty)
			unreachableRigs = append(unreachableRigs, prefix)

			firma := getFirmaForRig(prefix)
			rigID := "rig:" + prefix
			title := "nicht erfasst — Quelle unerreichbar"

			_, dbErr := p.Exec(context.Background(),
				`INSERT INTO portfolio.unlinked_item (id, kind, title, firma, rig_prefix, join_key)
				 VALUES ($1, 'rig', $2, $3, $4, NULL)
				 ON CONFLICT (id) DO UPDATE SET
				    kind = EXCLUDED.kind,
				    title = EXCLUDED.title,
				    firma = EXCLUDED.firma,
				    rig_prefix = EXCLUDED.rig_prefix,
				    join_key = EXCLUDED.join_key,
				    discovered_at = now()`,
				rigID, title, firma, prefix)
			if dbErr != nil {
				fmt.Fprintf(os.Stderr, "  ✗ failed to record skipped rig %s: %v\n", prefix, dbErr)
			}
			continue
		}
		fmt.Printf("  scanning rig %s (prefix %s)\n", rig.Dir, rig.Prefix)
		t, n, al, o, err := scanRigBeads(p, slugToInitiative, rig, linkedBeads)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ skip rig %s (prefix %s): %v\n", rig.Dir, rig.Prefix, err)
			unreachableRigs = append(unreachableRigs, rig.Prefix)

			firma := getFirmaForRig(rig.Prefix)
			rigID := "rig:" + rig.Prefix
			title := "nicht erfasst — Quelle unerreichbar"

			_, dbErr := p.Exec(context.Background(),
				`INSERT INTO portfolio.unlinked_item (id, kind, title, firma, rig_prefix, join_key)
				 VALUES ($1, 'rig', $2, $3, $4, NULL)
				 ON CONFLICT (id) DO UPDATE SET
				    kind = EXCLUDED.kind,
				    title = EXCLUDED.title,
				    firma = EXCLUDED.firma,
				    rig_prefix = EXCLUDED.rig_prefix,
				    join_key = EXCLUDED.join_key,
				    discovered_at = now()`,
				rigID, title, firma, rig.Prefix)
			if dbErr != nil {
				fmt.Fprintf(os.Stderr, "  ✗ failed to record unreachable rig %s: %v\n", rig.Prefix, dbErr)
			}
			continue
		}
		totalBeads += t
		newlyLinked += n
		alreadyLinked += al
		totalOrphaned += o
	}

	// 2. Scan unlinked vk-Workspaces
	if err := scanUnlinkedWorkspaces(p, linkedWorkspaces); err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠ failed to scan unlinked workspaces: %v\n", err)
	}

	// 3. Update detector status (heartbeat)
	detectorStatus := "healthy"
	if len(unreachableRigs) > 0 {
		detectorStatus = "warning"
	}
	_, err = p.Exec(context.Background(),
		`INSERT INTO portfolio.detector_status (id, last_run, status, unreachable_rigs, error_message)
		 VALUES ('leak-detector', now(), $1, $2, NULL)
		 ON CONFLICT (id) DO UPDATE SET
		    last_run = EXCLUDED.last_run,
		    status = EXCLUDED.status,
		    unreachable_rigs = EXCLUDED.unreachable_rigs,
		    error_message = EXCLUDED.error_message`,
		detectorStatus, unreachableRigs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ✗ failed to update detector status: %v\n", err)
	}

	fmt.Printf("Auto-link scan completed (%d rigs): %d beads processed, %d newly linked, %d already linked, %d orphaned.\n",
		len(scanOrder), totalBeads, newlyLinked, alreadyLinked, totalOrphaned)

	return nil
}

func maskDSN(dsn string) string {
	idx := strings.Index(dsn, "://")
	if idx < 0 {
		return dsn
	}
	after := dsn[idx+3:]
	if at := strings.Index(after, "@"); at >= 0 {
		return dsn[:idx+3] + "***" + after[at:]
	}
	return dsn[:idx+3] + "***"
}

func getJoinKey(specID string, labels []string) string {
	if specID != "" {
		slug := slugFromSpecID(specID)
		if slug != "" {
			return slug
		}
	}
	for _, lbl := range labels {
		if strings.HasPrefix(lbl, "plan:") {
			return strings.TrimPrefix(lbl, "plan:")
		}
	}
	return ""
}

func slugFromSpecID(specID string) string {
	if specID == "" {
		return ""
	}
	base := filepath.Base(specID)
	base = strings.ToLower(base)
	base = strings.TrimSuffix(base, ".md")
	base = strings.TrimSuffix(base, "-prd")
	return base
}

func sqlNullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func getFirmaForRig(prefix string) string {
	switch prefix {
	case "sa", "so":
		return "stayawesome"
	case "st", "tr":
		return "solartown"
	case "qu":
		return "quantbot"
	case "mb":
		return "mariobrain"
	case "ag", "cl":
		return "angeloos"
	case "sk":
		return "stack"
	default:
		return "solartown"
	}
}

func hexToUUID(h string) string {
	h = strings.ToLower(h)
	if len(h) != 32 {
		return h
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

func parseWorkspaceMetadata(name, branch string) (rigPrefix, firma string) {
	knownPrefixes := []string{"st", "tr", "qu", "sk", "sa", "so", "cl", "ag", "mb"}
	lowerBranch := strings.ToLower(branch)
	lowerName := strings.ToLower(name)

	for _, prefix := range knownPrefixes {
		if strings.Contains(lowerBranch, prefix+"-") || strings.Contains(lowerBranch, "/"+prefix) ||
			strings.Contains(lowerName, "sol-"+prefix) || strings.Contains(lowerName, "bd/"+prefix) ||
			strings.Contains(lowerName, "["+prefix+"-") {
			rigPrefix = prefix
			break
		}
	}

	if rigPrefix == "" {
		for _, prefix := range knownPrefixes {
			if strings.Contains(lowerBranch, prefix) || strings.Contains(lowerName, prefix) {
				rigPrefix = prefix
				break
			}
		}
	}

	if rigPrefix == "" {
		rigPrefix = "st"
	}

	return rigPrefix, getFirmaForRig(rigPrefix)
}

func scanUnlinkedWorkspaces(p *pgxpool.Pool, linkedWorkspaces map[string]bool) error {
	vkDB := envOr("VK_DB", "/root/.local/share/vibe-kanban/db.v2.sqlite")
	if _, err := os.Stat(vkDB); os.IsNotExist(err) {
		return nil
	}

	cmd := exec.Command("sqlite3", "-readonly", "-separator", "\x1f", vkDB,
		"SELECT hex(id), name, branch FROM workspaces WHERE archived = 0 AND worktree_deleted = 0;")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("sqlite query workspaces: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x1f")
		if len(parts) != 3 {
			continue
		}
		hexID := parts[0]
		name := parts[1]
		branch := parts[2]

		uuid := hexToUUID(hexID)
		if !linkedWorkspaces[uuid] {
			rigPrefix, firma := parseWorkspaceMetadata(name, branch)
			title := name
			if title == "" {
				title = fmt.Sprintf("Workspace %s", uuid)
			}

			_, err = p.Exec(context.Background(),
				`INSERT INTO portfolio.unlinked_item (id, kind, title, firma, rig_prefix, join_key)
				 VALUES ($1, 'vk_workspace', $2, $3, $4, NULL)
				 ON CONFLICT (id) DO UPDATE SET
				    kind = EXCLUDED.kind,
				    title = EXCLUDED.title,
				    firma = EXCLUDED.firma,
				    rig_prefix = EXCLUDED.rig_prefix,
				    join_key = EXCLUDED.join_key,
				    discovered_at = now()`,
				uuid, title, firma, rigPrefix)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ✗ failed to insert unlinked workspace %s: %v\n", uuid, err)
			}
		}
	}
	return nil
}
