package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractPEM_PathOnlyNoContentMatch(t *testing.T) {
	cases := []struct {
		path          string
		wantProcess   string
		wantExecutor  string
	}{
		{"/root/.claude/projects/-var-tmp-vibe-kanban-worktrees-1234-sol-tr-abc-solartown/x.jsonl", "claude", ""},
		{"/root/.claude/projects/-opt-quantbot-paperclip-worker-foo/x.jsonl", "paperclip-worker", ""},
		{"/root/.claude/projects/some-gemini-polecat/x.jsonl", "gemini", ""},
		{"/root/.claude/projects/opencode-session/x.jsonl", "opencode", ""},
		{"/root/.claude/projects/flows-dispatch/x.jsonl", "claude", "flows"},
	}
	for _, c := range cases {
		gotProc, gotExec := ExtractPEM(c.path)
		if gotProc != c.wantProcess || gotExec != c.wantExecutor {
			t.Errorf("ExtractPEM(%q) = (%q, %q); want (%q, %q)", c.path, gotProc, gotExec, c.wantProcess, c.wantExecutor)
		}
	}
}

// TestExtractPEM_NoContentSubstringMatch guards against the WP-1 #5 bug:
// content-substring matching caused transcripts that mention "flows"/"gemini"/
// "opencode" in their first 20 lines to be mis-bucketed. After the fix,
// ExtractPEM must ignore content entirely — only the path matters.
func TestExtractPEM_NoContentSubstringMatch(t *testing.T) {
	// A path that says "claude" — content that mentions "flows" many times
	// must NOT change the classification. (Pre-fix this would set executor="flows".)
	path := "/root/.claude/projects/-var-tmp-vibe-kanban-worktrees-1234-sol-tr-abc-solartown/x.jsonl"
	proc, exec := ExtractPEM(path)
	if proc != "claude" {
		t.Errorf("proc = %q; want claude (path-derived only)", proc)
	}
	if exec != "" {
		t.Errorf("executor = %q; want empty (content must not feed classification)", exec)
	}
}

func TestFirstModel_PrefersMessageModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	// Line 1 has top-level model "claude-opus-4-7" (init), line 3 has
	// message.model "glm-4.5" (assistant turn). FirstModel must pick
	// message.model — that's the model actually used for the call.
	content := `{"type":"init","model":"claude-opus-4-7"}
{"type":"user","message":{"role":"user"}}
{"type":"assistant","message":{"model":"glm-4.5","role":"assistant"}}
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}
	got := FirstModel(path)
	if got != "glm-4.5" {
		t.Errorf("FirstModel = %q; want glm-4.5 (first message.model wins)", got)
	}
}

func TestFirstModel_ScansWholeFileNotJustHead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	// GLM transcripts often have model only after line 20 (the bug that
	// caused GLM under-reporting pre-fix: head scan missed it).
	var lines string
	for i := 0; i < 30; i++ {
		lines += `{"type":"user","message":{"role":"user","content":"line"}}` + "\n"
	}
	lines += `{"type":"assistant","message":{"model":"glm-4.7","role":"assistant"}}` + "\n"
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}
	got := FirstModel(path)
	if got != "glm-4.7" {
		t.Errorf("FirstModel = %q; want glm-4.7 (whole-file scan)", got)
	}
}

func TestFirstModel_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "no-model.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user"}}
{"type":"user"}
`), 0o644); err != nil {
		t.Fatalf("write tmp file: %v", err)
	}
	if got := FirstModel(path); got != "" {
		t.Errorf("FirstModel = %q; want empty for file without model field", got)
	}
}

func TestParseWalkRoots_DefaultIncludesReviewHotPool(t *testing.T) {
	t.Setenv("FLEET_PARSE_ROOTS", "")
	roots := parseWalkRoots()
	if len(roots) == 0 {
		t.Fatalf("parseWalkRoots() returned no roots")
	}
	if roots[0] != "/root/.claude/projects" {
		t.Errorf("first root = %q; want /root/.claude/projects", roots[0])
	}
	// If the review-hot-pool exists on disk, it must be included.
	reviewHotPool := "/opt/quantbot/.claude-workbench/.claude/projects"
	if _, err := os.Stat(reviewHotPool); err == nil {
		found := false
		for _, r := range roots {
			if r == reviewHotPool {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("review-hot-pool %s exists but not in roots: %v", reviewHotPool, roots)
		}
	}
}

func TestParseWalkRoots_EnvOverride(t *testing.T) {
	t.Setenv("FLEET_PARSE_ROOTS", "/tmp/a:/tmp/b")
	roots := parseWalkRoots()
	want := []string{"/tmp/a", "/tmp/b"}
	if len(roots) != len(want) {
		t.Fatalf("roots = %v; want %v", roots, want)
	}
	for i := range want {
		if roots[i] != want[i] {
			t.Errorf("roots[%d] = %q; want %q", i, roots[i], want[i])
		}
	}
}
