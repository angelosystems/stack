package main

import "testing"

// SC1 (Capture-Completeness PRD): Ein frisch erzeugter label-loser Test-Bead
// muss binnen eines Adapter-Zyklus in der Unlinked-Lane erscheinen — nicht
// still gedroppt.
func TestSC1_LabellessBead_SurfacesAsUnlinked(t *testing.T) {
	freshLabelless := beadRow{ID: "st-test1", SpecID: "", Labels: nil}

	got := classifyBeads("st", []beadRow{freshLabelless}, map[string]string{
		"some-other-plan": "init-existing",
	})

	if len(got) != 1 {
		t.Fatalf("SC1 violated: expected 1 Unlinked entry, got %d (%+v)", len(got), got)
	}
	u := got[0]
	if u.Kind != "bead" {
		t.Fatalf("SC1 violated: kind = %q, want \"bead\"", u.Kind)
	}
	if u.Ref != "st-test1" {
		t.Fatalf("SC1 violated: ref = %q, want \"st-test1\"", u.Ref)
	}
	if u.Reason != "no_join_key" {
		t.Fatalf("SC1 violated: reason = %q, want \"no_join_key\" "+
			"(label-loser Bead muss als no_join_key sichtbar werden, nicht stumm verschwinden)", u.Reason)
	}
}

// SC5 (Capture-Completeness PRD): Kein Work-Item-Typ darf still unsichtbar
// sein — sowohl Orphan-Beads ALS AUCH unverlinkte vk-Workspaces müssen in
// der Unlinked-Lane auftauchen.
func TestSC5_BothBeadAndWorkspace_Surface(t *testing.T) {
	orphanBead := beadRow{
		ID:     "st-orphan",
		SpecID: "",
		Labels: []string{"plan:never-existed"},
	}
	slugToInit := map[string]string{"linked-plan": "init-1"}
	beadUnlinked := classifyBeads("st", []beadRow{orphanBead}, slugToInit)

	if len(beadUnlinked) != 1 || beadUnlinked[0].Kind != "bead" {
		t.Fatalf("SC5 violated (bead-Typ): orphan bead wurde nicht erfasst — got %+v", beadUnlinked)
	}
	if beadUnlinked[0].Reason != "no_match" {
		t.Fatalf("SC5 violated (bead-Typ): erwartet reason=no_match, got %q", beadUnlinked[0].Reason)
	}

	orphanWS := vkWorkspace{ID: "11111111-1111-1111-1111-111111111111", Title: "Loose Task"}
	linkedRefs := map[string]bool{
		"ffffffff-ffff-ffff-ffff-ffffffffffff": true,
	}
	wsUnlinked := classifyWorkspaces([]vkWorkspace{orphanWS}, linkedRefs)

	if len(wsUnlinked) != 1 || wsUnlinked[0].Kind != "vk_workspace" {
		t.Fatalf("SC5 violated (vk_workspace-Typ): unverlinkter Workspace nicht erfasst — got %+v", wsUnlinked)
	}
	if wsUnlinked[0].Reason != "no_link" {
		t.Fatalf("SC5 violated (vk_workspace-Typ): erwartet reason=no_link, got %q", wsUnlinked[0].Reason)
	}

	kindsSeen := map[string]bool{}
	for _, u := range append(beadUnlinked, wsUnlinked...) {
		kindsSeen[u.Kind] = true
	}
	if !kindsSeen["bead"] {
		t.Fatalf("SC5 violated: Typ \"bead\" ist still unsichtbar")
	}
	if !kindsSeen["vk_workspace"] {
		t.Fatalf("SC5 violated: Typ \"vk_workspace\" ist still unsichtbar")
	}
}
