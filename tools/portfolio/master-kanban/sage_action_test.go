package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

func TestSageDryRun_SC4(t *testing.T) {
	testDSN := os.Getenv("PORTFOLIO_DSN")
	if testDSN == "" {
		testDSN = "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable"
	}

	// Assign the package-level variable dsn so connect() uses it
	dsn = testDSN

	ctx := context.Background()
	p, err := pgxpool.New(ctx, testDSN)
	if err != nil {
		t.Skip("skipping integration test; db not reachable:", err)
	}
	defer p.Close()

	if err := p.Ping(ctx); err != nil {
		t.Skip("skipping integration test; db ping failed:", err)
	}

	// 1. Clean up any existing sage_action events for the 4 target workspaces
	targetWSIDs := []string{
		"05021F1F765846E299B6A36B39DC39F8", // sol-st-yozd
		"64D07879DB694345BFA59E9D321AAC08", // sol-st-1bpf
		"B842765043A04994B61AACF51E019956", // sol-st-ib5e
		"935D9575FDF54F9C816381B9A97DD481", // v3s34-rituale
	}

	for _, wsID := range targetWSIDs {
		_, err = p.Exec(ctx, `
			DELETE FROM portfolio.initiative_event
			WHERE kind = 'sage_action' AND (payload->>'workspace_id') = $1
		`, wsID)
		if err != nil {
			t.Fatalf("failed to clean up old sage_action events: %v", err)
		}
	}

	// Ensure pool is reset so cmdSage connects with the correct dsn
	pool = nil

	// 2. Execute cmdSage programmatically (dry-run mode)
	cmd := cmdSage()
	cmd.SetArgs([]string{}) // Dry-run
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("failed to execute sage command: %v", err)
	}

<<<<<<< HEAD
	// 3. Verify that the board events (sage_action) were logged for each of the 3 target workspaces with beads
	expectedWSIDs := []string{
		"05021F1F765846E299B6A36B39DC39F8", // sol-st-yozd
		"64D07879DB694345BFA59E9D321AAC08", // sol-st-1bpf
		"B842765043A04994B61AACF51E019956", // sol-st-ib5e
	}
	for _, wsID := range expectedWSIDs {
=======
	// 3. Verify that the board events (sage_action) were logged for each of the 3 target workspaces
	for _, wsID := range targetWSIDs {
		if wsID == "935D9575FDF54F9C816381B9A97DD481" {
			continue // Skip rituale as it has no bead/initiative to log board events to
		}

>>>>>>> origin/polecat/basalt/st-r8wm2@mqmky2zl
		var exists bool
		err = p.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM portfolio.initiative_event
				WHERE kind = 'sage_action' AND (payload->>'workspace_id') = $1
			)
		`, wsID).Scan(&exists)
		if err != nil {
			t.Fatalf("failed to check logged board event: %v", err)
		}

		if !exists {
			t.Errorf("expected board-event (sage_action) to be logged for workspace %s, but none was found", wsID)
		} else {
			t.Logf("✓ Verified: Board-Event (sage_action) logged for workspace %s", wsID)
		}
	}
}

func TestSageAction_HealAndReset(t *testing.T) {
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

	testBeadID := "st-test-sage-heal-reset"
	testWSID := "05021F1F765846E299B6A36B39DC39F8"

	// Clean up any existing state
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_lease WHERE bead_id=$1", testBeadID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_heal_count WHERE bead_id=$1", testBeadID)

	defer func() {
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_lease WHERE bead_id=$1", testBeadID)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_heal_count WHERE bead_id=$1", testBeadID)
	}()

	actionFn := func(tx pgx.Tx, healCount int) error {
		return nil
	}

	// 1. Initial execution with hasPartialProgress = false (should increment heal counter to 1)
	acquired, err := ExecuteSageAction(ctx, p, testBeadID, testWSID, "sage-test-actor", false, actionFn)
	if err != nil {
		t.Fatalf("failed to execute first sage action: %v", err)
	}
	if !acquired {
		t.Errorf("expected to acquire lease on first try")
	}

	var count int
	err = p.QueryRow(ctx, "SELECT heal_count FROM portfolio.sage_heal_count WHERE bead_id=$1", testBeadID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query heal count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected heal count to be 1, got %d", count)
	}

	// 2. Second execution with hasPartialProgress = false (should increment heal counter to 2)
	// Clear the lease lock so we can run again immediately
	_, _ = p.Exec(ctx, "UPDATE portfolio.sage_lease SET locked_until=NOW() - INTERVAL '1 minute' WHERE bead_id=$1", testBeadID)

	acquired, err = ExecuteSageAction(ctx, p, testBeadID, testWSID, "sage-test-actor", false, actionFn)
	if err != nil {
		t.Fatalf("failed to execute second sage action: %v", err)
	}
	if !acquired {
		t.Errorf("expected to acquire lease on second try")
	}

	err = p.QueryRow(ctx, "SELECT heal_count FROM portfolio.sage_heal_count WHERE bead_id=$1", testBeadID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query heal count: %v", err)
	}
	if count != 2 {
		t.Errorf("expected heal count to be 2, got %d", count)
	}

	// 3. Third execution with hasPartialProgress = true (should reset heal counter to 0)
	// Clear the lease lock
	_, _ = p.Exec(ctx, "UPDATE portfolio.sage_lease SET locked_until=NOW() - INTERVAL '1 minute' WHERE bead_id=$1", testBeadID)

	acquired, err = ExecuteSageAction(ctx, p, testBeadID, testWSID, "sage-test-actor", true, actionFn)
	if err != nil {
		t.Fatalf("failed to execute third sage action: %v", err)
	}
	if !acquired {
		t.Errorf("expected to acquire lease on third try")
	}

	err = p.QueryRow(ctx, "SELECT heal_count FROM portfolio.sage_heal_count WHERE bead_id=$1", testBeadID).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query heal count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected heal count to be reset to 0, got %d", count)
	}
}

func TestBuildDiagnosisPrompt(t *testing.T) {
	// Test yozd prompt
	promptYozd := buildDiagnosisPrompt(1, true, false)
	if !strings.Contains(promptYozd, "Heal Attempt #1") {
		t.Errorf("expected prompt to contain Heal Attempt #1, got: %s", promptYozd)
	}
	if !strings.Contains(promptYozd, "Backlog-Tab hat heute nur einen Triage-Knopf") {
		t.Errorf("expected prompt to contain yozd-specific diagnosis, got: %s", promptYozd)
	}

	// Test 1bpf prompt
	prompt1bpf := buildDiagnosisPrompt(2, false, true)
	if !strings.Contains(prompt1bpf, "Heal Attempt #2") {
		t.Errorf("expected prompt to contain Heal Attempt #2, got: %s", prompt1bpf)
	}
	if !strings.Contains(prompt1bpf, "cockpit hat firma-Stripes aber nicht die R5 Lane-Badges") {
		t.Errorf("expected prompt to contain 1bpf-specific diagnosis, got: %s", prompt1bpf)
	}

	// Test generic fallback prompt
	promptGeneric := buildDiagnosisPrompt(3, false, false)
	if !strings.Contains(promptGeneric, "Heal Attempt #3") {
		t.Errorf("expected prompt to contain Heal Attempt #3, got: %s", promptGeneric)
	}
	if !strings.Contains(promptGeneric, "The previous run failed with zero commits") {
		t.Errorf("expected prompt to contain generic diagnosis, got: %s", promptGeneric)
	}
}
