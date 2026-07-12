package main

import "testing"

// TestStageProposalDecision deckt die Regel-Guards des Vollzugs ab
// (Regel 1/2/5): halt blockt, locked blockt, rückwärts blockt, vorwärts bewegt.
func TestStageProposalDecision(t *testing.T) {
	cases := []struct {
		name       string
		halt       bool
		locked     bool
		current    string
		target     string
		wantMove   bool
		wantReason string
	}{
		{"halt blockt (auch vorwärts)", true, false, "now", "watching", false, "halted"},
		{"halt sticht locked+vorwärts", true, false, "idea", "done", false, "halted"},
		{"locked blockt vorwärts", false, true, "now", "watching", false, "locked"},
		{"rückwärts blockt", false, false, "watching", "now", false, "not-forward"},
		{"gleiche stage blockt", false, false, "now", "now", false, "not-forward"},
		{"vorwärts bewegt (now→watching)", false, false, "now", "watching", true, "moved"},
		{"vorwärts bewegt (idea→soon)", false, false, "idea", "soon", true, "moved"},
		{"vorwärts bewegt (watching→done)", false, false, "watching", "done", true, "moved"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			move, reason := stageProposalDecision(tc.halt, tc.locked, tc.current, tc.target)
			if move != tc.wantMove || reason != tc.wantReason {
				t.Errorf("stageProposalDecision(halt=%v,locked=%v,%q→%q) = (%v,%q), want (%v,%q)",
					tc.halt, tc.locked, tc.current, tc.target, move, reason, tc.wantMove, tc.wantReason)
			}
		})
	}
}

func TestStageRank(t *testing.T) {
	// Ordnung muss streng steigen; Unbekannt = 0 (identisch zur alten Inline-Map).
	if !(stageRank("idea") < stageRank("soon") &&
		stageRank("soon") < stageRank("now") &&
		stageRank("now") < stageRank("watching") &&
		stageRank("watching") < stageRank("done")) {
		t.Errorf("Stage-Ordnung nicht streng steigend: %v", stageOrder)
	}
	if stageRank("garbage") != 0 {
		t.Errorf("unbekannte Stage sollte 0 sein (alte Map-Semantik), got %d", stageRank("garbage"))
	}
}

// TestListenerShouldMove beweist die Pflicht-Regel des WP1-Nachtrags: der
// /api/events-Listener vollzieht propose_only-Vorschläge NIE (Regel 3,
// quantbot) — sonst bewegte er den bewusst nicht-vollzogenen Vorschlag doch.
// Ansonsten gilt dieselbe Vorwärts/Lock-Regel wie im Sweep.
func TestListenerShouldMove(t *testing.T) {
	cases := []struct {
		name        string
		proposeOnly bool
		locked      bool
		current     string
		target      string
		want        bool
	}{
		{"propose_only blockt (auch vorwärts, unlocked)", true, false, "now", "watching", false},
		{"propose_only blockt (idea→done)", true, false, "idea", "done", false},
		{"propose_only sticht: bewegt NICHT trotz erlaubter Bewegung", true, false, "watching", "done", false},
		{"normal vorwärts bewegt", false, false, "now", "watching", true},
		{"locked blockt", false, true, "now", "watching", false},
		{"rückwärts blockt", false, false, "watching", "now", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := listenerShouldMove(tc.proposeOnly, tc.locked, tc.current, tc.target); got != tc.want {
				t.Errorf("listenerShouldMove(propose_only=%v,locked=%v,%q→%q)=%v, want %v",
					tc.proposeOnly, tc.locked, tc.current, tc.target, got, tc.want)
			}
		})
	}
}

// TestWatchingDoneDecision deckt die drei Ledger-Fälle + die Negativpfade ab.
func TestWatchingDoneDecision(t *testing.T) {
	cases := []struct {
		name       string
		ev         doneEvidence
		wantMove   bool
		wantReason string
	}{
		{
			"mit Live-Deploy → done",
			doneEvidence{HasDelivery: true, DeployStatus: "live", SoftwareInLedger: true},
			true, "delivery+deploy-live",
		},
		{
			"ohne Ledger-Vorkommen → Delivery reicht (Docs/Konzept)",
			doneEvidence{HasDelivery: true, DeployStatus: "", SoftwareInLedger: false},
			true, "delivery-ohne-ledger-software",
		},
		{
			"Urteilsfall: Software im Ledger, aber kein Deploy-Beleg",
			doneEvidence{HasDelivery: true, DeployStatus: "", SoftwareInLedger: true},
			false, "watching-ohne-deploy",
		},
		{
			"eigener Deploy, aber nicht live → kein Move",
			doneEvidence{HasDelivery: true, DeployStatus: "errored", SoftwareInLedger: true},
			false, "deploy-nicht-live",
		},
		{
			"ohne Delivery → nie",
			doneEvidence{HasDelivery: false, DeployStatus: "live", SoftwareInLedger: true},
			false, "keine-delivery-evidenz",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			move, reason := watchingDoneDecision(tc.ev)
			if move != tc.wantMove || reason != tc.wantReason {
				t.Errorf("watchingDoneDecision(%+v) = (%v,%q), want (%v,%q)",
					tc.ev, move, reason, tc.wantMove, tc.wantReason)
			}
		})
	}
}

// TestFlowActionDeltaGate beweist: ein zweiter Sweep mit unverändertem Flag-Satz
// schreibt kein Event; geänderte/leere Vorgänger schreiben.
func TestFlowActionDeltaGate(t *testing.T) {
	reasons := []string{
		"Promote-reif: alle verlinkten Beads sind closed",
		"WIP-Überlauf: 5 karten in NOW (limit 4)",
	}

	// Reihenfolge-Unabhängigkeit: gleicher Satz, andere Reihenfolge → gleicher Hash.
	if flagsHash(reasons) != flagsHash([]string{reasons[1], reasons[0]}) {
		t.Errorf("flagsHash darf nicht von der Reihenfolge abhängen")
	}

	// Zweiter Sweep, unveränderter Satz → kein Schreiben.
	if flowActionChanged(flagsHash(reasons), reasons) {
		t.Errorf("unveränderter Flag-Satz darf das Delta-Gate NICHT passieren")
	}

	// Erster Sweep / Legacy-Event ohne Hash → schreiben.
	if !flowActionChanged("", reasons) {
		t.Errorf("leerer Vorgänger-Hash muss das Delta-Gate passieren (erster Sweep)")
	}

	// Geänderter Flag-Satz → schreiben.
	changed := append(append([]string(nil), reasons...), "Stagnation: 15 tage stille")
	if !flowActionChanged(flagsHash(reasons), changed) {
		t.Errorf("geänderter Flag-Satz muss das Delta-Gate passieren")
	}

	// Leerer Satz ist stabil gegen sich selbst (Clear-Event schreibt nur einmal).
	if flowActionChanged(flagsHash(nil), nil) {
		t.Errorf("leerer Satz gegen leeren Hash darf nicht erneut schreiben")
	}
}
