package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRigReachable_MissingDir(t *testing.T) {
	reason, ok := rigReachable("/nonexistent/path/that/does/not/exist/xyz")
	if ok {
		t.Fatalf("expected unreachable for missing dir, got reachable")
	}
	if reason != "dir nicht vorhanden" {
		t.Errorf("unexpected reason for missing dir: %q", reason)
	}
}

func TestRigReachable_DirWithoutBeads(t *testing.T) {
	tmp := t.TempDir()
	reason, ok := rigReachable(tmp)
	if ok {
		t.Fatalf("expected unreachable for dir without .beads, got reachable")
	}
	if reason != "kein .beads" {
		t.Errorf("unexpected reason for missing .beads: %q", reason)
	}
}

func TestRigReachable_DirWithBeads(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Mkdir(filepath.Join(tmp, ".beads"), 0755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}
	reason, ok := rigReachable(tmp)
	if !ok {
		t.Fatalf("expected reachable for dir with .beads, got unreachable (%q)", reason)
	}
}

func TestRigReachable_NotADirectory(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	reason, ok := rigReachable(f)
	if ok {
		t.Fatalf("expected unreachable for non-dir, got reachable")
	}
	if reason != "nicht Verzeichnis" {
		t.Errorf("unexpected reason for non-dir: %q", reason)
	}
}
