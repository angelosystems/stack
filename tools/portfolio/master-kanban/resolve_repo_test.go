package main

import (
	"os"
	"testing"
)

func TestGetReposMap(t *testing.T) {
	// Set custom environment
	os.Setenv("PLANFILE_REPOS", "/root/foo=stayawesome,/root/bar=quantbot")
	defer os.Unsetenv("PLANFILE_REPOS")

	repos, paths := getReposMap()

	// Verify stayawesome mapping
	if repos["stayawesome"] != "/root/foo" {
		t.Errorf("expected /root/foo, got %s", repos["stayawesome"])
	}

	// Verify quantbot mapping
	if repos["quantbot"] != "/root/bar" {
		t.Errorf("expected /root/bar, got %s", repos["quantbot"])
	}

	// Verify angeloos still falls back to default
	if repos["angeloos"] != "/opt/stack" {
		t.Errorf("expected /opt/stack, got %s", repos["angeloos"])
	}

	// Paths should be sorted by length descending
	if len(paths) < 2 {
		t.Errorf("expected multiple paths, got %d", len(paths))
	}
	for i := 0; i < len(paths)-1; i++ {
		if len(paths[i]) < len(paths[i+1]) {
			t.Errorf("paths not sorted by length descending: %v", paths)
		}
	}
}
