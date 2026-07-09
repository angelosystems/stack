package main

// promote_test.go — hermetische Unit-Tests fürs Promotion-Gate (sa-deploy-
// stufen W3). Kein Netz, keine DB, kein ssh: nur die reinen Entscheidungs-
// funktionen (Trading-Wall, Approval-TTL, SHA-Match). Die echte End-to-end-
// Promotion + der Verweigerungs-Beweis laufen als Delivery-E2E gegen die Box.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTradingWall_BlocksLiveMoneyFields(t *testing.T) {
	cases := []struct {
		name   string
		fields map[string]string
		block  bool
	}{
		{"clean canary", map[string]string{"app": "staging-canary", "unit": "sa-canary.service", "src": "apps/staging-canary", "bin": "/opt/sa-canary/sa-canary", "box": "root@178.105.36.33", "repo": "/opt/solartown/stayawesomeOS/rig-mirror.git"}, false},
		{"quantbot app", map[string]string{"app": "quantbot-exec"}, true},
		{"supervisor unit", map[string]string{"unit": "supervisor.service"}, true},
		{"dublin box", map[string]string{"box": "root@dublin"}, true},
		{"strategies src", map[string]string{"src": "strategies/foo"}, true},
		{"live-trad bin", map[string]string{"bin": "/opt/live-trad/x"}, true},
		{"case-insensitive", map[string]string{"app": "QuantBot"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, p := tradingWallViolation(c.fields)
			got := f != ""
			if got != c.block {
				t.Fatalf("block=%v, wollte %v (feld=%q muster=%q)", got, c.block, f, p)
			}
		})
	}
}

func TestShaPrefixMatch(t *testing.T) {
	long := "ca045bc7d9851a413ea95e4739d0d3030c041a6c"
	shortSha := "ca045bc"
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{long, shortSha, true},
		{shortSha, long, true},
		{long, long, true},
		{"deadbeef", long, false},
		{"", long, false},
		{"unknown", long, false},
	} {
		if got := shaPrefixMatch(c.a, c.b); got != c.want {
			t.Errorf("shaPrefixMatch(%q,%q)=%v, wollte %v", c.a, c.b, got, c.want)
		}
	}
}

func writeApproval(t *testing.T, dir string, a approval) {
	t.Helper()
	b, _ := json.Marshal(a)
	if err := os.WriteFile(approvalPath(dir, a.App, a.Sha), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCheckApproval(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	app, sha := "staging-canary", "ca045bc7d9851a413ea95e4739d0d3030c041a6c"

	// (1) fehlend → Fehler (Verweigerungs-Beweis-Kern).
	if _, err := checkApproval(dir, app, sha, now); err == nil {
		t.Fatal("fehlendes Approval MUSS ein Fehler sein (sonst kein Gate)")
	}

	// (2) frisch + passend → ok.
	writeApproval(t, dir, approval{App: app, Sha: sha, ApprovedBy: "mario", ApprovedAt: now.Add(-1 * time.Hour), TTLSeconds: 86400})
	if a, err := checkApproval(dir, app, sha, now); err != nil {
		t.Fatalf("frisches Approval sollte gelten: %v", err)
	} else if a.ApprovedBy != "mario" {
		t.Fatalf("approver = %q", a.ApprovedBy)
	}

	// (3) abgelaufen → Fehler.
	writeApproval(t, dir, approval{App: app, Sha: sha, ApprovedBy: "mario", ApprovedAt: now.Add(-48 * time.Hour), TTLSeconds: 3600})
	if _, err := checkApproval(dir, app, sha, now); err == nil {
		t.Fatal("abgelaufenes Approval MUSS ein Fehler sein")
	}

	// (4) falsche SHA im Body → Fehler (Manipulations-Schutz).
	os.WriteFile(approvalPath(dir, app, sha), []byte(`{"app":"staging-canary","sha":"deadbeef","approved_by":"x","approved_at":"2026-07-09T11:00:00Z","ttl_seconds":86400}`), 0600)
	if _, err := checkApproval(dir, app, sha, now); err == nil {
		t.Fatal("SHA-Mismatch zwischen Dateiname und Body MUSS ein Fehler sein")
	}

	// (5) andere SHA hat KEIN Approval → Fehler (eine Freigabe gilt nur ihrer SHA).
	writeApproval(t, dir, approval{App: app, Sha: sha, ApprovedBy: "mario", ApprovedAt: now, TTLSeconds: 86400})
	if _, err := checkApproval(dir, app, "beef1234", now); err == nil {
		t.Fatal("Approval einer SHA darf keine andere SHA durchwinken")
	}
}

func TestLoadPromoteManifest(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.yaml")
	os.WriteFile(p, []byte(`repo: /opt/solartown/stayawesomeOS/rig-mirror.git
services:
  staging-canary:
    src: apps/staging-canary
    bin: /opt/sa-canary/sa-canary
    unit: sa-canary.service
    box: stayawesome-prod
    smoke: /opt/sa-deploy/sa-canary-prod-smoke.sh
`), 0644)
	m, err := loadPromoteManifest(p)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := m.Services["staging-canary"]
	if !ok {
		t.Fatal("staging-canary fehlt")
	}
	if rec.Box != "stayawesome-prod" || rec.Smoke == "" {
		t.Fatalf("Rezept falsch: %+v", rec)
	}
	// repo fehlt → Fehler.
	os.WriteFile(p, []byte("services: {}\n"), 0644)
	if _, err := loadPromoteManifest(p); err == nil {
		t.Fatal("Manifest ohne repo MUSS ein Fehler sein")
	}
}
