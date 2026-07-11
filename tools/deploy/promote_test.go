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

// TestPromoteInvocation — die go|node-Weiche des Promote-Gates (sa-deploy-stufen
// W4): go→deploy-gt.sh --bin, node→deploy-node.sh --dest (+ --build-cmd).
func TestPromoteInvocation(t *testing.T) {
	const gt, nd, repo, ssh = "/x/deploy-gt.sh", "/x/deploy-node.sh", "/opt/mirror.git", "root@prod"

	goRec := promoteRecipe{Src: "apps/staging-canary", Bin: "/opt/sa-canary/sa-canary", Unit: "sa-canary.service", Box: "stayawesome-prod"}
	s, a := promoteInvocation(goRec, gt, nd, repo, ssh, "staging-canary", "abc123")
	if s != gt {
		t.Fatalf("go script=%q, want %q", s, gt)
	}
	if !pcHasPair(a, "--bin", "/opt/sa-canary/sa-canary") || pcHasFlag(a, "--dest") {
		t.Fatalf("go args falsch: %v", a)
	}

	ndRec := promoteRecipe{Type: "node", Src: "apps/fin", Bin: "/opt/sa-fin", Unit: "sa-fin.service", Box: "stayawesome-prod",
		BuildCmd: "pnpm install --frozen-lockfile && pnpm run build"}
	s, a = promoteInvocation(ndRec, gt, nd, repo, ssh, "sa-fin", "deadbeef")
	if s != nd {
		t.Fatalf("node script=%q, want %q", s, nd)
	}
	if pcHasFlag(a, "--bin") || !pcHasPair(a, "--dest", "/opt/sa-fin") {
		t.Fatalf("node args falsch: %v", a)
	}
	if !pcHasPair(a, "--build-cmd", "pnpm install --frozen-lockfile && pnpm run build") {
		t.Fatalf("node --build-cmd fehlt: %v", a)
	}
	if !pcHasPair(a, "--unit", "sa-fin.service") || !pcHasPair(a, "--box", ssh) || !pcHasPair(a, "--ref", "deadbeef") {
		t.Fatalf("node args unvollständig: %v", a)
	}
}

// TestJourneyGateSatisfied — die zusätzliche Promote-Stufe „Journey-Grün des SHA"
// (WP4). Nur ein grünes gate-Result der EXAKTEN app+sha zählt; red/harness,
// falscher Kontext, falsche app, falsche SHA werden verworfen (SHA-Präfix-tolerant).
func TestJourneyGateSatisfied(t *testing.T) {
	dir := t.TempDir()
	longSha := "77f6037c852ece813dd39f0d634d3d0b5de51167"
	write := func(name string, r journeyResult) {
		b, _ := json.Marshal(r)
		if err := os.WriteFile(filepath.Join(dir, name), b, 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Leeres Verzeichnis → nicht erfüllt (Verweigerungs-Beweis-Kern → exit 67).
	if ok, _ := journeyGateSatisfied(dir, "fin", longSha); ok {
		t.Fatal("leeres Results-Verzeichnis MUSS unerfüllt sein (sonst kein Gate)")
	}

	// Ablenker: rot, falscher Kontext, falsche app, falsche SHA.
	write("fin-gate-1.json", journeyResult{App: "fin", Context: "gate", Ref: longSha, Verdict: "red"})
	write("fin-smoke-1.json", journeyResult{App: "fin", Context: "smoke", Ref: longSha, Verdict: "green"})
	write("other-gate-1.json", journeyResult{App: "master-kanban", Context: "gate", Ref: longSha, Verdict: "green"})
	write("fin-gate-wrongsha.json", journeyResult{App: "fin", Context: "gate", Ref: "deadbeefdeadbeef", Verdict: "green"})
	if ok, _ := journeyGateSatisfied(dir, "fin", longSha); ok {
		t.Fatal("nur Ablenker (rot/smoke/andere-app/andere-sha) dürfen NICHT durchwinken")
	}

	// Grünes gate-Result mit KURZER SHA im Ref, Anfrage mit langer SHA → Präfix-Match.
	write("fin-gate-2.json", journeyResult{App: "fin", Context: "gate", Ref: "77f6037", Verdict: "green"})
	ok, proof := journeyGateSatisfied(dir, "fin", longSha)
	if !ok {
		t.Fatal("grünes gate-Result (kurze SHA) MUSS die lange SHA durchwinken (Präfix)")
	}
	if proof == "" {
		t.Fatal("Beweis-Dateipfad fehlt")
	}

	// Andere app fragt dieselbe SHA an → nicht erfüllt (app ist Teil des Gates).
	if ok, _ := journeyGateSatisfied(dir, "sa-canary", longSha); ok {
		t.Fatal("gate-Result einer anderen app darf keine Fremd-app durchwinken")
	}
}

func pcHasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}
func pcHasPair(args []string, flag, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}
	return false
}
