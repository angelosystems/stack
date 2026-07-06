package main

// deploy_reactor_outbox_test.go — Zustandsmaschinen-Tests des Deploy-Reaktors
// (Release-Pipeline WP6). Die vier Rollback-Übergänge und der No-Race gegen den
// Reconciler sind GRÜNE Tests, nicht Erst-im-Incident-Code (Geist Crispin/WP6).
// DB-frei: die Entscheide sind pur; die echten Ledger-Übergänge beweist der
// Game-Day (game-day-deploy.sh) gegen eine Scratch-DB.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDecideClaim(t *testing.T) {
	cases := []struct {
		name        string
		status      string
		breakerOpen bool
		want        claimAction
	}{
		{"pending, Breaker zu → claim", "pending", false, actClaim},
		{"Breaker offen → skip (D15)", "pending", true, actSkipBreaker},
		{"schon deploying → skip (Doppel-Zustellung, D11)", "deploying", false, actSkipNotPending},
		{"schon live → skip (idempotent)", "live", false, actSkipNotPending},
		{"rolled_back → skip (Quarantäne)", "rolled_back", false, actSkipNotPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideClaim(tc.status, tc.breakerOpen); got != tc.want {
				t.Fatalf("decideClaim(%q, breaker=%v) = %v, will %v", tc.status, tc.breakerOpen, got, tc.want)
			}
		})
	}
}

func TestDecideSmoke(t *testing.T) {
	const maxA = 5
	cases := []struct {
		name                     string
		reached, shaOK, forceRed bool
		attempt                  int
		dueHit                   bool
		want                     smokeVerdict
	}{
		{"erreicht + SHA ok → grün", true, true, false, 1, false, smokeGreen},
		{"rot, Retries offen → retry", false, false, false, 1, false, smokeRetry},
		{"SHA falsch, Retries offen → retry", true, false, false, 2, false, smokeRetry},
		{"rot, Versuche erschöpft → rollback", false, false, false, maxA, false, smokeRollback},
		{"rot, Frist gerissen → rollback (D17)", false, false, false, 1, true, smokeRollback},
		{"forceRed übertrumpft Grün, Retries offen → retry", true, true, true, 1, false, smokeRetry},
		{"forceRed, erschöpft → rollback", true, true, true, maxA, false, smokeRollback},
		{"forceRed, Frist gerissen → rollback", true, true, true, 1, true, smokeRollback},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideSmoke(tc.reached, tc.shaOK, tc.forceRed, tc.attempt, maxA, tc.dueHit)
			if got != tc.want {
				t.Fatalf("decideSmoke(reached=%v shaOK=%v forceRed=%v attempt=%d due=%v) = %v, will %v",
					tc.reached, tc.shaOK, tc.forceRed, tc.attempt, tc.dueHit, got, tc.want)
			}
		})
	}
}

func TestDecideRollback(t *testing.T) {
	if decideRollback("") != rbErroredNoPrev {
		t.Fatal("leerer prev muss errored geben (kein So-tun-als-ob)")
	}
	if decideRollback("   ") != rbErroredNoPrev {
		t.Fatal("whitespace-prev zählt als kein prev")
	}
	if decideRollback("abc1234") != rbDeployPrev {
		t.Fatal("prev vorhanden → SHA-gepinnt zurückbauen")
	}
}

func TestBreakerOpens(t *testing.T) {
	if breakerOpens(2, 3) {
		t.Fatal("2 < 3 darf den Breaker nicht öffnen")
	}
	if !breakerOpens(3, 3) {
		t.Fatal("3 rote in Folge müssen bei K=3 öffnen")
	}
	if !breakerOpens(4, 3) {
		t.Fatal("darüber bleibt offen")
	}
}

// TestReactorLease_ReconcilerSkips — der No-Race (WP6-Done-Kriterium): eine
// vom Reaktor geleaste deploying-Zeile fasst der Reconciler (decideReconcile,
// Session B) NICHT an, auch bei grüner Probe. So flippt die 60-s-Probe keine
// Zeile mitten im Smoke (D13).
func TestReactorLease_ReconcilerSkips(t *testing.T) {
	now := time.Now()
	until := now.Add(9 * time.Minute) // Reaktor-Lease (owned_until > now)
	leased := deploymentHead{
		Service: "svc", Environment: "prod-mvp", ProbeKind: "http",
		GitSha: "abc1234", Status: "deploying",
		DeployedAt: now.Add(-time.Hour), OwnedUntil: &until,
	}
	greenProbe := probeResult{Reached: true, Sha: "abc1234"}
	if v := decideReconcile(leased, greenProbe, now, 10*time.Minute); v.NewStatus != "" {
		t.Fatalf("Reconciler fasste geleaste Zeile an: %+v (darf nicht)", v)
	}
	// Gegenprobe: ohne Lease jenseits des Fensters + rot → der Reconciler DARF errored.
	unleased := leased
	unleased.OwnedUntil = nil
	unleased.Status = "live"
	red := probeResult{Err: "rest"}
	if v := decideReconcile(unleased, red, now, 10*time.Minute); v.NewStatus != "errored" {
		t.Fatalf("ungeleaste rote live-Zeile: erwartet errored, bekam %+v", v)
	}
}

// TestEscalateAndBreaker — durable Eskalations-Artefakte (D20c) + Breaker-
// Persistenz (D15): ein geöffneter Breaker überlebt als Marker-File, ein
// späterer --once-Lauf sieht ihn.
func TestEscalateAndBreaker(t *testing.T) {
	dir := t.TempDir()
	r := &reactor{
		owner:     "deploy-reactor@test",
		stateDir:  filepath.Join(dir, "state"),
		eventsDir: filepath.Join(dir, "events"),
		breakerK:  3,
		logf:      func(string, ...any) {},
	}

	if r.breakerIsOpen() {
		t.Fatal("frischer Breaker darf nicht offen sein")
	}
	r.openBreaker("2 rote Deploys in Folge")
	if !r.breakerIsOpen() {
		t.Fatal("nach openBreaker muss der Marker liegen")
	}

	// openBreaker eskaliert → ein GATE_BREAKER_OPEN-Artefakt liegt durable.
	ents, _ := os.ReadDir(r.eventsDir)
	found := false
	for _, e := range ents {
		if len(e.Name()) > 16 && e.Name()[:16] == "GATE_BREAKER_OPE" {
			found = true
		}
	}
	if !found {
		t.Fatalf("kein GATE_BREAKER_OPEN-Artefakt in %s: %v", r.eventsDir, ents)
	}

	// Manueller Reset = Marker entfernen (die im Artefakt genannte Handlung).
	if err := os.Remove(r.breakerFile()); err != nil {
		t.Fatal(err)
	}
	if r.breakerIsOpen() {
		t.Fatal("nach Reset muss der Breaker zu sein")
	}
}

func TestLoadReactorManifest(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.yaml")
	yaml := `repo: /opt/stack
services:
  master-kanban:
    src: tools/portfolio/master-kanban
    bin: /opt/stack/bin/master-kanban
    unit: master-kanban-serve.service
    probe_kind: http
    health_url: http://127.0.0.1:7780/api/version
    smoke_url: http://127.0.0.1:7780/api/initiatives
`
	if err := os.WriteFile(p, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	m, err := loadReactorManifest(p)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := m.Services["master-kanban"]
	if !ok || rec.ProbeKind != "http" || rec.Unit != "master-kanban-serve.service" {
		t.Fatalf("Manifest missgeparst: %+v", m)
	}
	// Default-Repo bei leerem Feld.
	empty := filepath.Join(dir, "e.yaml")
	_ = os.WriteFile(empty, []byte("services: {}\n"), 0644)
	me, _ := loadReactorManifest(empty)
	if me.Repo != "/opt/stack" {
		t.Fatalf("Default-Repo erwartet /opt/stack, war %q", me.Repo)
	}
}
