package main

import (
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFormatUUID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"d01893e537aa4dd7a7f030969e2e5164", "d01893e5-37aa-4dd7-a7f0-30969e2e5164"},
		{"D01893E537AA4DD7A7F030969E2E5164", "d01893e5-37aa-4dd7-a7f0-30969e2e5164"},
		{"short", "short"},
	}

	for _, tc := range tests {
		got := formatUUID(tc.input)
		if got != tc.expected {
			t.Errorf("expected %q, got %q", tc.expected, got)
		}
	}
}

func TestHasPartialProgress_NoRepo(t *testing.T) {
	// If path doesn't exist or is not a git repo, it should return false
	got := hasPartialProgress("00000000000000000000000000000000")
	if got {
		t.Errorf("expected false for nonexistent session")
	}
}

func TestHasPartialProgress_WithRepo(t *testing.T) {
	// Create a temp session dir structured like vibe-kanban
	tmpDir, err := ioutil.TempDir("", "vk-sessions-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	sessionID := "1234567890abcdef1234567890abcdef"
	sessionUUID := formatUUID(sessionID)

	// Create structured sessions/<prefix>/<uuid> directory
	prefix := sessionUUID[:2]
	sessionPath := filepath.Join(tmpDir, prefix, sessionUUID)
	err = os.MkdirAll(sessionPath, 0755)
	if err != nil {
		t.Fatalf("failed to create structured session dir: %v", err)
	}

	// Clone or init a git repository inside sessionPath
	repoPath := filepath.Join(sessionPath, "my-repo")
	err = os.MkdirAll(repoPath, 0755)
	if err != nil {
		t.Fatalf("failed to create repo path: %v", err)
	}

	runCmd := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoPath
		err := cmd.Run()
		if err != nil {
			t.Fatalf("failed to run git command: %v", err)
		}
	}

	runCmd("init", "-b", "main")
	runCmd("config", "user.name", "Test User")
	runCmd("config", "user.email", "test@example.com")

	// Create initial file and commit
	file1 := filepath.Join(repoPath, "file1.txt")
	err = os.WriteFile(file1, []byte("hello"), 0644)
	if err != nil {
		t.Fatalf("failed to write file1: %v", err)
	}
	runCmd("add", "file1.txt")
	runCmd("commit", "-m", "initial commit")

	// Create branch origin/main at HEAD
	runCmd("branch", "origin/main")

	// Create a second commit ahead of origin/main
	file2 := filepath.Join(repoPath, "file2.txt")
	err = os.WriteFile(file2, []byte("world"), 0644)
	if err != nil {
		t.Fatalf("failed to write file2: %v", err)
	}
	runCmd("add", "file2.txt")
	runCmd("commit", "-m", "second commit")

	// Set VK_SESSIONS environment variable so hasPartialProgress finds our mock
	os.Setenv("VK_SESSIONS", tmpDir)
	defer os.Unsetenv("VK_SESSIONS")

	got := hasPartialProgress(sessionID)
	if !got {
		t.Errorf("expected hasPartialProgress to return true for repo with commits ahead of origin/main")
	}
}
