// planfile-adapter — Plan-Files (docs/plans/*-prd.md) → Master-Kanban
//
// Edge-triggered: fsnotify auf den docs/plans-Verzeichnissen der Repos,
// initialer Voll-Scan als Dawn-Sync beim Start. Kein Intervall-Polling.
//
// Pro PRD: Frontmatter lesen (title, slug, status, layer), Initiative
// upserten (id = <firma-präfix>-<slug>, stage=idea nur bei Neuanlage),
// plan_file-Link anlegen (ref = absoluter Pfad), bei Status-Wechsel
// 'activity'-Event posten ('completed' wenn status=done).
//
// Konfiguration über env:
//   PLANFILE_REPOS    pfad=firma,pfad=firma,…   (Pflicht)
//   PORTFOLIO_DSN     Portfolio-Postgres
//   PORTFOLIO_API     master-kanban serve Basis-URL
//   PORTFOLIO_API_KEY X-Api-Key für /api/events
//
// Usage:
//   planfile-adapter --once    (Dawn-Sync, exit)
//   planfile-adapter --watch   (Dawn-Sync + fsnotify, langlaufend)

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"
)

var (
	dsn     = envOr("PORTFOLIO_DSN", "postgres://mario@127.0.0.1:5434/mario_brain?sslmode=disable")
	stDSN   = envOr("SOLARTOWN_DSN", "postgres://remote:remote@127.0.0.1:5433/solartown_clean?sslmode=disable")
	apiURL  = envOr("PORTFOLIO_API", "http://127.0.0.1:7780")
	apiKey  = envOr("PORTFOLIO_API_KEY", "dev-secret")
	once    = flag.Bool("once", false, "Dawn-Sync + exit")
	watch   = flag.Bool("watch", false, "Dawn-Sync + fsnotify, langlaufend")
	stPool  *pgxpool.Pool
	Version string = "dev"
)

// emitTown — Event in den Town-Strom (:5433 town.events, PRD A5/P3.1).
// Miss ist nicht blockierend: Board-Sync läuft weiter, Event geht verloren
// und wird beim nächsten Status-Edge nachgeholt.
func emitTown(kind string, payload map[string]any) {
	if stPool == nil {
		p, err := pgxpool.New(context.Background(), stDSN)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ town-emit connect: %v\n", err)
			return
		}
		stPool = p
	}
	b, _ := json.Marshal(payload)
	if _, err := stPool.Exec(context.Background(),
		`SELECT town.emit($1, $2::jsonb, 'planfile-adapter')`, kind, string(b)); err != nil {
		fmt.Fprintf(os.Stderr, "  ✗ town-emit %s: %v\n", kind, err)
	}
}

var firmaPrefix = map[string]string{
	"stayawesome": "sa", "quantbot": "qb", "solartown": "st",
	"mariobrain": "mb", "angeloos": "ag", "stack": "sk",
}

type repo struct {
	root  string // Repo-Wurzel
	firma string
}

type frontmatter struct {
	Title      string `yaml:"title"`
	Slug       string `yaml:"slug"`
	Status     string `yaml:"status"`
	Layer      string `yaml:"layer"`
	ParentPlan string `yaml:"parent_plan"`
}

func main() {
	flag.Parse()
	if !*once && !*watch {
		*once = true
	}

	repos, err := parseRepos(os.Getenv("PLANFILE_REPOS"))
	if err != nil {
		die("config", err)
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		die("connect", err)
	}
	defer pool.Close()

	// Dawn-Sync: einmal alles abgleichen
	for _, r := range repos {
		scanRepo(pool, r)
	}
	if *once {
		return
	}

	// Edge-Trigger: fsnotify auf den plans-Verzeichnissen
	w, err := fsnotify.NewWatcher()
	if err != nil {
		die("fsnotify", err)
	}
	defer w.Close()
	byDir := map[string]repo{}
	for _, r := range repos {
		dir := filepath.Join(r.root, "docs", "plans")
		if err := w.Add(dir); err != nil {
			fmt.Fprintf(os.Stderr, "watch %s: %v\n", dir, err)
			continue
		}
		byDir[dir] = r
		fmt.Println("watching", dir)
	}

	// Debounce pro Datei: Editoren feuern mehrere Events pro Save
	pending := map[string]repo{}
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if !strings.HasSuffix(ev.Name, ".md") || ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) == 0 {
				continue
			}
			pending[ev.Name] = byDir[filepath.Dir(ev.Name)]
			timer.Reset(2 * time.Second)
		case <-timer.C:
			for path, r := range pending {
				if _, err := os.Stat(path); err == nil {
					syncFile(pool, r, path)
				}
			}
			pending = map[string]repo{}
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			fmt.Fprintln(os.Stderr, "fsnotify:", err)
		}
	}
}

func parseRepos(spec string) ([]repo, error) {
	if spec == "" {
		return nil, fmt.Errorf("PLANFILE_REPOS ist leer (erwartet pfad=firma,…)")
	}
	var out []repo
	for _, part := range strings.Split(spec, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 || firmaPrefix[kv[1]] == "" {
			return nil, fmt.Errorf("ungültiger Eintrag %q (pfad=firma, firma ∈ sa/qb/st/mb/ag-Familie)", part)
		}
		out = append(out, repo{root: kv[0], firma: kv[1]})
	}
	return out, nil
}

func scanRepo(p *pgxpool.Pool, r repo) {
	dir := filepath.Join(r.root, "docs", "plans")
	matches, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	for _, path := range matches {
		syncFile(p, r, path)
	}
}

func syncFile(p *pgxpool.Pool, r repo, path string) {
	syncFileRec(p, r, path, map[string]bool{})
}

// normalizeParent: "(none)" / "none" / "null" / "-" → leer
func normalizeParent(pp string) string {
	v := strings.TrimSpace(pp)
	switch strings.ToLower(v) {
	case "", "(none)", "none", "null", "-", "~":
		return ""
	}
	return v
}

// resolveParentPath: parent_plan kann absolut, repo-relativ oder bloßer
// Dateiname (gleicher Ordner) sein.
func resolveParentPath(r repo, childPath, pp string) string {
	var cand string
	switch {
	case filepath.IsAbs(pp):
		cand = pp
	case strings.Contains(pp, "/"):
		cand = filepath.Join(r.root, pp)
	default:
		cand = filepath.Join(filepath.Dir(childPath), pp)
	}
	if _, err := os.Stat(cand); err != nil {
		return ""
	}
	return cand
}

// syncFileRec gibt die plan_item-id zurück ("" wenn übersprungen).
// Parents werden vor Kindern gesynct (Rekursion, Zyklen via seen-Set gekappt).
func syncFileRec(p *pgxpool.Pool, r repo, path string, seen map[string]bool) string {
	if seen[path] {
		return ""
	}
	seen[path] = true

	fm, err := readFrontmatter(path)
	if err != nil {
		// Nur bei PRDs laut sein — docs/plans enthält auch frontmatter-lose Notizen
		if strings.HasSuffix(path, "-prd.md") {
			fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", path, err)
		}
		return ""
	}
	if fm.Slug == "" {
		fm.Slug = strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".md"), "-prd")
	}
	if fm.Title == "" {
		fm.Title = fm.Slug
	}
	id := firmaPrefix[r.firma] + "-" + fm.Slug
	ctx := context.Background()

	// Parent zuerst syncen, Initiative vom Parent erben
	parentID := ""
	initiativeID := id
	if pp := normalizeParent(fm.ParentPlan); pp != "" {
		if ppath := resolveParentPath(r, path, pp); ppath != "" {
			parentID = syncFileRec(p, r, ppath, seen)
		}
		if parentID == "" {
			// Parent evtl. schon aus früherem Lauf in der DB (z.B. anderes Repo)
			pslug := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(pp), ".md"), "-prd")
			_ = p.QueryRow(ctx, `SELECT id FROM portfolio.plan_item WHERE slug=$1 LIMIT 1`, pslug).Scan(&parentID)
		}
	}
	if parentID != "" {
		if err := p.QueryRow(ctx, `SELECT initiative_id FROM portfolio.plan_item WHERE id=$1`, parentID).Scan(&initiativeID); err != nil {
			parentID, initiativeID = "", id
		}
	}

	isRootCard := parentID == "" &&
		(strings.HasSuffix(path, "-prd.md") || fm.Layer == "prd" || fm.Layer == "vision" || fm.Layer == "roadmap")
	if parentID == "" && !isRootCard {
		return "" // lose Notiz ohne Eltern und ohne Karten-Layer — kein Board-Material
	}

	created := false
	if isRootCard {
		// Initiative nur anlegen, nie verschieben — Stage-Hoheit bleibt beim Board
		tag, err := p.Exec(ctx,
			`INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend)
			 VALUES ($1,$2,'idea',$3,'plan_file') ON CONFLICT (id) DO NOTHING`,
			id, r.firma, fm.Title)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ upsert %s: %v\n", id, err)
			return ""
		}
		created = tag.RowsAffected() > 0
	}

	// plan_item upsert — die Baum-Projektion (P4.1)
	if _, err := p.Exec(ctx,
		`INSERT INTO portfolio.plan_item (id, initiative_id, parent_id, slug, layer, status, title, repo, path)
		 VALUES ($1,$2,NULLIF($3,''),$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (id) DO UPDATE SET initiative_id=EXCLUDED.initiative_id,
		   parent_id=EXCLUDED.parent_id, layer=EXCLUDED.layer, status=EXCLUDED.status,
		   title=EXCLUDED.title, repo=EXCLUDED.repo, path=EXCLUDED.path, updated_at=now()`,
		id, initiativeID, parentID, fm.Slug, fm.Layer, fm.Status, fm.Title, r.root, path); err != nil {
		fmt.Fprintf(os.Stderr, "  ✗ plan_item %s: %v\n", id, err)
		return ""
	}

	// Link existenz-geprüft anlegen (initiative_link hat keinen Unique-Constraint)
	var dummy int
	err = p.QueryRow(ctx,
		`SELECT 1 FROM portfolio.initiative_link WHERE initiative_id=$1 AND kind='plan_file' AND ref=$2`,
		initiativeID, path).Scan(&dummy)
	if err != nil {
		if _, err := p.Exec(ctx,
			`INSERT INTO portfolio.initiative_link (initiative_id, kind, ref) VALUES ($1,'plan_file',$2)`,
			initiativeID, path); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ link %s: %v\n", initiativeID, err)
			return id
		}
	}

	// Event nur bei Status-Übergang (edge), nicht bei jedem Scan
	var lastStatus string
	_ = p.QueryRow(ctx,
		`SELECT payload->>'plan_status' FROM portfolio.initiative_event
		 WHERE source_backend='plan_file' AND payload->>'ref'=$1
		 ORDER BY at DESC LIMIT 1`, path).Scan(&lastStatus)
	if lastStatus == fm.Status && !created {
		return id
	}
	        kind := "activity"
	        if fm.Status == "done" {
	                kind = "completed"
	        }
	        payload := map[string]any{
	                "plan_status": fm.Status, "slug": fm.Slug, "layer": fm.Layer,
	                "ref": path, "title": fm.Title, "plan_item": id,
	        }
	        if err := postEvent(initiativeID, kind, payload); err != nil {
	                fmt.Fprintf(os.Stderr, "  ✗ post %s: %v\n", initiativeID, err)
	                return id
	        }
	
	        // P2.1 Auto-Stage Verdrahtung
	        if fm.Status == "approved" {
	                _ = postEvent(initiativeID, "stage_proposed", map[string]any{"stage": "soon"})
	        }
	
	                // P3.1 — Status-Edge in den Town-Strom (Decomposer + Auto-Reviewer hören hier)
	                emitTown("plan.status-changed", map[string]any{
		"plan_item": id, "slug": fm.Slug, "layer": fm.Layer,
		"old": lastStatus, "new": fm.Status,
		"repo": r.root, "path": path, "title": fm.Title, "initiative": initiativeID,
	})
	fmt.Printf("  ✓ %s → %s (status=%s, created=%v)\n", filepath.Base(path), initiativeID, fm.Status, created)
	return id
}

func readFrontmatter(path string) (*frontmatter, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := string(raw)
	// Führende HTML-Kommentare (z.B. WORD_HYGIENE_EXEMPT-Marker) und
	// Leerzeilen überspringen — das Frontmatter folgt direkt danach.
	for {
		s = strings.TrimLeft(s, "\n")
		if strings.HasPrefix(s, "<!--") {
			end := strings.Index(s, "-->")
			if end < 0 {
				return nil, fmt.Errorf("HTML-Kommentar nicht terminiert")
			}
			s = s[end+3:]
			continue
		}
		break
	}
	if !strings.HasPrefix(s, "---\n") {
		return nil, fmt.Errorf("kein Frontmatter")
	}
	end := strings.Index(s[4:], "\n---")
	if end < 0 {
		return nil, fmt.Errorf("Frontmatter nicht terminiert")
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(s[4:4+end]), &fm); err != nil {
		return nil, err
	}
	return &fm, nil
}

func postEvent(initiative, kind string, payload map[string]any) error {
	body := map[string]any{
		"initiative_id":  initiative,
		"kind":           kind,
		"source_backend": "plan_file",
		"payload":        payload,
		"actor":          "planfile-adapter",
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
