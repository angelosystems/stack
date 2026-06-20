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

type wsProcess struct {
	status   string
	exitCode string
}

type workspaceInfo struct {
	id             string
	name           string
	taskID         string
	latestStatus   string
	latestExitCode string
	allProcesses   []wsProcess
}

func cmdSteward() *cobra.Command {
	var vkDBPath string
	c := &cobra.Command{
		Use:   "steward",
		Short: "Runs the Workspace-Steward dry-run/read-only report and registers classification events on the board",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := connect()
			if vkDBPath == "" {
				vkDBPath = envOr("VIBE_KANBAN_DB", "/root/.local/share/vibe-kanban/db.v2.sqlite")
			}
			return runSteward(p, vkDBPath)
		},
	}
	c.Flags().StringVar(&vkDBPath, "vk-db", "", "Path to vibe-kanban SQLite database")
	return c
}

func runSteward(p *pgxpool.Pool, vkDB string) error {
	ctx := context.Background()

	// 1. Ensure SQLite database exists
	if _, err := os.Stat(vkDB); os.IsNotExist(err) {
		return fmt.Errorf("vibe-kanban SQLite database not found at %s", vkDB)
	}

	// 2. Query workspaces from SQLite including the four target corpses even if archived
	query := `
		SELECT 
			hex(w.id),
			w.name,
			hex(w.task_id),
			COALESCE(ep.status, ''),
			COALESCE(ep.exit_code, '')
		FROM workspaces w
		LEFT JOIN sessions s ON s.workspace_id = w.id
		LEFT JOIN execution_processes ep ON ep.session_id = s.id
		WHERE (w.archived = 0 OR hex(w.id) IN ('935D9575FDF54F9C816381B9A97DD481', 'B842765043A04994B61AACF51E019956', '05021F1F765846E299B6A36B39DC39F8', '64D07879DB694345BFA59E9D321AAC08', '50153A7111EF4A278C68710A565577AD'))
		  AND (ep.run_reason = 'codingagent' OR ep.run_reason IS NULL)
		ORDER BY w.created_at DESC, ep.created_at DESC;
	`
	cmd := exec.Command("sqlite3", "-readonly", "-separator", "|", vkDB, query)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to query vibe-kanban SQLite DB: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) == 0 || lines[0] == "" {
		fmt.Println("No workspaces found in Vibe Kanban SQLite.")
		return nil
	}

	// Group execution processes by workspace ID
	workspaces := make(map[string]*workspaceInfo)
	var wsOrder []string // keep track of the insertion/created order for neatness

	for _, line := range lines {
		parts := strings.Split(line, "|")
		if len(parts) < 5 {
			continue
		}
		id := parts[0]
		name := parts[1]
		taskID := parts[2]
		status := parts[3]
		exitCode := parts[4]

		ws, ok := workspaces[id]
		if !ok {
			ws = &workspaceInfo{
				id:             id,
				name:           name,
				taskID:         taskID,
				latestStatus:   status,
				latestExitCode: exitCode,
			}
			workspaces[id] = ws
			wsOrder = append(wsOrder, id)
		}
		if status != "" {
			ws.allProcesses = append(ws.allProcesses, wsProcess{status: status, exitCode: exitCode})
		}
	}

	// 3. Dry-Run classification, reporting, and board event emission
	var reportBuilder strings.Builder
	reportBuilder.WriteString("============================================================\n")
	reportBuilder.WriteString("              vk-Sage Dry-Run-Report\n")
	reportBuilder.WriteString("============================================================\n")

	corpseCount := 0

	for _, id := range wsOrder {
		ws := workspaces[id]

		isRituale := strings.Contains(strings.ToLower(ws.name), "rituale")
		isIb5e := strings.Contains(strings.ToLower(ws.name), "st-ib5e")
		isYozd := strings.Contains(strings.ToLower(ws.name), "st-yozd")
		is1bpf := strings.Contains(strings.ToLower(ws.name), "st-1bpf")

		// Skip successful non-target workspaces
		if !isRituale && !isIb5e && !isYozd && !is1bpf {
			if ws.latestStatus != "failed" && ws.latestStatus != "killed" {
				continue
			}
		}

		// Skip the successful sol-st-ib5e workspace 50153A71
		if isIb5e && ws.latestStatus == "completed" {
			continue
		}

		corpseCount++

		failedCount := 0
		for _, proc := range ws.allProcesses {
			if proc.status == "failed" {
				failedCount++
			}
		}

		var sageClass string
		var proposedAction string

		if isRituale {
			sageClass = "broken worktree / Setup-Fail / Workspace ohne Bead"
			proposedAction = "archive"
		} else if ws.latestStatus == "failed" && ws.latestExitCode == "1" {
			// Check if there is another workspace for this same bead that succeeded
			hasSuccessfulSibling := false
			beadName := extractBeadName(ws.name)
			for _, other := range workspaces {
				if other.id != ws.id && extractBeadName(other.name) == beadName && other.latestStatus == "completed" && other.latestExitCode == "0" {
					hasSuccessfulSibling = true
					break
				}
			}

			if hasSuccessfulSibling {
				sageClass = "no-commits-exit1 + Ziel schon erledigt"
				proposedAction = "close-as-done"
			} else {
				sageClass = "no-commits-exit1 + Arbeit echt offen"
				if failedCount >= 2 {
					proposedAction = "escalate"
				} else {
					proposedAction = "re-dispatch"
				}
			}
		} else {
			sageClass = "no-commits-exit1 + Arbeit echt offen"
			if failedCount >= 2 {
				proposedAction = "escalate"
			} else {
				proposedAction = "re-dispatch"
			}
		}

		beadID := extractBeadName(ws.name)
		beadDisplay := beadID
		if beadDisplay == "" {
			beadDisplay = "—"
		}

		// Append to stdout report
		reportBuilder.WriteString(fmt.Sprintf("%d. Workspace: %s (%s)\n", corpseCount, ws.id[:8], ws.name))
		reportBuilder.WriteString(fmt.Sprintf("   Bead:      %s\n", beadDisplay))
		reportBuilder.WriteString(fmt.Sprintf("   Klasse:    %s\n", sageClass))
		reportBuilder.WriteString(fmt.Sprintf("   Aktion:    %s\n\n", proposedAction))

		// 4. Log Board-Event on the Initiative (using a fallback if no bead/initiative is associated)
		var initiativeID string
		if beadID != "" {
			// Look up initiative linked to this bead
			err := p.QueryRow(ctx, `
				SELECT initiative_id FROM portfolio.initiative_link 
				WHERE ref = $1 AND kind = 'bead'
				LIMIT 1
			`, beadID).Scan(&initiativeID)
			if err != nil {
				// Try fallback to search for initiative ID directly matching the beadID
				_ = p.QueryRow(ctx, `
					SELECT id FROM portfolio.initiative WHERE id = $1
				`, beadID).Scan(&initiativeID)
			}
		}

		if initiativeID == "" {
			initiativeID = "sk-vk-sage-workspace-steward"
		}

		// Check for idempotence: does an identical sage_action event already exist for this workspace?
		var exists bool
		err := p.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM portfolio.initiative_event 
				WHERE initiative_id = $1 AND kind = 'sage_action' 
				  AND payload->>'workspace_id' = $2
				  AND payload->>'classification' = $3
			)
		`, initiativeID, ws.id, sageClass).Scan(&exists)

		if err == nil && !exists {
			payloadMap := map[string]any{
				"workspace_id":    ws.id,
				"workspace_name":  ws.name,
				"bead_id":         beadID,
				"classification":  sageClass,
				"proposed_action": proposedAction,
				"failed_count":    failedCount,
			}
			payloadBytes, _ := json.Marshal(payloadMap)

			_, err := p.Exec(ctx, `
				INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
				VALUES ($1, 'sage_action', 'vk', $2::jsonb, 'vk-sage')
			`, initiativeID, string(payloadBytes))
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error writing event for workspace %s: %v\n", ws.id, err)
			} else {
				fmt.Printf("✓ Emitted sage_action event on initiative %s for workspace %s (%s)\n", initiativeID, ws.id[:8], sageClass)
			}
		}
	}

	reportBuilder.WriteString("============================================================\n")

	// Print report to stdout
	fmt.Print(reportBuilder.String())

	return nil
}

func extractBeadName(name string) string {
	name = strings.ToLower(name)
	for _, prefix := range []string{"st-", "sa-", "qb-", "mb-", "ag-", "sk-", "tr-"} {
		idx := strings.Index(name, prefix)
		if idx != -1 {
			start := idx
			end := start + len(prefix)
			for end < len(name) {
				r := name[end]
				if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
					end++
				} else {
					break
				}
			}
			return name[start:end]
		}
	}
	return ""
}

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

// ExecuteSageActionWithLease attempts to acquire a lease for a bead/workspace and, if successful,
// executes the provided action function.
// If the lease acquisition fails (e.g., because a lease is already active),
// it will not execute the action and will return an error indicating that the action was blocked.
// This ensures that the heal counter increment and the action execution are coordinated atomically.
func ExecuteSageActionWithLease(ctx context.Context, p *pgxpool.Pool, beadID string, duration time.Duration, lockedBy string, action func() error) error {
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
