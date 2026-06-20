package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

// AcquireSageLease attempts to atomically acquire a lease/lock for a bead and
// increments its heal counter in a single atomic database operation.
// Returns the updated heal counter and locked_until time if successful.
// If the lease is already held by another client, returns an error.
func AcquireSageLease(ctx context.Context, p *pgxpool.Pool, beadID string, duration time.Duration, lockedBy string) (int, time.Time, error) {
	lockedUntil := time.Now().Add(duration)

	// Ensure the table exists (self-healing for tests/environments)
	_, _ = p.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS portfolio.sage_lease (
			bead_id       text PRIMARY KEY,
			locked_until  timestamptz NOT NULL,
			locked_by     text NOT NULL,
			heal_counter  integer DEFAULT 0 NOT NULL,
			updated_at    timestamptz DEFAULT now() NOT NULL
		)
	`)

	// Perform atomic upsert/update with condition:
	// Only update locked_by and locked_until, and increment heal_counter if:
	// 1) The row does not exist, or
	// 2) The existing lease has expired (locked_until < now())
	// RETURNING will return 1 row only if the INSERT succeeded or the UPDATE's WHERE was true.
	var healCounter int
	var actualUntil time.Time
	var actualBy string

	query := `
		INSERT INTO portfolio.sage_lease (bead_id, locked_until, locked_by, heal_counter, updated_at)
		VALUES ($1, $2, $3, 1, now())
		ON CONFLICT (bead_id) DO UPDATE
		SET
			heal_counter = portfolio.sage_lease.heal_counter + 1,
			locked_until = EXCLUDED.locked_until,
			locked_by = EXCLUDED.locked_by,
			updated_at = now()
		WHERE portfolio.sage_lease.locked_until < now()
		RETURNING heal_counter, locked_until, locked_by
	`

	err := p.QueryRow(ctx, query, beadID, lockedUntil, lockedBy).Scan(&healCounter, &actualUntil, &actualBy)
	if err != nil {
		// If no row is returned (pgx.ErrNoRows), the lease acquisition failed
		// because the lease is still active (locked_until >= now()).
		return 0, time.Time{}, fmt.Errorf("lease acquisition failed for bead %s (active lease exists)", beadID)
	}

	return healCounter, actualUntil, nil
}

// ReleaseSageLease releases an actively held lease by setting locked_until to now.
func ReleaseSageLease(ctx context.Context, p *pgxpool.Pool, beadID string, lockedBy string) error {
	_, err := p.Exec(ctx, `
		UPDATE portfolio.sage_lease
		SET locked_until = now()
		WHERE bead_id = $1 AND locked_by = $2
	`, beadID, lockedBy)
	return err
}

// GetHealCounter returns the current heal counter for a bead.
func GetHealCounter(ctx context.Context, p *pgxpool.Pool, beadID string) (int, error) {
	var counter int
	err := p.QueryRow(ctx, `
		SELECT heal_counter FROM portfolio.sage_lease WHERE bead_id = $1
	`, beadID).Scan(&counter)
	if err != nil {
		return 0, nil // Return 0 if no record exists
	}
	return counter, nil
}

// ExecuteSageAction attempts to acquire a lease for a bead/workspace and, if successful,
// executes the provided action function.
// If the lease acquisition fails (e.g., because a lease is already active),
// it will not execute the action and will return an error indicating that the action was blocked.
// This ensures that the heal counter increment and the action execution are coordinated atomically.
func ExecuteSageAction(ctx context.Context, p *pgxpool.Pool, beadID string, duration time.Duration, lockedBy string, action func() error) error {
	// 1. Attempt to atomically acquire the lease and increment the heal counter
	_, _, err := AcquireSageLease(ctx, p, beadID, duration, lockedBy)
	if err != nil {
		return fmt.Errorf("failed to acquire lease for action: %w", err)
	}

	// 2. Execute the action
	if err := action(); err != nil {
		// If the action failed, release the lease early so that it can be retried,
		// but the heal counter increment remains recorded (committed upon AcquireSageLease).
		_ = ReleaseSageLease(ctx, p, beadID, lockedBy)
		return fmt.Errorf("sage action failed: %w", err)
	}

	return nil
}

// cmdSteward returns the Cobra command for the vk-Sage steward.
func cmdSteward() *cobra.Command {
	c := &cobra.Command{
		Use:   "steward",
		Short: "vk-Sage Workspace Steward (Phase 1 read-only check and event logger)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStewardPhase1()
		},
	}
	return c
}

// getInitiativeForBead looks up the Postgres initiative ID for a given Bead ID.
func getInitiativeForBead(ctx context.Context, p *pgxpool.Pool, beadID string) (string, error) {
	var initID string
	err := p.QueryRow(ctx, `
		SELECT initiative_id FROM portfolio.initiative_link
		WHERE kind = 'bead' AND ref = $1
	`, beadID).Scan(&initID)
	if err != nil {
		return "", err
	}
	return initID, nil
}

// runStewardPhase1 implements the Phase 1 read-only detection, classification,
// Board-Event logging (kind=sage_action) on Postgres, and Dry-Run-Report generation.
func runStewardPhase1() error {
	p := connect()
	ctx := context.Background()

	vkDB := envOr("VK_DB", "/root/.local/share/vibe-kanban/db.v2.sqlite")
	if _, err := os.Stat(vkDB); os.IsNotExist(err) {
		return fmt.Errorf("vibe-kanban SQLite database not found at %s", vkDB)
	}

	// Query unarchived and archived workspaces matching the four corpses from SQLite
	query := `
		SELECT 
			hex(w.id),
			w.name,
			hex(w.task_id),
			ep.status,
			ep.exit_code
		FROM workspaces w
		LEFT JOIN sessions s ON s.workspace_id = w.id
		LEFT JOIN execution_processes ep ON ep.session_id = s.id
		WHERE w.name LIKE '%rituale%'
		   OR w.name LIKE '%st-ib5e%'
		   OR w.name LIKE '%st-yozd%'
		   OR w.name LIKE '%st-1bpf%'
		ORDER BY w.created_at DESC, ep.created_at DESC;
	`
	cmd := exec.Command("sqlite3", "-readonly", "-separator", "|", vkDB, query)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to query vibe-kanban SQLite DB: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")

	type wsInfo struct {
		id       string
		name     string
		hasTask  bool
		epStatus string
		exitCode string
	}

	workspaces := make(map[string]*wsInfo)
	for _, line := range lines {
		parts := strings.Split(line, "|")
		if len(parts) < 5 {
			continue
		}
		id := parts[0]
		name := parts[1]
		hasTask := parts[2] != ""
		epStatus := parts[3]
		exitCode := parts[4]

		if _, ok := workspaces[id]; !ok {
			workspaces[id] = &wsInfo{
				id:       id,
				name:     name,
				hasTask:  hasTask,
				epStatus: epStatus,
				exitCode: exitCode,
			}
		}
	}

	categories := []struct {
		key      string
		beadID   string
		class    string
		action   string
		isCorpse bool
	}{
		{key: "rituale", beadID: "", class: "broken worktree / Setup-Fail / Workspace ohne Bead", action: "archivieren"},
		{key: "st-ib5e", beadID: "st-ib5e", class: "no-commits-exit1 + Ziel schon erledigt", action: "Bead „already done“ schließen + archivieren"},
		{key: "st-yozd", beadID: "st-yozd", class: "no-commits-exit1 + Arbeit echt offen", action: "re-dispatch mit diagnose-geschärftem/re-scopetem Prompt"},
		{key: "st-1bpf", beadID: "st-1bpf", class: "no-commits-exit1 + Arbeit echt offen", action: "re-dispatch mit geschärfter Fehlerdiagnose"},
	}

	matchedWorkspaces := make(map[string]*wsInfo)
	for _, ws := range workspaces {
		isRituale := strings.Contains(strings.ToLower(ws.name), "rituale")
		isIb5e := strings.Contains(strings.ToLower(ws.name), "st-ib5e")
		isYozd := strings.Contains(strings.ToLower(ws.name), "st-yozd")
		is1bpf := strings.Contains(strings.ToLower(ws.name), "st-1bpf")

		if !isRituale && !isIb5e && !isYozd && !is1bpf {
			continue
		}

		// Skip completed successful ones for non-rituale
		if !isRituale && ws.epStatus != "failed" && ws.epStatus != "killed" {
			continue
		}

		var categoryKey string
		switch {
		case isRituale:
			categoryKey = "rituale"
		case isIb5e:
			categoryKey = "st-ib5e"
		case isYozd:
			categoryKey = "st-yozd"
		case is1bpf:
			categoryKey = "st-1bpf"
		}

		if _, exists := matchedWorkspaces[categoryKey]; !exists {
			matchedWorkspaces[categoryKey] = ws
		}
	}

	// 1. Generate & print Dry-Run-Report
	fmt.Println("=== SAGE WORKSPACE STEWARD DRY-RUN REPORT ===")
	fmt.Printf("%-12s | %-12s | %-45s | %s\n", "Workspace ID", "Bead ID", "Classification", "Proposed Action")
	fmt.Println(strings.Repeat("-", 120))

	for _, cat := range categories {
		ws, found := matchedWorkspaces[cat.key]
		if !found {
			fmt.Printf("%-12s | %-12s | %-45s | %s\n", "NOT FOUND", cat.beadID, cat.class, cat.action)
			continue
		}
		fmt.Printf("%-12s | %-12s | %-45s | %s\n", ws.id[:8], cat.beadID, cat.class, cat.action)
	}
	fmt.Println()

	// 2. Visible Board-Event Logging (kind=sage_action) on PostgreSQL
	fmt.Println("=== POSTING BOARD EVENTS (kind=sage_action) ===")
	for _, cat := range categories {
		if cat.beadID == "" {
			continue
		}
		ws, found := matchedWorkspaces[cat.key]
		if !found {
			continue
		}

		// Find initiative for this bead
		initID, err := getInitiativeForBead(ctx, p, cat.beadID)
		if err != nil {
			fmt.Printf("✗ Error: could not find initiative for bead %s: %v\n", cat.beadID, err)
			continue
		}

		// Construct payload
		payload := map[string]any{
			"bead_id":         cat.beadID,
			"workspace_id":    ws.id,
			"workspace_name":  ws.name,
			"classification":  cat.class,
			"proposed_action": cat.action,
			"message":         fmt.Sprintf("Sage classified workspace %s (%s) as '%s'. Proposed action: %s.", ws.id[:8], ws.name, cat.class, cat.action),
		}

		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			fmt.Printf("✗ Error: failed to marshal payload for %s: %v\n", cat.beadID, err)
			continue
		}

		// Idempotent check
		var eventExists bool
		err = p.QueryRow(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM portfolio.initiative_event
				WHERE initiative_id = $1
				  AND kind = 'sage_action'
				  AND payload->>'workspace_id' = $2
			)
		`, initID, ws.id).Scan(&eventExists)
		if err != nil {
			fmt.Printf("✗ Error: failed to check event existence for %s: %v\n", cat.beadID, err)
			continue
		}

		if !eventExists {
			_, err = p.Exec(ctx, `
				INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
				VALUES ($1, 'sage_action', 'vk', $2::jsonb, 'sage')
			`, initID, string(payloadBytes))
			if err != nil {
				fmt.Printf("✗ Error: failed to insert sage_action event for %s: %v\n", cat.beadID, err)
			} else {
				fmt.Printf("✓ Logged board event (kind=sage_action) on initiative %s for bead %s.\n", initID, cat.beadID)
			}
		} else {
			fmt.Printf("✓ Board event on initiative %s for bead %s already exists (idempotent).\n", initID, cat.beadID)
		}
	}

	return nil
}
