package main

import (
	"strings"
	"testing"
)

func TestBuildDeployVerdiktPayload(t *testing.T) {
	p := buildDeployVerdiktPayload("paperclip", "abc1234def567", "prod", "live", "green", "")
	if p["verdict"] != "live" || p["smoke"] != "green" {
		t.Fatalf("verdict/smoke falsch: %v/%v", p["verdict"], p["smoke"])
	}
	if p["incident_key"] != "deploy-paperclip-abc1234" {
		t.Fatalf("incident_key falsch: %v", p["incident_key"])
	}
	bin, ok := p["binary"].(string)
	if !ok || bin == "" {
		t.Fatal("binary-Identitaet fehlt (Kopien-Aufloesung, Panel BL-2)")
	}
	if _, da := p["why"]; da {
		t.Fatal("leeres why darf nicht erscheinen")
	}
}

func TestBuildDeployVerdiktPayloadRollback(t *testing.T) {
	p := buildDeployVerdiktPayload("svc", "short", "prod", "rolled_back", "red", "Smoke rot")
	if p["incident_key"] != "deploy-svc-short" {
		t.Fatalf("kurzer SHA muss ungekuerzt bleiben: %v", p["incident_key"])
	}
	if !strings.Contains(p["why"].(string), "Smoke") {
		t.Fatalf("why fehlt: %v", p["why"])
	}
}
