package main

// deploy_gt_test.go — Integrationstest für ../deploy-gt.sh (Release-Pipeline
// WP5/D12/D14). Beweist am echten Skript + echtem git-Worktree:
//   * der Build ist an den --ref gepinnt (Quelle, nicht HEAD) — Vorwärts UND
//     Rollback bauen den exakten Commit,
//   * ldflags stampt die SHA in die Binary (`version`-Vertrag),
//   * der Swap ist atomar, ein prev-Anker entsteht,
//   * der Live-Baum bleibt unberührt (kein checkout/reset auf dem Repo),
//   * kein Worktree bleibt zurück.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInit richtet ein wegwerfbares git-Repo mit Go-Modul ein.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"}, {"config", "user.name", "T"}, {"config", "user.email", "t@t"},
		{"config", "commit.gpgsign", "false"},
	} {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

// commitMarker legt ein Ein-Datei-Go-Programm mit gegebenem Marker an und
// committet es; gibt die Kurz-SHA zurück. Der Marker steckt in der QUELLE
// (nicht in ldflags) — so unterscheidet der Test „welcher Commit wurde gebaut".
func commitMarker(t *testing.T, dir, marker string) string {
	t.Helper()
	prog := "package main\n\nimport \"fmt\"\n\nvar Sha string\n\n" +
		"func main() { fmt.Println(\"MARKER:" + marker + " SHA:\" + Sha) }\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(prog), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module victimsvc\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "marker " + marker}} {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func deployGtPath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd() // go test läuft im Paket-Verzeichnis
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(wd, "..", "deploy-gt.sh")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("deploy-gt.sh nicht gefunden unter %s: %v", p, err)
	}
	return p
}

func TestDeployGt_ShaPinnedBuildAndSwap(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go nicht auf PATH")
	}
	script := deployGtPath(t)

	repo := t.TempDir()
	gitInit(t, repo)
	shaA := commitMarker(t, repo, "A")
	shaB := commitMarker(t, repo, "B") // HEAD steht jetzt auf B

	bin := filepath.Join(t.TempDir(), "opfer-bin")

	run := func(ref string) string {
		t.Helper()
		cmd := exec.Command(script,
			"--ref", ref, "--service", "victimsvc", "--src", ".",
			"--bin", bin, "--repo", repo, "--json")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("deploy-gt.sh --ref %s missglückte: %v\n%s", ref, err, out)
		}
		return string(out)
	}
	marker := func() string {
		t.Helper()
		out, err := exec.Command(bin).Output()
		if err != nil {
			t.Fatalf("Binary lief nicht: %v", err)
		}
		return strings.TrimSpace(string(out))
	}

	// Deploy A, obwohl HEAD=B → die QUELLE muss A sein (Worktree-Pinning).
	run(shaA)
	if got := marker(); !strings.Contains(got, "MARKER:A") || !strings.Contains(got, "SHA:"+shaA) {
		t.Fatalf("Deploy A: Binary meldet %q, erwartet MARKER:A + SHA:%s", got, shaA)
	}

	// Rollback/Vorwärts auf B → jetzt muss die Quelle B sein.
	run(shaB)
	if got := marker(); !strings.Contains(got, "MARKER:B") || !strings.Contains(got, "SHA:"+shaB) {
		t.Fatalf("Deploy B: Binary meldet %q, erwartet MARKER:B + SHA:%s", got, shaB)
	}

	// prev-Anker entstand beim zweiten Deploy (A wird gesichert).
	if _, err := os.Stat(bin + ".prev"); err != nil {
		t.Errorf("prev-Anker %s.prev misst: %v", bin, err)
	}

	// Live-Baum unberührt: HEAD steht weiter auf B, Arbeitsbaum sauber.
	head, _ := exec.Command("git", "-C", repo, "rev-parse", "--short", "HEAD").Output()
	if strings.TrimSpace(string(head)) != shaB {
		t.Errorf("Live-HEAD wanderte auf %s, sollte %s bleiben (kein checkout/reset!)", strings.TrimSpace(string(head)), shaB)
	}
	status, _ := exec.Command("git", "-C", repo, "status", "--porcelain").Output()
	if strings.TrimSpace(string(status)) != "" {
		t.Errorf("Live-Baum nicht sauber nach Deploy: %q", status)
	}
	// Kein Worktree-Leichnam.
	wl, _ := exec.Command("git", "-C", repo, "worktree", "list").Output()
	if strings.Count(string(wl), "\n") > 1 {
		t.Errorf("Worktree blieb zurück:\n%s", wl)
	}
}

func TestDeployGt_ArgGuards(t *testing.T) {
	script := deployGtPath(t)
	cases := []struct {
		name string
		args []string
	}{
		{"ref misst", []string{"--service", "s", "--src", ".", "--bin", "/tmp/x"}},
		{"bin relativ", []string{"--ref", "HEAD", "--service", "s", "--src", ".", "--bin", "relativ"}},
		{"repo kein git", []string{"--ref", "HEAD", "--service", "s", "--src", ".", "--bin", "/tmp/x", "--repo", t.TempDir()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(script, tc.args...)
			err := cmd.Run()
			ee, ok := err.(*exec.ExitError)
			if !ok || ee.ExitCode() != 64 {
				t.Fatalf("erwartet Exit 64 (Aufruf-Fehler), bekam %v", err)
			}
		})
	}
}
