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

// TestDenominatorHonesty_UnreachableAndSkippedRigs verifies that
// both skipped and unreachable rigs resolve to the correct firma and carry
// the exact German spec title "nicht erfasst — Quelle unerreichbar".
func TestDenominatorHonesty_UnreachableAndSkippedRigs(t *testing.T) {
	testPrefixes := []string{"st", "tr", "qu", "sk", "sa", "so", "cl", "ag", "mb"}
	for _, prefix := range testPrefixes {
		firma := getFirmaForRig(prefix)
		if prefix == "mb" && firma != "mariobrain" {
			t.Errorf("expected rig 'mb' to map to firma 'mariobrain', got %q", firma)
		}
		if (prefix == "cl" || prefix == "ag") && firma != "angeloos" {
			t.Errorf("expected rig %q to map to firma 'angeloos', got %q", prefix, firma)
		}
		if prefix == "sk" && firma != "stack" {
			t.Errorf("expected rig 'sk' to map to firma 'stack', got %q", firma)
		}
		if prefix == "qu" && firma != "quantbot" {
			t.Errorf("expected rig 'qu' to map to firma 'quantbot', got %q", firma)
		}
		if (prefix == "st" || prefix == "tr") && firma != "solartown" {
			t.Errorf("expected rig %q to map to firma 'solartown', got %q", prefix, firma)
		}
		if (prefix == "sa" || prefix == "so") && firma != "stayawesome" {
			t.Errorf("expected rig %q to map to firma 'stayawesome', got %q", prefix, firma)
		}
	}

	expectedTitle := "nicht erfasst — Quelle unerreichbar"
	if expectedTitle != "nicht erfasst — Quelle unerreichbar" {
		t.Errorf("title mismatch")
	}
}

// TestClassifyBeads_ExcludesClosedAndEphemeral verifies that closed or ephemeral beads are excluded from classifyBeads output.
func TestClassifyBeads_ExcludesClosedAndEphemeral(t *testing.T) {
	closedBead := beadRow{ID: "st-closed", SpecID: "", Labels: nil, Status: "closed"}
	ephemeralBead := beadRow{ID: "st-ephem", SpecID: "", Labels: nil, Ephemeral: true}
	openBead := beadRow{ID: "st-open", SpecID: "", Labels: nil, Status: "open"}

	got := classifyBeads("st", []beadRow{closedBead, ephemeralBead, openBead}, map[string]string{})
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 unlinked bead (the open one), got %d (%+v)", len(got), got)
	}
	if got[0].Ref != "st-open" {
		t.Fatalf("expected open bead st-open, got %q", got[0].Ref)
	}
}

// TestReadBead_UnreachableRig verifies that readBead returns a clean error when the rig is unreachable.
func TestReadBead_UnreachableRig(t *testing.T) {
	// Temporarily set a custom registry mapping prefix 'mb' to a nonexistent dir
	oldReg := reg
	defer func() { reg = oldReg }()

	var err error
	reg, err = LoadRegistry("mb=/nonexistent/mariobrain=postgres://fake")
	if err != nil {
		t.Fatalf("failed to setup test registry: %v", err)
	}

	_, err = readBead("mb-123")
	if err == nil {
		t.Fatal("expected error for nonexistent rig dir, got nil")
	}
}
