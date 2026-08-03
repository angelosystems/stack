package main

// dispatch_planfile.go — Fix des Halluzinations-Dispatchs (Befund 2026-08-03).
//
// Vorfall: cf-mk-autopilot-stufe2 wurde mit einem verlinkten, 202-Zeilen-PRD
// (inkl. Deep-Tech-Panel-Auflagen A1-A6) auf die Solartown-Lane dispatcht —
// der Dispatch schrieb aber ein 51-Zeilen-SCAFFOLD ("Goal: Feature 1,
// Feature 2") in die Rig-Arbeitskopie, weil approvedPlanItem() nichts fand
// (die mk-*-plan_items haengen an der PROGRAMM-Karte, nicht an der
// Vorhaben-Karte). Der Decomposer zerlegte die leere Vorlage, die Worker
// bauten am echten Auftrag vorbei (zwei unbrauchbare Branches).
//
// Regel ab jetzt: Wenn die Karte eine echte Plan-Datei verlinkt, wird DIESE
// in die Rig-Arbeitskopie uebernommen — nie eine erfunden. Nur wenn es gar
// keine gibt, greift das Scaffold (Neu-Vorhaben ohne PRD).

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// decomposerLayers: nur diese Layer haben eine settings/layers.d/<layer>.yaml
// im Rig — alles andere fuehrt zu "kein Zerfall definiert" (2026-08-03 live
// beobachtet mit layer: implementation).
var decomposerLayers = map[string]bool{"prd": true, "vision": true, "phase": true}

var layerLineRe = regexp.MustCompile(`(?m)^layer:\s*.*$`)

// scaffoldMarker: Fingerabdruck der alten Generator-Leervorlage. Solche
// Dateien duerfen NIE als Auftrags-Spec kopiert werden (Befund 2026-08-03:
// an sa-hr-strecke haengen BEIDE — das leere 'hr-strecke-prd.md' und die
// echte 203-Zeilen-'sa-hr-strecke-prd.md'; alphabetisch gewinnt das leere).
func istScaffold(inhalt string) bool {
	treffer := 0
	for _, m := range []string{"- Feature 1", "### R1 - Core Flow", "Phase 1 - Prototype", "parent_plan: null"} {
		if strings.Contains(inhalt, m) {
			treffer++
		}
	}
	return treffer >= 2
}

// linkedPlanFile liefert die SUBSTANZIELLSTE verlinkte, existierende Plan-Datei
// einer Karte: Leervorlagen fliegen raus, danach gewinnt die groesste Datei
// (Delivery-Reports sind ueber das -prd.md-Muster ohnehin ausgeschlossen).
// Alphabetische Reihenfolge waere gefaehrlich — sie liefert bei Prefix-
// Duplikaten die falsche (leere) Datei.
func linkedPlanFile(ctx context.Context, p *pgxpool.Pool, initiativeID string) string {
	rows, err := p.Query(ctx, `
		SELECT ref FROM portfolio.initiative_link
		 WHERE initiative_id=$1 AND kind='plan_file' AND ref LIKE '%-prd.md'`, initiativeID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	best, bestLen := "", 0
	for rows.Next() {
		var ref string
		if rows.Scan(&ref) != nil || !strings.HasPrefix(ref, "/") {
			continue
		}
		raw, rerr := os.ReadFile(ref)
		if rerr != nil {
			continue
		}
		if istScaffold(string(raw)) {
			fmt.Fprintf(os.Stderr, "  · dispatch %s: Leervorlage %s uebersprungen (Scaffold-Fingerabdruck)\n", initiativeID, ref)
			continue
		}
		if len(raw) > bestLen {
			best, bestLen = ref, len(raw)
		}
	}
	return best
}

// copyPlanToRig kopiert die echte Plan-Datei 1:1 in die Rig-Arbeitskopie und
// setzt NUR den layer auf einen, den der Decomposer zerlegen kann (samt
// Herkunfts-Notiz). Rueckgabe: Zielpfad im Rig.
func copyPlanToRig(srcPath, rigRepo, slug string) (string, error) {
	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return "", fmt.Errorf("Plan-Datei %s lesen: %w", srcPath, err)
	}
	body := string(raw)

	// layer normalisieren, damit settings/layers.d/<layer>.yaml greift.
	if m := layerLineRe.FindString(body); m != "" {
		cur := strings.TrimSpace(strings.TrimPrefix(m, "layer:"))
		if !decomposerLayers[cur] {
			body = layerLineRe.ReplaceAllString(body, "layer: prd")
		}
	}
	// Herkunft dokumentieren (die Arbeitskopie ist NICHT die Wahrheit).
	if i := strings.Index(body, "\n---\n"); i > 0 {
		head := body[:i+len("\n---\n")]
		rest := body[i+len("\n---\n"):]
		body = head + fmt.Sprintf(
			"\n> **Fabrik-Arbeitskopie.** Quelle der Wahrheit: `%s`.\n"+
				"> Aenderungen am Auftrag gehoeren dorthin, nicht hierher.\n", srcPath) + rest
	}

	dir := filepath.Join(rigRepo, "docs", "plans")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	dst := filepath.Join(dir, slug+"-prd.md")
	if old, err := os.ReadFile(dst); err == nil && string(old) == body {
		return dst, nil // identisch, nichts zu tun
	}
	if err := os.WriteFile(dst, []byte(body), 0o644); err != nil {
		return "", fmt.Errorf("schreiben %s: %w", dst, err)
	}
	// Best-effort-Commit (dirty-Repo bricht den Dispatch nicht).
	_ = exec.Command("git", "-C", rigRepo, "add", dst).Run()
	_ = exec.Command("git", "-C", rigRepo,
		"-c", "user.name=master-kanban", "-c", "user.email=mk@werkstatt",
		"commit", "--no-verify", "-q", "-m",
		fmt.Sprintf("Fabrik-Arbeitskopie %s (Dispatch-Kopie der echten Spec aus %s)", slug, srcPath)).Run()
	return dst, nil
}

// emitPlanForDecomposer weckt den Decomposer fuer die Rig-Arbeitskopie.
// WICHTIG: der Decomposer liest layer aus dem EVENT-PAYLOAD (nicht aus der
// Datei) — fehlt das Feld, meldet er "kein layers.d/.yaml" und tut nichts
// (2026-08-03 live verifiziert).
func emitPlanForDecomposer(ctx context.Context, slug, rigPath, rigRepo string) error {
	sp, err := solartownPool()
	if err != nil {
		return fmt.Errorf("solartown-Pool: %w", err)
	}
	payload := fmt.Sprintf(
		`{"slug":%q,"path":%q,"repo":%q,"layer":"prd","old":"draft","new":"approved-with-notes"}`,
		slug, rigPath, rigRepo)
	_, err = sp.Exec(ctx, `SELECT town.emit('plan.status-changed', $1::jsonb, 'master-kanban')`, payload)
	return err
}
