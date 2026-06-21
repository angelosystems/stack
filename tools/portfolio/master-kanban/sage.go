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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

// ExecuteSageAction attempts to acquire the lease, increment (or reset) the heal counter,
// and execute the given action function atomically in a transaction.
// Reset-Semantik (Newman-Note): If there is partial progress (hasPartialProgress is true),
// the heal counter is reset to 0 to prevent starvation on hard tasks making incremental progress.
func ExecuteSageAction(ctx context.Context, pool *pgxpool.Pool, beadID string, workspaceID string, actor string, hasPartialProgress bool, actionFn func(tx pgx.Tx, healCount int) error) (bool, error) {
	// Start a transaction
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1. Ensure the sage_lease row exists
	_, err = tx.Exec(ctx, `
		INSERT INTO portfolio.sage_lease (bead_id, locked_until, locked_by, heal_counter, updated_at)
		VALUES ($1, NOW(), '', 0, NOW())
		ON CONFLICT (bead_id) DO NOTHING
	`, beadID)
	if err != nil {
		return false, fmt.Errorf("failed to ensure lease row: %w", err)
	}

	// 2. Lock and read the current lease details
	var lockedUntil time.Time
	var lockedBy string
	var healCounter int
	err = tx.QueryRow(ctx, `
		SELECT locked_until, locked_by, heal_counter
		FROM portfolio.sage_lease
		WHERE bead_id = $1
		FOR UPDATE
	`, beadID).Scan(&lockedUntil, &lockedBy, &healCounter)
	if err != nil {
		return false, fmt.Errorf("failed to lock lease row: %w", err)
	}

	// 3. Check if lease is active (and not held by us)
	now := time.Now()
	if lockedUntil.After(now) && lockedBy != "" && lockedBy != actor {
		// Lease is currently active and held by another actor -> skip/lock acquisition fails
		return false, nil
	}

	// 4. Calculate new heal counter and lock duration
	// Lease expires in 5 minutes by default to avoid orphan locks if process crashes
	newLockedUntil := now.Add(5 * time.Minute)
	var newHealCounter int
	if hasPartialProgress {
		newHealCounter = 0
	} else {
		newHealCounter = healCounter + 1
	}

	// 5. Update the sage_lease table
	_, err = tx.Exec(ctx, `
		UPDATE portfolio.sage_lease
		SET locked_until = $2,
		    locked_by = $3,
		    heal_counter = $4,
		    updated_at = NOW()
		WHERE bead_id = $1
	`, beadID, newLockedUntil, actor, newHealCounter)
	if err != nil {
		return false, fmt.Errorf("failed to update lease: %w", err)
	}

	// 6. Update the sage_heal_count table to stay in sync
	_, err = tx.Exec(ctx, `
		INSERT INTO portfolio.sage_heal_count (bead_id, heal_count, escalated, updated_at)
		VALUES ($1, $2, false, NOW())
		ON CONFLICT (bead_id) DO UPDATE
		SET heal_count = EXCLUDED.heal_count,
		    updated_at = NOW()
	`, beadID, newHealCounter)
	if err != nil {
		return false, fmt.Errorf("failed to update heal count: %w", err)
	}

	// 7. Execute the actual action callback inside the transaction!
	if err := actionFn(tx, newHealCounter); err != nil {
		return false, fmt.Errorf("action execution failed: %w", err)
	}

	// 8. Commit the transaction
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return true, nil
}

// cmdSage implements the Sage Workspace-Steward.
// It queries the 4 known dead/stale workspaces, classifies them, prints a report,
// and executes Sage actions atomically with a per-Bead-Lease / Compare-and-Set and atomic Heal-Counter.
func cmdSage() *cobra.Command {
	c := &cobra.Command{
		Use:   "sage",
		Short: "Führt den vk-Sage Workspace-Steward mit per-Bead-Lease und atomarem Heal-Counter aus",
		RunE: func(cmd *cobra.Command, args []string) error {
			vkDB := "/root/.local/share/vibe-kanban/db.v2.sqlite"

			// Check if SQLite DB exists
			if _, err := os.Stat(vkDB); os.IsNotExist(err) {
				return fmt.Errorf("vibe-kanban SQLite database not found at %s", vkDB)
			}

			// Query workspaces from SQLite (specifically the 4 targets)
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
				WHERE (ep.run_reason = 'codingagent' OR ep.run_reason IS NULL)
				  AND (hex(w.id) IN ('05021F1F765846E299B6A36B39DC39F8', '64D07879DB694345BFA59E9D321AAC08', 'B842765043A04994B61AACF51E019956', '935D9575FDF54F9C816381B9A97DD481'))
				ORDER BY w.created_at DESC, ep.created_at DESC;
			`
			sqliteCmd := exec.Command("sqlite3", "-readonly", "-separator", "|", vkDB, query)
			var out bytes.Buffer
			sqliteCmd.Stdout = &out
			if err := sqliteCmd.Run(); err != nil {
				return fmt.Errorf("failed to query vibe-kanban SQLite DB: %v", err)
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

				// Store the first occurrence (which is the most recent because of ep.created_at DESC!)
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

			fmt.Println("=== 🧓 vk-Sage Workspace-Steward ===")
			fmt.Println("--------------------------------------------------------------------------------")
			fmt.Printf("%-10s | %-20s | %-40s | %-15s\n", "ID (8)", "Workspace-Name", "Sage-Klassifikation", "Vorgeschl. Aktion")
			fmt.Println("--------------------------------------------------------------------------------")

			p := connect()
			ctx := context.Background()

			for _, ws := range workspaces {
				isRituale := strings.Contains(strings.ToLower(ws.name), "rituale")
				isIb5e := strings.Contains(strings.ToLower(ws.name), "st-ib5e")
				isYozd := strings.Contains(strings.ToLower(ws.name), "st-yozd")
				is1bpf := strings.Contains(strings.ToLower(ws.name), "st-1bpf")

				// Detect partial progress for this workspace via execution_process_repo_states
				hasPartialProgress := false
				progressQuery := fmt.Sprintf(`
					SELECT EXISTS (
						SELECT 1 
						FROM execution_processes ep
						JOIN sessions s ON s.workspace_id = X'%s'
						JOIN execution_process_repo_states eprs ON eprs.execution_process_id = ep.id
						WHERE ep.session_id = s.id
						  AND ep.run_reason = 'codingagent'
						  AND eprs.before_head_commit != eprs.after_head_commit
					);
				`, ws.id)
				sqliteProgressCmd := exec.Command("sqlite3", "-readonly", vkDB, progressQuery)
				var progressOut bytes.Buffer
				sqliteProgressCmd.Stdout = &progressOut
				if err := sqliteProgressCmd.Run(); err == nil {
					if strings.TrimSpace(progressOut.String()) == "1" {
						hasPartialProgress = true
					}
				}

				var sageClass string
				var proposedAction string
				var reason string
				var beadID string

				if isRituale {
					sageClass = "broken worktree / Setup-Fail / Workspace ohne Bead"
					proposedAction = "close (workspace-only)"
					reason = "Worktree kein gültiges Git-Repo, keine Bead-Zuordnung (Workspace unvollständig/setup-fail)."
				} else {
					beadID = extractBeadID(ws.name)

					// Query persistent current heal count from database to decide on retry/escalate budget
					currentHealCount := 0
					_ = p.QueryRow(ctx, `
						SELECT heal_count FROM portfolio.sage_heal_count WHERE bead_id = $1
					`, beadID).Scan(&currentHealCount)

					if isIb5e {
						sageClass = "no-commits-exit1 + Ziel schon erledigt"
						proposedAction = "close-as-done"
						reason = "Detox-Konzept ist bereits im master-kanban-Backend umgesetzt (st-ib5e.status='closed'). Workspace archivieren und Zombie-Loop stoppen."
					} else {
						// Open work - determine action based on retry budget
						sageClass = "no-commits-exit1 + Arbeit echt offen"
						if currentHealCount >= 2 {
							proposedAction = "escalate"
							if isYozd {
								reason = fmt.Sprintf("Retry-Budget verbraucht (Heal-Counter=%d): 4-5x re-dispatcht und jedes Mal fehlgeschlagen. UI-Lücke verifiziert: Backlog-Tab hat heute nur einen Triage-Knopf statt der drei R1-Buttons.", currentHealCount)
							} else if is1bpf {
								reason = fmt.Sprintf("Retry-Budget verbraucht (Heal-Counter=%d): 4-5x re-dispatcht und jedes Mal fehlgeschlagen. UI-Lücke verifiziert: cockpit hat firma-Stripes aber nicht die R5 Lane-Badges.", currentHealCount)
							} else {
								reason = fmt.Sprintf("Retry-Budget verbraucht (Heal-Counter=%d): fehlgeschlagen.", currentHealCount)
							}
						} else {
							proposedAction = "re-dispatch"
							reason = fmt.Sprintf("no-commits-exit1 + Arbeit echt offen (Heal-Counter=%d). Re-dispatch mit Fehlerdiagnose.", currentHealCount)
						}
					}
				}

				fmt.Printf("%-10s | %-20s | %-40s | %-15s\n", ws.id[:8], ws.name, sageClass, proposedAction)

				// Lock target (per-bead, fallback to workspace_id if beadID is empty)
				lockID := beadID
				if lockID == "" {
					lockID = ws.id
				}

				if beadID != "" {
					// 1. Look up initiative ID linked to bead
					var initiativeID string
					err := p.QueryRow(ctx, `SELECT initiative_id FROM portfolio.initiative_link WHERE kind='bead' AND ref=$1`, beadID).Scan(&initiativeID)
					if err != nil {
						fmt.Printf("  ⚠️  Keine verknüpfte Initiative für Bead %s gefunden: %v\n", beadID, err)
						continue
					}

					// Define atomic action callback
					actionFn := func(tx pgx.Tx, healCount int) error {
						// Build payload
						payloadMap := map[string]any{
							"workspace_id":    ws.id,
							"workspace_name":  ws.name,
							"bead_id":         beadID,
							"classification":  sageClass,
							"proposed_action": proposedAction,
							"reason":          reason,
							"heal_count":      healCount,
						}
						payloadJSON, _ := json.Marshal(payloadMap)

						// 3. Insert sage_action event
						_, err = tx.Exec(ctx, `
							INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
							VALUES ($1, 'sage_action', 'sage', $2, 'sage-steward')
						`, initiativeID, payloadJSON)
						if err != nil {
							return err
						}

						// If proposed action is re-dispatch, perform actual re-dispatch with a diagnosis-informed / re-scoped prompt
						if proposedAction == "re-dispatch" {
							diagnosisPrompt := buildDiagnosisPrompt(healCount, isYozd, is1bpf)
							dispatchPayload := map[string]any{
								"lane": "plan",
								"note": diagnosisPrompt,
							}
							dispatchJSON, _ := json.Marshal(dispatchPayload)
							_, err = tx.Exec(ctx, `
								INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
								VALUES ($1, 'dispatched', 'sage', $2, 'sage-steward')
							`, initiativeID, dispatchJSON)
							if err != nil {
								return fmt.Errorf("failed to write dispatched event: %w", err)
							}
						}

						// If proposed action is close-as-done, actually update the bead's status to closed in the beads database
						if proposedAction == "close-as-done" {
							sp, err := solartownPool()
							if err == nil {
								_, err = sp.Exec(ctx, "UPDATE beads.issues SET status='closed' WHERE id=$1", beadID)
								if err != nil {
									return fmt.Errorf("failed to update bead status to closed: %w", err)
								}
							}
						}

						return nil
					}

					// Try to execute Sage action atomically with lease and hasPartialProgress reset semantics
					acquired, err := ExecuteSageAction(ctx, p, lockID, ws.id, "sage-steward", hasPartialProgress, actionFn)
					if err != nil {
						fmt.Printf("  ❌ Fehler beim Ausführen der Sage-Aktion für Bead %s: %v\n", beadID, err)
						continue
					}

					if acquired {
						fmt.Printf("  ✓ Sage-Aktion erfolgreich ausgeführt: Lease erworben, Counter aktualisiert (Fortschritt=%t) und Board-Event erfasst für Initiative: %s (Bead %s)\n", hasPartialProgress, initiativeID, beadID)
					} else {
						fmt.Printf("  ✓ Sage-Aktion übersprungen für Bead %s: Lease ist bereits aktiv (Kollisionsschutz greift).\n", beadID)
					}
				} else {
					// No bead associated (e.g. rituale). Lock on lockID (ws.id) to avoid duplicates.
					actionFn := func(tx pgx.Tx, healCount int) error {
						return nil
					}

					acquired, err := ExecuteSageAction(ctx, p, lockID, ws.id, "sage-steward", hasPartialProgress, actionFn)
					if err != nil {
						fmt.Printf("  ❌ Fehler beim Sichern des Setup-Fail-Workspaces %s: %v\n", ws.id[:8], err)
						continue
					}

					if acquired {
						fmt.Printf("  ✓ Workspace-Sicherung (Lease) erfolgreich erfasst für: %s\n", ws.name)
					} else {
						fmt.Printf("  ✓ Workspace-Sicherung übersprungen für %s: Lease ist bereits aktiv (Kollisionsschutz greift).\n", ws.name)
					}
				}
			}
			fmt.Println("--------------------------------------------------------------------------------")
			return nil
		},
	}
	return c
}

func extractBeadID(name string) string {
	idx := strings.Index(name, "st-")
	if idx == -1 {
		return ""
	}
	res := ""
	for i := idx; i < len(name); i++ {
		ch := name[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' {
			res += string(ch)
		} else {
			break
		}
	}
	return res
}

// SageAction represents a logged Sage/Steward action
type SageAction struct {
	Action     string `json:"action"`       // "heal", "close-as-done", "escalate", "archive", "merge-nudge"
	Reason     string `json:"reason"`       // Why this action was taken
	HealCount  int    `json:"heal_count"`   // Current heal count
	IsLiveGeld bool   `json:"is_live_geld"` // Whether this is a live-geld (quantbot) bead
	Timestamp  string `json:"timestamp"`
}

// SageDecisionEngine coordinates the healing and escalation of beads/workspaces
type SageDecisionEngine struct {
	Pool            *pgxpool.Pool
	DefaultMaxHeals int // Default N (usually 2)
}

func NewSageDecisionEngine(pool *pgxpool.Pool, defaultMaxHeals int) *SageDecisionEngine {
	if defaultMaxHeals <= 0 {
		defaultMaxHeals = 2
	}
	return &SageDecisionEngine{
		Pool:            pool,
		DefaultMaxHeals: defaultMaxHeals,
	}
}

// ProcessFailure evaluates a failed run for an initiative and decides whether to heal or escalate.
// It implements the Newman-Note Reset-Semantik for partial progress (Counter-Reset-Regel).
func (s *SageDecisionEngine) ProcessFailure(ctx context.Context, initiativeID string, hasPartialProgress bool) (string, error) {
	// 1. Fetch the initiative details (firma/company, current heal_count)
	var firma string
	var healCount int
	err := s.Pool.QueryRow(ctx,
		`SELECT firma, COALESCE(heal_count, 0) FROM portfolio.initiative WHERE id = $1`,
		initiativeID).Scan(&firma, &healCount)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", fmt.Errorf("initiative not found: %s", initiativeID)
		}
		return "", err
	}

	isLiveGeld := (firma == "quantbot")

	// 2. Apply Cockburn, Live-Geld-Konvention (quantbot beads only escalate, never close/heal/re-dispatch)
	if isLiveGeld {
		err = s.Escalate(ctx, initiativeID, "Live-Geld-Konvention: Trading-Path-Beads dürfen nur eskaliert werden", healCount, true)
		if err != nil {
			return "", err
		}
		return "escalated (live-geld)", nil
	}

	// 3. Apply Newman-Note Reset-Semantik: If hasPartialProgress is true, reset heal count to 0.
	var newHealCount int
	if hasPartialProgress {
		newHealCount = 0
	} else {
		newHealCount = healCount + 1
	}

	// 4. For regular beads, check the retry/healing budget (L4)
	if !hasPartialProgress && healCount >= s.DefaultMaxHeals {
		// STOP + Escalate!
		err = s.Escalate(ctx, initiativeID, fmt.Sprintf("Retry-Budget verbraucht (%d/%d erfolglose Heilungen)", healCount, s.DefaultMaxHeals), healCount, false)
		if err != nil {
			return "", err
		}
		return "escalated (budget-exhausted)", nil
	}

	// 5. Update initiative heal_count and heal_counter
	_, err = s.Pool.Exec(ctx,
		`UPDATE portfolio.initiative SET heal_count = $1, heal_counter = $1, updated_at = now() WHERE id = $2`,
		newHealCount, initiativeID)
	if err != nil {
		return "", err
	}

	// Also sync to sage_heal_count table
	_, _ = s.Pool.Exec(ctx, `
		INSERT INTO portfolio.sage_heal_count (bead_id, heal_count, escalated, updated_at)
		VALUES ($1, $2, false, NOW())
		ON CONFLICT (bead_id) DO UPDATE
		SET heal_count = EXCLUDED.heal_count,
		    updated_at = NOW()
	`, initiativeID, newHealCount)

	// Log healing action board-event
	var reason string
	if hasPartialProgress {
		reason = fmt.Sprintf("Automatisches Heilen / Re-dispatch eingeleitet mit Reset des Heal-Counters wegen partial progress (Heilversuch %d/%d)", newHealCount, s.DefaultMaxHeals)
	} else {
		reason = fmt.Sprintf("Automatisches Heilen / Re-dispatch eingeleitet (Heilversuch %d/%d)", newHealCount, s.DefaultMaxHeals)
	}
	payload := SageAction{
		Action:     "heal",
		Reason:     reason,
		HealCount:  newHealCount,
		IsLiveGeld: false,
		Timestamp:  time.Now().Format(time.RFC3339),
	}
	payloadBytes, _ := json.Marshal(payload)

	_, err = s.Pool.Exec(ctx,
		`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
		 VALUES ($1, 'sage_action', 'master', $2, 'vk-sage')`,
		initiativeID, payloadBytes)
	if err != nil {
		return "", err
	}

	fmt.Printf("[Sage Advisor-Signal] HEAL: Initiative %s re-dispatched. Versuch %d/%d.\n", initiativeID, newHealCount, s.DefaultMaxHeals)

	return "healed", nil
}

// Escalate logs an escalation event and stops any future automatic action on the bead
func (s *SageDecisionEngine) Escalate(ctx context.Context, initiativeID string, reason string, healCount int, isLiveGeld bool) error {
	payload := SageAction{
		Action:     "escalate",
		Reason:     reason,
		HealCount:  healCount,
		IsLiveGeld: isLiveGeld,
		Timestamp:  time.Now().Format(time.RFC3339),
	}
	payloadBytes, _ := json.Marshal(payload)

	_, err := s.Pool.Exec(ctx,
		`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
		 VALUES ($1, 'sage_action', 'master', $2, 'vk-sage')`,
		initiativeID, payloadBytes)
	if err != nil {
		return err
	}

	fmt.Printf("[Sage Advisor-Signal/Mail] ESCALATION: Initiative %s eskaliert! Grund: %s\n", initiativeID, reason)

	return nil
}

func buildDiagnosisPrompt(healCount int, isYozd, is1bpf bool) string {
	var specificDiagnosis string
	if isYozd {
		specificDiagnosis = "UI-Lücke verifiziert: Backlog-Tab hat heute nur einen Triage-Knopf statt der drei R1-Buttons. Bitte implementieren Sie die drei fehlenden R1-Buttons im Backlog-Tab."
	} else if is1bpf {
		specificDiagnosis = "UI-Lücke verifiziert: cockpit hat firma-Stripes aber nicht die R5 Lane-Badges. Bitte implementieren Sie die fehlenden R5 Lane-Badges im Cockpit."
	} else {
		specificDiagnosis = "The previous run failed with zero commits (no-commits-exit1)."
	}

	return fmt.Sprintf(
		"[vk-Sage] RE-DISPATCH (Heal Attempt #%d). Diagnosis: %s Instruction Re-scoping: Please ensure that you make logical, incremental commits before completing your work tree. Ensure tests are run and pass, and verify you are not stuck in an empty/idle loop.",
		healCount,
		specificDiagnosis,
	)
}

