package main

// deploy_reactor_outbox_test.go — Zustandsmaschinen-Tests des Deploy-Reaktors
// (Release-Pipeline WP6). Die vier Rollback-Übergänge und der No-Race gegen den
// Reconciler sind GRÜNE Tests, nicht Erst-im-Incident-Code (Geist Crispin/WP6).
// DB-frei: die Entscheide sind pur; die echten Ledger-Übergänge beweist der
// Game-Day (game-day-deploy.sh) gegen eine Scratch-DB.

import (
	"bufio"
	"encoding/json"
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

// TestEnvEligible — der Stufen-Scope-Filter (sa-deploy-stufen W2). Der prod-
// Reaktor läuft mit environment='prod-mvp' und darf NUR Fabrik-Prod-Zeilen
// (master-kanban/…-adapter) ziehen; der sa-staging-drain mit 'staging' nur die
// SA-Staging-Zeilen. Leerer Scope = alle Stufen (rückwärtskompatibel, altes
// Verhalten). Ohne diesen Filter griff der ungescopte prod-Reaktor die staging-
// Outbox-Zeilen und errorte sie ("kein Rezept") — der wiederkehrende
// Kollisions-Befund (3× notiert). Dieser Test friert die Scope-Semantik ein.
func TestEnvEligible(t *testing.T) {
	cases := []struct {
		name               string
		reactorEnv, rowEnv string
		want               bool
	}{
		{"leerer Scope zieht prod-mvp (rückwärtskompatibel)", "", "prod-mvp", true},
		{"leerer Scope zieht staging (rückwärtskompatibel)", "", "staging", true},
		{"prod-mvp zieht prod-mvp", "prod-mvp", "prod-mvp", true},
		{"prod-mvp lässt staging liegen (Kollisions-Fix)", "prod-mvp", "staging", false},
		{"prod-mvp lässt prod/promote liegen", "prod-mvp", "prod", false},
		{"staging zieht staging", "staging", "staging", true},
		{"staging lässt prod-mvp liegen", "staging", "prod-mvp", false},
		{"staging lässt prod/promote liegen", "staging", "prod", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := envEligible(tc.reactorEnv, tc.rowEnv); got != tc.want {
				t.Fatalf("envEligible(%q, %q) = %v, will %v", tc.reactorEnv, tc.rowEnv, got, tc.want)
			}
		})
	}
}

// TestJourneyVerdict friert den SHADOW-Kontrakt (D14) ein: der journey-run-Exit
// wird zu green|red|harness, und ALLES außer 0/1 (Timeout-Kill, -1, unerwartet)
// zählt als Harness — NIE als „rot". So kann ein Runner-Defekt später nie
// fälschlich einen guten Deploy zurückrollen.
func TestJourneyVerdict(t *testing.T) {
	cases := []struct {
		exit int
		want string
	}{
		{0, "green"},
		{1, "red"},
		{3, "harness"},
		{124, "harness"}, // Timeout-Kill (SIGTERM/coreutils) → nie rot
		{-1, "harness"},  // konnte nicht starten → nie rot
		{2, "harness"},   // unerwarteter Code → nie rot
	}
	for _, c := range cases {
		if got := journeyVerdict(c.exit); got != c.want {
			t.Errorf("journeyVerdict(%d)=%q, want %q", c.exit, got, c.want)
		}
	}
}

// TestJourneyShadow_WritesLine_NeverBlocks — journeyShadow fährt den echten
// exec-Pfad gegen einen STUB-Runner (kein Browser, DB-frei), klassifiziert den
// Exit und hängt GENAU EINE {ts,sha,http_verdict,journey_exit,journey_verdict}-
// Zeile an <dir>/<service>.jsonl. Beweist zugleich das „Shadow heißt Shadow":
// selbst ein rot/Harness-Runner liefert keinen Rückgabewert und kann den
// Deploy-Fluss nicht berühren (die Funktion gibt nichts zurück).
func TestJourneyShadow_WritesLine_NeverBlocks(t *testing.T) {
	dir := t.TempDir()
	shadowDir := filepath.Join(dir, "shadow")

	// Stub-Runner: exitet mit dem Code aus JOURNEY_STUB_EXIT (poka-yoke pro Fall).
	stub := filepath.Join(dir, "journey-run-stub.sh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho \"stub journey-run $*\"\nexit ${JOURNEY_STUB_EXIT:-0}\n"), 0755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name        string
		svc         string
		exit        string
		httpVerdict string
		wantVerdict string
	}{
		{"green journey neben grünem Deploy", "svc-a", "0", "green", "green"},
		{"rote journey ändert grünen Deploy NICHT", "svc-b", "1", "green", "red"},
		{"harness (exit 3) ist neutral", "svc-c", "3", "green", "harness"},
		{"journey neben rolled_back-Deploy", "svc-d", "0", "rollback", "green"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("JOURNEY_STUB_EXIT", tc.exit)
			r := &reactor{
				owner:            "deploy-reactor@test",
				journeyRun:       stub,
				journeyShadowDir: shadowDir,
				journeyShadowTO:  30 * time.Second,
				logf:             func(string, ...any) {},
			}
			o := outboxRow{Service: tc.svc, Environment: "prod-mvp", GitSha: "abc1234def"}
			// Kein Rückgabewert → strukturell kann der Shadow den Deploy nicht drehen.
			r.journeyShadow(o, tc.httpVerdict)

			last := lastJSONL(t, filepath.Join(shadowDir, o.Service+".jsonl"))
			if last["journey_verdict"] != tc.wantVerdict {
				t.Fatalf("journey_verdict=%v, want %q (zeile=%v)", last["journey_verdict"], tc.wantVerdict, last)
			}
			if last["http_verdict"] != tc.httpVerdict {
				t.Fatalf("http_verdict=%v, want %q", last["http_verdict"], tc.httpVerdict)
			}
			if last["sha"] != o.GitSha {
				t.Fatalf("sha=%v, want %q", last["sha"], o.GitSha)
			}
			for _, k := range []string{"ts", "journey_exit"} {
				if _, ok := last[k]; !ok {
					t.Fatalf("Pflichtfeld %q fehlt in Shadow-Zeile: %v", k, last)
				}
			}
		})
	}

	// Append-Semantik: zwei Läufe desselben Service hängen an, überschreiben nicht.
	t.Setenv("JOURNEY_STUB_EXIT", "0")
	r := &reactor{journeyRun: stub, journeyShadowDir: shadowDir, journeyShadowTO: 30 * time.Second, logf: func(string, ...any) {}}
	o := outboxRow{Service: "svc-append", Environment: "prod-mvp", GitSha: "abc1234def"}
	r.journeyShadow(o, "green")
	r.journeyShadow(o, "green")
	if n := countLines(t, filepath.Join(shadowDir, "svc-append.jsonl")); n != 2 {
		t.Fatalf("erwartete 2 angehängte Zeilen für svc-append, war %d", n)
	}
}

// TestJourneyShadow_MissingRunner_Neutral — ein nicht auffindbarer Runner darf
// den Drain NICHT stürzen und wird als Harness verbucht (exec-Startfehler → -1).
func TestJourneyShadow_MissingRunner_Neutral(t *testing.T) {
	dir := t.TempDir()
	r := &reactor{
		journeyRun:       filepath.Join(dir, "does-not-exist"),
		journeyShadowDir: filepath.Join(dir, "shadow"),
		journeyShadowTO:  5 * time.Second,
		logf:             func(string, ...any) {},
	}
	o := outboxRow{Service: "gone", Environment: "prod-mvp", GitSha: "deadbeef"}
	r.journeyShadow(o, "green") // darf nicht paniken
	last := lastJSONL(t, filepath.Join(dir, "shadow", "gone.jsonl"))
	if last["journey_verdict"] != "harness" {
		t.Fatalf("fehlender Runner muss Harness geben, war %v", last["journey_verdict"])
	}
}

func lastJSONL(t *testing.T, path string) map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Shadow-JSONL %s nicht lesbar: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	var lastLine string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := sc.Text(); l != "" {
			lastLine = l
		}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lastLine), &m); err != nil {
		t.Fatalf("Shadow-Zeile kein JSON (%q): %v", lastLine, err)
	}
	return m
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if sc.Text() != "" {
			n++
		}
	}
	return n
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
	// Journey-Shadow ist ADDITIV: fehlt das Feld → false (Bestands-Service
	// unverändert, kein Shadow).
	if rec.JourneyShadow {
		t.Fatalf("journey_shadow ohne Feld muss false sein, war true: %+v", rec)
	}
	// Default-Repo bei leerem Feld.
	empty := filepath.Join(dir, "e.yaml")
	_ = os.WriteFile(empty, []byte("services: {}\n"), 0644)
	me, _ := loadReactorManifest(empty)
	if me.Repo != "/opt/stack" {
		t.Fatalf("Default-Repo erwartet /opt/stack, war %q", me.Repo)
	}
}

// TestRecipeTypeAndMethod — die go|node-Weiche (sa-deploy-stufen W4): ""→go
// (rückwärtskompatibel), case-insensitive 'node', und der Ledger-Stempel.
func TestRecipeTypeAndMethod(t *testing.T) {
	cases := []struct {
		typ   string
		wantT string
		wantM string
	}{
		{"", "go", "deploy-gt"},
		{"go", "go", "deploy-gt"},
		{"node", "node", "deploy-node"},
		{"Node", "node", "deploy-node"},
		{" NODE ", "node", "deploy-node"},
		{"golang", "go", "deploy-gt"}, // nur exakt 'node' schaltet um
	}
	for _, c := range cases {
		rec := ServiceRecipe{Type: c.typ}
		if got := recipeType(rec); got != c.wantT {
			t.Errorf("recipeType(%q)=%q, want %q", c.typ, got, c.wantT)
		}
		if got := deployMethod(rec); got != c.wantM {
			t.Errorf("deployMethod(%q)=%q, want %q", c.typ, got, c.wantM)
		}
	}
}

// TestDeployInvocation — go-Rezept ruft deploy-gt.sh mit --bin, node-Rezept
// ruft deploy-node.sh mit --dest (+ --build-cmd/--unit/--box korrekt).
func TestDeployInvocation(t *testing.T) {
	const gt, nd, repo = "/x/deploy-gt.sh", "/x/deploy-node.sh", "/opt/mirror.git"

	// go: --bin, deploy-gt.sh, kein --dest/--build-cmd
	goRec := ServiceRecipe{Src: "apps/foo", Bin: "/opt/foo/foo", Unit: "foo.service", Box: "root@box"}
	s, a := deployInvocation(goRec, gt, nd, repo, "foo", "abc123")
	if s != gt {
		t.Fatalf("go script=%q, want %q", s, gt)
	}
	if !hasPair(a, "--bin", "/opt/foo/foo") || hasFlag(a, "--dest") || hasFlag(a, "--build-cmd") {
		t.Fatalf("go args falsch: %v", a)
	}
	if !hasPair(a, "--unit", "foo.service") || !hasPair(a, "--box", "root@box") || !hasPair(a, "--ref", "abc123") {
		t.Fatalf("go args unvollständig: %v", a)
	}

	// node: --dest statt --bin, deploy-node.sh, --build-cmd durchgereicht
	ndRec := ServiceRecipe{Type: "node", Src: "apps/fin", Bin: "/opt/sa-fin", Unit: "sa-fin.service",
		Box: "root@staging", BuildCmd: "pnpm run build"}
	s, a = deployInvocation(ndRec, gt, nd, repo, "sa-fin", "deadbeef")
	if s != nd {
		t.Fatalf("node script=%q, want %q", s, nd)
	}
	if hasFlag(a, "--bin") {
		t.Fatalf("node darf kein --bin tragen: %v", a)
	}
	if !hasPair(a, "--dest", "/opt/sa-fin") || !hasPair(a, "--build-cmd", "pnpm run build") {
		t.Fatalf("node args falsch: %v", a)
	}
	if !hasPair(a, "--unit", "sa-fin.service") || !hasPair(a, "--box", "root@staging") || !hasPair(a, "--src", "apps/fin") {
		t.Fatalf("node args unvollständig: %v", a)
	}

	// node ohne build_cmd: kein --build-cmd (deploy-node.sh nimmt seinen Default)
	_, a = deployInvocation(ServiceRecipe{Type: "node", Src: "apps/x", Bin: "/opt/x"}, gt, nd, repo, "x", "sha")
	if hasFlag(a, "--build-cmd") {
		t.Fatalf("leeres build_cmd darf kein --build-cmd erzeugen: %v", a)
	}
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func hasPair(args []string, flag, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}
	return false
}

// TestLoadReactorManifest_NodeType — node-Rezept aus YAML inkl. build_cmd.
func TestLoadReactorManifest_NodeType(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.yaml")
	yaml := `repo: /opt/mirror.git
services:
  staging-node-canary:
    type: node
    src: apps/staging-node-canary
    bin: /opt/sa-staging-node-canary
    unit: sa-staging-node-canary.service
    box: root@167.233.82.201
    build_cmd: pnpm run build
    probe_kind: cli
    health_url: /opt/sa-staging/sa-staging-node-smoke.sh
`
	if err := os.WriteFile(p, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	m, err := loadReactorManifest(p)
	if err != nil {
		t.Fatal(err)
	}
	rec := m.Services["staging-node-canary"]
	if recipeType(rec) != "node" || rec.BuildCmd != "pnpm run build" || rec.Bin != "/opt/sa-staging-node-canary" {
		t.Fatalf("node-Rezept missgeparst: %+v", rec)
	}
}

// TestLoadReactorManifest_JourneyShadow — journey_shadow: true wird geparst
// (WP3/D14), und ein Service ohne das Feld bleibt false (additiv).
func TestLoadReactorManifest_JourneyShadow(t *testing.T) {
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
    journey_shadow: true
  deploy-selftest:
    src: tools/portfolio/master-kanban
    bin: /opt/stack/var/deploy-reactor/selftest-bin/master-kanban
    probe_kind: cli
    health_url: /opt/stack/var/deploy-reactor/selftest-bin/master-kanban
`
	if err := os.WriteFile(p, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
	m, err := loadReactorManifest(p)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Services["master-kanban"].JourneyShadow {
		t.Fatal("master-kanban: journey_shadow: true muss true geparst werden")
	}
	if m.Services["deploy-selftest"].JourneyShadow {
		t.Fatal("deploy-selftest ohne Feld muss false bleiben (additiv, kein Shadow)")
	}
}
