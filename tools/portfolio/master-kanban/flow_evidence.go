package main

// flow_evidence.go — gebatchte Evidenz-Beschaffung fuer den Flow-Manager-Sweep
// (mk-flow-manager-haertung WP1). Ersetzt die per-Karte-Einzelqueries und den
// per-Karte-sqlite3-Spawn: pro Sweep EINE Link-Query, EINE Bead-Status-Query
// (solartown), EIN sqlite3-Lauf ueber alle unarchivierten VK-Workspaces.
//
// Workspace-Matching: Workspaces heissen nach Bead-IDs (z.B. sol-so-nhx0fr),
// NICHT nach Karten-IDs — gematcht wird daher ueber die Bead-Refs der Karte
// (plus Karten-ID als Fallback fuer vk-delegate-Workspaces mit Slug-Namen).
// Der alte LIKE '%karten-id%'-Lookup traf praktisch nie (blindes Auge).

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// wsRow ist eine Zeile des globalen Workspace-Snapshots (eine je
// execution_process, wie der alte per-Karte-Query — Mehrfachzeilen pro
// Workspace sind gewollt und erhalten das bisherige Status-Verhalten).
type wsRow struct {
	Name   string
	ID     string
	Status string
}

// loadBeadEvidence laedt alle bead-Links (Board) und deren Stati (solartown)
// in zwei Set-Queries. Fehler machen die Evidenz konservativ leer — dieselbe
// Semantik wie vorher (kein Status = "unknown" beim Zusammensetzen).
func loadBeadEvidence(ctx context.Context, p *pgxpool.Pool) (map[string][]string, map[string]string) {
	refsByCard := make(map[string][]string)
	statusByRef := make(map[string]string)

	rows, err := p.Query(ctx, `
		SELECT initiative_id, ref FROM portfolio.initiative_link WHERE kind = 'bead'
	`)
	if err != nil {
		return refsByCard, statusByRef
	}
	var allRefs []string
	for rows.Next() {
		var id, ref string
		if rows.Scan(&id, &ref) == nil {
			refsByCard[id] = append(refsByCard[id], ref)
			allRefs = append(allRefs, ref)
		}
	}
	rows.Close()
	if len(allRefs) == 0 {
		return refsByCard, statusByRef
	}

	sp, err := solartownPool()
	if err != nil {
		return refsByCard, statusByRef
	}
	srows, err := sp.Query(ctx, `
		SELECT id, status FROM beads.issues WHERE id = ANY($1) AND deleted_at IS NULL
	`, allRefs)
	if err != nil {
		return refsByCard, statusByRef
	}
	for srows.Next() {
		var id, status string
		if srows.Scan(&id, &status) == nil {
			statusByRef[id] = status
		}
	}
	srows.Close()
	return refsByCard, statusByRef
}

// beadsFor setzt die LinkedBead-Liste einer Karte aus den Batch-Maps zusammen
// (fehlender Status = "unknown", wie der alte Einzel-Query-Fallback).
func beadsFor(cardID string, refsByCard map[string][]string, statusByRef map[string]string) []LinkedBead {
	var beads []LinkedBead
	for _, ref := range refsByCard[cardID] {
		status, ok := statusByRef[ref]
		if !ok {
			status = "unknown"
		}
		beads = append(beads, LinkedBead{Ref: ref, Status: status})
	}
	return beads
}

// loadAllWorkspaces laedt alle unarchivierten VK-Workspaces (Name, ID, Status)
// mit EINEM sqlite3-Aufruf. Kein VK-DB-File oder Fehler ⇒ leer (wie vorher:
// dann gibt es schlicht keine Workspace-Evidenz).
func loadAllWorkspaces() []wsRow {
	vkDB := envOr("VIBE_KANBAN_DB", "/root/.local/share/vibe-kanban/db.v2.sqlite")
	if _, err := os.Stat(vkDB); err != nil {
		return nil
	}
	const q = `
		SELECT w.name, hex(w.id), COALESCE(ep.status, '')
		FROM workspaces w
		JOIN sessions s ON s.workspace_id = w.id
		LEFT JOIN execution_processes ep ON ep.session_id = s.id
		WHERE w.archived = 0
		ORDER BY ep.created_at DESC;
	`
	cmd := exec.Command("sqlite3", "-readonly", vkDB, q)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil
	}
	var all []wsRow
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) == 3 && parts[0] != "" {
			all = append(all, wsRow{Name: parts[0], ID: parts[1], Status: parts[2]})
		}
	}
	return all
}

// workspacesFor liefert die Workspaces einer Karte: Name enthaelt eine der
// Bead-Refs der Karte ODER die Karten-ID (Fallback fuer slug-benannte
// vk-delegate-Workspaces).
func workspacesFor(cardID string, beadRefs []string, all []wsRow) []LinkedWorkspace {
	var ws []LinkedWorkspace
	for _, row := range all {
		match := strings.Contains(row.Name, cardID)
		if !match {
			for _, ref := range beadRefs {
				if ref != "" && strings.Contains(row.Name, ref) {
					match = true
					break
				}
			}
		}
		if match {
			ws = append(ws, LinkedWorkspace{ID: row.ID, Status: row.Status})
		}
	}
	return ws
}
