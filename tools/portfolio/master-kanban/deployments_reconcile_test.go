package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func head(status, sha string, deployedAgo time.Duration, ownedUntil *time.Time) deploymentHead {
	return deploymentHead{
		ID: 1, Service: "svc", ProbeKind: "http", Environment: "prod-mvp",
		Version: "v1.0.0", GitSha: sha, Status: status,
		DeployedAt: time.Now().Add(-deployedAgo), OwnedUntil: ownedUntil,
	}
}

// Die Übergangstabelle des Reconcilers (D13) — jeder Fall ein grüner Test,
// nicht Erst-im-Incident-Code (Geist von WP6/Crispin).
func TestDecideReconcile_Uebergangstabelle(t *testing.T) {
	now := time.Now()
	window := 10 * time.Minute
	future := now.Add(5 * time.Minute)
	okProbe := probeResult{Reached: true, Sha: "abc1234"}
	wrongProbe := probeResult{Reached: true, Sha: "fff9999"}
	redProbe := probeResult{Err: "nicht erreichbar"}

	cases := []struct {
		name       string
		row        deploymentHead
		probe      probeResult
		wantStatus string // "" = kein Übergang
	}{
		{"geleast wird übersprungen, auch bei Match", head("deploying", "abc1234", time.Hour, &future), okProbe, ""},
		{"pending (Outbox) gehört dem Reaktor", head("pending", "abc1234", time.Hour, nil), okProbe, ""},
		{"rolled_back ist terminal (D15)", head("rolled_back", "abc1234", time.Hour, nil), okProbe, ""},
		{"deploying im Smoke-Fenster: rot ≠ errored (D18)", head("deploying", "abc1234", time.Minute, nil), redProbe, ""},
		{"deploying + Match → live (Probe bestätigt nur)", head("deploying", "abc1234", time.Minute, nil), okProbe, "live"},
		{"deploying jenseits Fenster + rot → errored", head("deploying", "abc1234", time.Hour, nil), redProbe, "errored"},
		{"live + Match → bleibt", head("live", "abc1234", time.Hour, nil), okProbe, ""},
		{"live + falsche SHA → errored", head("live", "abc1234", time.Hour, nil), wrongProbe, "errored"},
		{"live + rot → errored (WP3-Done-Kriterium)", head("live", "abc1234", time.Hour, nil), redProbe, "errored"},
		{"live + rot im Fenster: Restart ≠ rot", head("live", "abc1234", time.Minute, nil), redProbe, ""},
		{"errored + Match → live (Selbstheilung)", head("errored", "abc1234", time.Hour, nil), okProbe, "live"},
		{"errored + rot → bleibt errored", head("errored", "abc1234", time.Hour, nil), redProbe, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideReconcile(tc.row, tc.probe, now, window)
			if got.NewStatus != tc.wantStatus {
				t.Fatalf("decideReconcile(%s, probe=%+v) = %q (%s), will %q",
					tc.row.Status, tc.probe, got.NewStatus, got.Reason, tc.wantStatus)
			}
		})
	}
}

func TestShaMatch(t *testing.T) {
	cases := []struct {
		name    string
		rowSha  string
		probe   probeResult
		want    bool
	}{
		{"Kurz-SHA-Zeile vs Lang-SHA-Probe", "abc1234", probeResult{Sha: "abc1234def5678900000000000000000000000000"}, true},
		{"Lang-SHA-Zeile vs Kurz-SHA-Probe", "abc1234def5678900000000000000000000000000", probeResult{Sha: "abc1234"}, true},
		{"exakt gleich", "abc1234", probeResult{Sha: "abc1234"}, true},
		{"verschieden", "abc1234", probeResult{Sha: "fff9999"}, false},
		{"sha leer → version-Fallback greift", "v1.0.0", probeResult{Version: "v1.0.0"}, true},
		{"sha 'unknown' (ungestampfter Build) → version-Fallback", "v1.0.0", probeResult{Sha: "unknown", Version: "v1.0.0"}, true},
		{"alles leer → kein Match", "abc1234", probeResult{}, false},
		{"Zeile ohne SHA → kein Match", "", probeResult{Sha: "abc1234"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := deploymentHead{GitSha: tc.rowSha}
			if got := shaMatch(row, tc.probe); got != tc.want {
				t.Fatalf("shaMatch(%q, %+v) = %v, will %v", tc.rowSha, tc.probe, got, tc.want)
			}
		})
	}
}

func TestProbeHTTP(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"service":"svc","version":"v1.2.3","sha":"abc1234","built_at":"2026-07-06T00:00:00Z","env":"prod-mvp"}`))
	}))
	defer good.Close()
	res := probeHTTP(context.Background(), good.URL, 2*time.Second)
	if !res.Reached || res.Sha != "abc1234" || res.Version != "v1.2.3" || res.Env != "prod-mvp" {
		t.Fatalf("probeHTTP good = %+v", res)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`kein json`))
	}))
	defer bad.Close()
	if res := probeHTTP(context.Background(), bad.URL, 2*time.Second); res.Reached {
		t.Fatalf("kaputter Vertrag darf nicht Reached sein: %+v", res)
	}

	e500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "kaputt", 500)
	}))
	defer e500.Close()
	if res := probeHTTP(context.Background(), e500.URL, 2*time.Second); res.Reached {
		t.Fatalf("HTTP 500 darf nicht Reached sein: %+v", res)
	}

	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	refused.Close() // Port zu → connection refused
	if res := probeHTTP(context.Background(), refused.URL, 2*time.Second); res.Reached {
		t.Fatalf("connection refused darf nicht Reached sein: %+v", res)
	}
}

func TestProbeCLI(t *testing.T) {
	dir := t.TempDir()
	mock := filepath.Join(dir, "svc-binary")
	script := "#!/bin/sh\n" +
		`echo '{"service":"svc","version":"v2.0.0","sha":"def5678","built_at":"2026-07-06T00:00:00Z","env":"prod-mvp"}'` + "\n"
	if err := os.WriteFile(mock, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	res := probeCLI(context.Background(), mock, 2*time.Second)
	if !res.Reached || res.Sha != "def5678" {
		t.Fatalf("probeCLI mock = %+v", res)
	}

	// Poka-yoke: relative Pfade verweigert der Prober.
	if res := probeCLI(context.Background(), "svc-binary", 2*time.Second); res.Reached {
		t.Fatalf("relativer Pfad darf nicht sondiert werden: %+v", res)
	}

	// Binary, das den Vertrag nicht spricht → nicht Reached.
	broken := filepath.Join(dir, "broken")
	if err := os.WriteFile(broken, []byte("#!/bin/sh\necho kaputt\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if res := probeCLI(context.Background(), broken, 2*time.Second); res.Reached {
		t.Fatalf("kaputter CLI-Vertrag darf nicht Reached sein: %+v", res)
	}
}

func TestProbeRow_OhneHealthURL(t *testing.T) {
	row := head("live", "abc1234", time.Hour, nil)
	row.HealthURL = nil
	if res := probeRow(context.Background(), row, time.Second); res.Reached {
		t.Fatalf("Zeile ohne health_url darf nicht Reached sein: %+v", res)
	}
}

func TestBuildReleasesQuery(t *testing.T) {
	// Kein Filter: Basis-Query, keine Args, kein WHERE.
	sql, args := buildReleasesQuery("")
	if len(args) != 0 {
		t.Fatalf("ohne service-Param: keine Args erwartet, bekam %v", args)
	}
	if strings.Contains(sql, "WHERE") {
		t.Fatalf("ohne service-Param: kein WHERE erwartet, bekam:\n%s", sql)
	}
	if !strings.Contains(sql, "DISTINCT ON (d.service, d.environment)") ||
		!strings.HasSuffix(strings.TrimSpace(sql), "d.deployed_at DESC") {
		t.Fatalf("Basis-Query verstümmelt:\n%s", sql)
	}
	// Ledger-Felder fürs Cockpit: wer (deployed_by) + womit (deploy_method)
	// gedeployt hat, müssen im Head-Read stehen, sonst zeigt der Releases-Tab
	// die Deploy-Loop-Herkunft nicht an.
	if !strings.Contains(sql, "d.deployed_by") {
		t.Fatalf("deployed_by fehlt im Select:\n%s", sql)
	}
	if !strings.Contains(sql, "d.deploy_method") {
		t.Fatalf("deploy_method fehlt im Select:\n%s", sql)
	}

	// Whitespace zählt als leer (poka-yoke gegen versehentlichen Leerfilter).
	if _, a := buildReleasesQuery("   "); len(a) != 0 {
		t.Fatalf("Whitespace-service darf nicht filtern, bekam Args %v", a)
	}

	// Mit Filter: parametrisiertes WHERE + genau ein getrimmtes Arg, ORDER BY bleibt hinten.
	sql, args = buildReleasesQuery("  master-kanban  ")
	if len(args) != 1 || args[0] != "master-kanban" {
		t.Fatalf("service-Arg getrimmt='master-kanban' erwartet, bekam %v", args)
	}
	if !strings.Contains(sql, "WHERE d.service = $1") {
		t.Fatalf("parametrisiertes WHERE erwartet, bekam:\n%s", sql)
	}
	if strings.Contains(sql, "master-kanban") {
		t.Fatalf("Service-Wert darf NICHT ins SQL interpoliert werden (Injection):\n%s", sql)
	}
	if strings.Index(sql, "WHERE") > strings.Index(sql, "ORDER BY") {
		t.Fatalf("WHERE muss vor ORDER BY stehen:\n%s", sql)
	}
}
