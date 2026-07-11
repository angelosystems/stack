package main

import (
	"strings"
	"testing"
)

func TestBuildDiagnosisPrompt(t *testing.T) {
	// Test yozd prompt
	promptYozd := buildDiagnosisPrompt(1, true, false)
	if !strings.Contains(promptYozd, "Heal Attempt #1") {
		t.Errorf("expected prompt to contain Heal Attempt #1, got: %s", promptYozd)
	}
	if !strings.Contains(promptYozd, "Backlog-Tab hat heute nur einen Triage-Knopf") {
		t.Errorf("expected prompt to contain yozd-specific diagnosis, got: %s", promptYozd)
	}

	// Test 1bpf prompt
	prompt1bpf := buildDiagnosisPrompt(2, false, true)
	if !strings.Contains(prompt1bpf, "Heal Attempt #2") {
		t.Errorf("expected prompt to contain Heal Attempt #2, got: %s", prompt1bpf)
	}
	if !strings.Contains(prompt1bpf, "cockpit hat firma-Stripes aber nicht die R5 Lane-Badges") {
		t.Errorf("expected prompt to contain 1bpf-specific diagnosis, got: %s", prompt1bpf)
	}

	// Test generic fallback prompt
	promptGeneric := buildDiagnosisPrompt(3, false, false)
	if !strings.Contains(promptGeneric, "Heal Attempt #3") {
		t.Errorf("expected prompt to contain Heal Attempt #3, got: %s", promptGeneric)
	}
	if !strings.Contains(promptGeneric, "The previous run failed with zero commits") {
		t.Errorf("expected prompt to contain generic diagnosis, got: %s", promptGeneric)
	}
}
