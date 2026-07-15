package main

import (
	"reflect"
	"testing"
)

// TestWorkspacesFor beweist den WP1-Kern: Workspaces heissen nach Bead-IDs
// (sol-so-nhx0fr) — gematcht wird ueber die Bead-Refs der Karte; die
// Karten-ID bleibt Fallback (slug-benannte vk-delegate-Workspaces). Der alte
// LIKE '%karten-id%'-Lookup haette alle drei Bead-Faelle verfehlt.
func TestWorkspacesFor(t *testing.T) {
	all := []wsRow{
		{Name: "sol-so-nhx0fr", ID: "AA", Status: "running"},
		{Name: "sol-so-nhx0fr", ID: "AA", Status: "completed"}, // zweite ep-Zeile desselben WS
		{Name: "sol-tr-x1y2z3", ID: "BB", Status: "failed"},
		{Name: "sk-kanban-flow-manager-wp1", ID: "CC", Status: "waiting"},
		{Name: "unrelated", ID: "DD", Status: "running"},
	}

	// Match ueber Bead-Ref (der Regelfall).
	got := workspacesFor("cf-irgendeine-karte", []string{"so-nhx0fr"}, all)
	want := []LinkedWorkspace{{ID: "AA", Status: "running"}, {ID: "AA", Status: "completed"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bead-ref-Match: got %+v, want %+v", got, want)
	}

	// Fallback ueber Karten-ID (vk-delegate-Workspaces mit Slug-Namen).
	got = workspacesFor("sk-kanban-flow-manager", nil, all)
	want = []LinkedWorkspace{{ID: "CC", Status: "waiting"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("karten-id-Fallback: got %+v, want %+v", got, want)
	}

	// Kein Match ⇒ leer (und leere Bead-Refs matchen NICHT alles).
	if got := workspacesFor("qb-anders", []string{""}, all); len(got) != 0 {
		t.Errorf("kein-Match: got %+v, want leer", got)
	}
}

// TestBeadsFor prueft die Batch-Zusammensetzung inkl. "unknown"-Fallback
// (Semantik des alten Einzel-Query-Pfads: geloeschter/fehlender Bead).
func TestBeadsFor(t *testing.T) {
	refs := map[string][]string{"karte-1": {"so-a", "so-b"}}
	status := map[string]string{"so-a": "closed"}

	got := beadsFor("karte-1", refs, status)
	want := []LinkedBead{{Ref: "so-a", Status: "closed"}, {Ref: "so-b", Status: "unknown"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("beadsFor: got %+v, want %+v", got, want)
	}
	if got := beadsFor("karte-ohne-links", refs, status); got != nil {
		t.Errorf("karte ohne Links: got %+v, want nil", got)
	}
}
