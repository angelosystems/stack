package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

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

		// 4. Log Board-Event on the Initiative if a bead is associated
		if beadID != "" {
			var initiativeID string
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

			if initiativeID != "" {
				// Check for idempotence: does an identical sage_action event already exist for this workspace?
				var exists bool
				err = p.QueryRow(ctx, `
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

					_, err = p.Exec(ctx, `
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
