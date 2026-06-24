package main

import (
	"os"
	"strings"
	"testing"
)

func TestGenerateDigestReport(t *testing.T) {
	cards := []DiagnosedCard{
		{
			Initiative: FlowInitiative{
				ID:    "init-1",
				Title: "Test Initiative 1",
				Stage: "now",
				Firma: "stayawesome",
			},
			FlaggedReasons: []string{
				"Stagnation: 5 tage stille, keine aktive arbeit (workspace/beads)",
			},
			Diagnosis: FlowDiagnosis{
				Category:       "wartet-auf-Mensch",
				Confidence:     "High",
				Reasoning:      "No active beads or workspaces found for 5 days.",
				ProposedAction: "ask owner for input",
			},
		},
		{
			Initiative: FlowInitiative{
				ID:    "init-2",
				Title: "Test Initiative 2",
				Stage: "idea",
				Firma: "solartown",
			},
			FlaggedReasons: []string{
				"Backlog-Fäule: über 14 tage unbewegt in IDEA",
			},
			Diagnosis: FlowDiagnosis{
				Category:       "verlassen",
				Confidence:     "Low",
				Reasoning:      "Old idea with no activity or linked elements.",
				ProposedAction: "",
			},
		},
	}

	report := generateDigestReport(cards)

	if !strings.Contains(report, "🩺 KANBAN FLOW-MANAGER BOARD REVIEW DIGEST") {
		t.Errorf("Expected title in report, got: %s", report)
	}

	if !strings.Contains(report, "Total Flagged Cards:** 2") {
		t.Errorf("Expected total flagged cards to be 2, got: %s", report)
	}

	if !strings.Contains(report, "Stagnant cards (Stagnation):** 1") {
		t.Errorf("Expected stagnant count to be 1, got: %s", report)
	}

	if !strings.Contains(report, "Backlog Rot cards (Backlog-Fäule):** 1") {
		t.Errorf("Expected backlog rot count to be 1, got: %s", report)
	}

	if !strings.Contains(report, "Test Initiative 1") {
		t.Errorf("Expected Initiative 1 details in report, got: %s", report)
	}

	if !strings.Contains(report, "Test Initiative 2") {
		t.Errorf("Expected Initiative 2 details in report, got: %s", report)
	}
}

func TestDeliverDigest_DryRun(t *testing.T) {
	// Set a custom recipient for testing
	os.Setenv("PORTFOLIO_DIGEST_RECIPIENT", "test-recipient/")
	defer os.Unsetenv("PORTFOLIO_DIGEST_RECIPIENT")

	err := deliverDigest("Test digest content", true) // dry-run
	if err != nil {
		t.Fatalf("Expected no error on dry-run, got: %v", err)
	}
}
