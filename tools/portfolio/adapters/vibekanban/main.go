// vibekanban-adapter — vibe-kanban Task-Status → Master-Kanban
//
// Manuelles Linken (Mario-Entscheid 2026-06-12): Links entstehen bewusst via
//   master-kanban link <initiative> vk_workspace <task-uuid>
// Dieser Adapter pusht ausschließlich Status-Übergänge der gelinkten Tasks —
// kein Auto-Link, kein Board-Spam.
//
// Edge-triggered: fsnotify auf der sqlite-WAL von vibe-kanban, Dawn-Sync beim
// Start. Lesezugriff per Shell-out zu sqlite3 -readonly (kein DB-Treiber im
// Backend-Bestand, analog solartown-adapter → bd-CLI).
//
// ref-Format: Task-UUID lowercase mit Bindestrichen
// (Task-ids liegen in vibe-kanban als 16-Byte-BLOB).
//
// Usage:
//   vibekanban-adapter --once    (Dawn-Sync, exit)
//   vibekanban-adapter --watch   (Dawn-Sync + fsnotify, langlaufend)

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
	"regexp"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	dsn     = envOr("PORTFOLIO_DSN", "postgres://mario@127.0.0.1:5434/mario_brain?sslmode=disable")
	apiURL  = envOr("PORTFOLIO_API", "http://127.0.0.1:7780")
	apiKey  = envOr("PORTFOLIO_API_KEY", "dev-secret")
	vkDB    = envOr("VK_DB", "/root/.local/share/vibe-kanban/db.v2.sqlite")
	once    = flag.Bool("once", false, "Dawn-Sync + exit")
	watch   = flag.Bool("watch", false, "Dawn-Sync + fsnotify, langlaufend")
	Version string = "dev"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F-]{32,36}$`)

func main() {
	flag.Parse()
	if !*once && !*watch {
		*once = true
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		die("connect", err)
	}
	defer pool.Close()

	scan(pool)
	if *once {
		return
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		die("fsnotify", err)
	}
	defer w.Close()
	// Verzeichnis watchen: sqlite rotiert -wal/-shm, Datei-Watches reißen ab
	if err := w.Add(filepath.Dir(vkDB)); err != nil {
		die("watch", err)
	}
	fmt.Println("watching", filepath.Dir(vkDB))

	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if !strings.HasPrefix(filepath.Base(ev.Name), filepath.Base(vkDB)) {
				continue
			}
			timer.Reset(2 * time.Second)
		case <-timer.C:
			scan(pool)
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			fmt.Fprintln(os.Stderr, "fsnotify:", err)
		}
	}
}

func scan(p *pgxpool.Pool) {
	rows, err := p.Query(context.Background(),
		`SELECT initiative_id, ref FROM portfolio.initiative_link WHERE kind='vk_workspace'`)
	if err != nil {
		fmt.Fprintln(os.Stderr, "links:", err)
		return
	}
	defer rows.Close()
	type link struct{ initiative, ref string }
	var links []link
	for rows.Next() {
		var l link
		if err := rows.Scan(&l.initiative, &l.ref); err == nil {
			links = append(links, l)
		}
	}
	for _, l := range links {
		status, title, err := readTask(l.ref)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s (%s): %v\n", l.ref, l.initiative, err)
			continue
		}
		var last string
		_ = p.QueryRow(context.Background(),
			`SELECT payload->>'vk_status' FROM portfolio.initiative_event
			 WHERE source_backend='vk' AND payload->>'ref'=$1
			 ORDER BY at DESC LIMIT 1`, l.ref).Scan(&last)
		if last == status {
			continue
		}
		kind := "activity"
		if status == "done" {
			kind = "completed"
		}
		payload := map[string]any{"vk_status": status, "ref": l.ref, "title": title}
		if err := postEvent(l.initiative, kind, payload); err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ post %s: %v\n", l.ref, err)
			continue
		}
		fmt.Printf("  ✓ %s → %s (%s)\n", l.ref, l.initiative, status)
	}
}

func readTask(ref string) (status, title string, err error) {
	if !uuidRe.MatchString(ref) {
		return "", "", fmt.Errorf("ref ist keine UUID")
	}
	hexID := strings.ToUpper(strings.ReplaceAll(ref, "-", ""))
	out, err := exec.Command("sqlite3", "-readonly", "-separator", "\x1f", vkDB,
		fmt.Sprintf("SELECT status, title FROM tasks WHERE hex(id)='%s';", hexID)).Output()
	if err != nil {
		return "", "", err
	}
	line := strings.TrimRight(string(out), "\n")
	if line == "" {
		return "", "", fmt.Errorf("task nicht gefunden")
	}
	parts := strings.SplitN(line, "\x1f", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("unerwartetes sqlite-Format")
	}
	return parts[0], parts[1], nil
}

func postEvent(initiative, kind string, payload map[string]any) error {
	body := map[string]any{
		"initiative_id":  initiative,
		"kind":           kind,
		"source_backend": "vk",
		"payload":        payload,
		"actor":          "vibekanban-adapter",
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
