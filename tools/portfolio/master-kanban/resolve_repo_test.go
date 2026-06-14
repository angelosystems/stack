package main

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
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

func TestResolveTargetRepo(t *testing.T) {
	dsn := os.Getenv("PORTFOLIO_DSN")
	if dsn == "" {
		dsn = "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable"
	}

	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skip("skipping integration test; db not reachable:", err)
	}
	defer p.Close()

	// Verify connection
	if err := p.Ping(ctx); err != nil {
		t.Skip("skipping integration test; db ping failed:", err)
	}

	// Clean up any leftovers first
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id IN ('test-init-plan', 'test-init-fallback')")

	// Insert test-init-plan (stayawesome)
	_, err = p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend)
		VALUES ('test-init-plan', 'stayawesome', 'idea', 'Test Plan Initiative', 'plan_file')`)
	if err != nil {
		t.Fatalf("failed to insert test-init-plan: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = 'test-init-plan'")

	// Insert plan_file link for test-init-plan
	_, err = p.Exec(ctx, `INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		VALUES ('test-init-plan', 'plan_file', '/root/stayawesomeOS/docs/plans/test-plan.md')`)
	if err != nil {
		t.Fatalf("failed to insert initiative_link: %v", err)
	}

	// Insert test-init-fallback (solartown, no plan_file links)
	_, err = p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend)
		VALUES ('test-init-fallback', 'solartown', 'idea', 'Test Fallback Initiative', 'vk')`)
	if err != nil {
		t.Fatalf("failed to insert test-init-fallback: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = 'test-init-fallback'")

	// Test case 1: test-init-plan should resolve to /root/stayawesomeOS via linked plan file
	repo, err := resolveTargetRepo(p, "test-init-plan")
	if err != nil {
		t.Errorf("resolveTargetRepo failed for test-init-plan: %v", err)
	}
	expectedRepo := "/root/stayawesomeOS"
	if repo != expectedRepo {
		t.Errorf("expected %s, got %s", expectedRepo, repo)
	}

	// Test case 2: test-init-fallback should fallback to /root/solartown via firma→repo map
	repo, err = resolveTargetRepo(p, "test-init-fallback")
	if err != nil {
		t.Errorf("resolveTargetRepo failed for test-init-fallback: %v", err)
	}
	expectedRepo = "/root/solartown"
	if repo != expectedRepo {
		t.Errorf("expected %s, got %s", expectedRepo, repo)
	}
}
