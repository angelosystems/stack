package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCheckDoneProbe_PostgresCheck(t *testing.T) {
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

	if err := p.Ping(ctx); err != nil {
		t.Skip("skipping integration test; db ping failed:", err)
	}

	sp, err := solartownPool()
	if err != nil {
		t.Fatalf("failed to connect to solartown pool: %v", err)
	}

	// 1. Setup a test bead in beads.issues
	testBeadID := "st-test-sage-probe"
	_, _ = sp.Exec(ctx, "DELETE FROM beads.issues WHERE id=$1", testBeadID)
	_, err = sp.Exec(ctx, `
		INSERT INTO beads.issues (id, title, status, rig, created_at)
		VALUES ($1, 'Test Sage Probe', 'open', 'stack', $2)
	`, testBeadID, time.Now())
	if err != nil {
		t.Fatalf("failed to insert test bead: %v", err)
	}
	defer sp.Exec(ctx, "DELETE FROM beads.issues WHERE id=$1", testBeadID)

	vkDB := "/root/.local/share/vibe-kanban/db.v2.sqlite"

	// 2. Since bead is open and no workspace exists, Done-Probe should be false
	done := checkDoneProbe(p, vkDB, "SOMEWS", "", testBeadID)
	if done {
		t.Errorf("expected Done-Probe to be false for open bead with no other completed workspaces")
	}

	// 3. Close the bead in Postgres
	_, err = sp.Exec(ctx, "UPDATE beads.issues SET status='closed' WHERE id=$1", testBeadID)
	if err != nil {
		t.Fatalf("failed to close test bead: %v", err)
	}

	// 4. Done-Probe should now be true
	done = checkDoneProbe(p, vkDB, "SOMEWS", "", testBeadID)
	if !done {
		t.Errorf("expected Done-Probe to be true for closed bead in Postgres")
	}
}
