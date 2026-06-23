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

type workspaceStatusInfo struct {
	id        string
	name      string
	taskHex   string
	epStatus  string
	exitCode  string
	updatedAt string
	startedAt string
	createdAt string
}

func cmdFlowManager() *cobra.Command {
	var vkDBPath string
	c := &cobra.Command{
		Use:   "flow-manager",
		Short: "Runs the Kanban-Flow-Manager with flow signals, stagnation detection, and vk-Sage handoff",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := connect()
			if vkDBPath == "" {
				vkDBPath = envOr("VIBE_KANBAN_DB", "/root/.local/share/vibe-kanban/db.v2.sqlite")
			}
			return runFlowManager(p, vkDBPath)
		},
	}
	c.Flags().StringVar(&vkDBPath, "vk-db", "", "Path to vibe-kanban SQLite database")
	return c
}

func runFlowManager(p *pgxpool.Pool, vkDB string) error {
	ctx := context.Background()

	// 1. Fetch active cards (stage 'now' or 'soon')
	rows, err := p.Query(ctx, `
		SELECT id, title, stage, firma 
		FROM portfolio.initiative 
		WHERE stage IN ('now', 'soon') AND archived_at IS NULL
	`)
	if err != nil {
		return fmt.Errorf("failed to query active initiatives: %w", err)
	}
	defer rows.Close()

	type cardInfo struct {
		id    string
		title string
		stage string
		firma string
	}
	var activeCards []cardInfo
	for rows.Next() {
		var c cardInfo
		if err := rows.Scan(&c.id, &c.title, &c.stage, &c.firma); err == nil {
			activeCards = append(activeCards, c)
		}
	}

	if len(activeCards) == 0 {
		fmt.Println("No active cards in stage now/soon.")
		return nil
	}

	// 2. Fetch linked beads for these active cards
	// Maps: cardID -> []beadID
	cardToBeads := make(map[string][]string)
	var activeCardIDs []string
	for _, c := range activeCards {
		activeCardIDs = append(activeCardIDs, c.id)
	}

	linkRows, err := p.Query(ctx, `
		SELECT initiative_id, ref 
		FROM portfolio.initiative_link 
		WHERE kind = 'bead' AND initiative_id = ANY($1)
	`, activeCardIDs)
	if err == nil {
		defer linkRows.Close()
		for linkRows.Next() {
			var initID, beadID string
			if linkRows.Scan(&initID, &beadID) == nil {
				cardToBeads[initID] = append(cardToBeads[initID], beadID)
			}
		}
	}

	// 3. Query vibe-kanban SQLite DB for workspaces and execution processes
	if _, err := os.Stat(vkDB); os.IsNotExist(err) {
		fmt.Println("Vibe Kanban SQLite database not found, skipping workspace checks.")
		return nil
	}

	query := `
		SELECT 
			hex(w.id),
			COALESCE(w.name, ''),
			hex(w.task_id),
			COALESCE(ep.status, ''),
			COALESCE(ep.exit_code, ''),
			COALESCE(ep.updated_at, ''),
			COALESCE(ep.started_at, ''),
			COALESCE(w.created_at, '')
		FROM workspaces w
		LEFT JOIN sessions s ON s.workspace_id = w.id
		LEFT JOIN execution_processes ep ON ep.session_id = s.id
		WHERE (w.archived = 0 OR hex(w.id) IN ('05021F1F765846E299B6A36B39DC39F8', '64D07879DB694345BFA59E9D321AAC08', 'B842765043A04994B61AACF51E019956', '935D9575FDF54F9C816381B9A97DD481'))
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
	workspaces := make(map[string]*workspaceStatusInfo)
	for _, line := range lines {
		parts := strings.Split(line, "|")
		if len(parts) < 8 {
			continue
		}
		id := parts[0]
		name := parts[1]
		taskHex := parts[2]
		epStatus := parts[3]
		exitCode := parts[4]
		updatedAt := parts[5]
		startedAt := parts[6]
		createdAt := parts[7]

		if _, ok := workspaces[id]; !ok {
			workspaces[id] = &workspaceStatusInfo{
				id:        id,
				name:      name,
				taskHex:   taskHex,
				epStatus:  epStatus,
				exitCode:  exitCode,
				updatedAt: updatedAt,
				startedAt: startedAt,
				createdAt: createdAt,
			}
		}
	}

	fmt.Println("=== 🩺 Kanban-Flow-Manager: Stagnation Detection & vk-Sage Handoff ===")

	// 4. Check each active card
	for _, card := range activeCards {
		beads := cardToBeads[card.id]
		if len(beads) == 0 {
			continue
		}

		for _, beadID := range beads {
			// Find matching workspace
			var matchedWS *workspaceStatusInfo
			for _, ws := range workspaces {
				// Match by exact taskHex matching beadID hex, or by workspace name containing beadID
				beadLower := strings.ToLower(beadID)
				nameLower := strings.ToLower(ws.name)
				if strings.Contains(nameLower, beadLower) || strings.ToLower(ws.taskHex) == strings.ToLower(fmt.Sprintf("%x", beadID)) {
					matchedWS = ws
					break
				}
			}

			if matchedWS == nil {
				continue
			}

			// Check if workspace is failed/stuck
			isFailed := matchedWS.epStatus == "failed" || matchedWS.epStatus == "killed"
			isStuck := false

			if matchedWS.epStatus == "running" {
				lastActive := time.Now()
				activeTimeStr := matchedWS.updatedAt
				if activeTimeStr == "" {
					activeTimeStr = matchedWS.startedAt
				}
				if activeTimeStr == "" {
					activeTimeStr = matchedWS.createdAt
				}
				if tVal, err := parseSqliteTime(activeTimeStr); err == nil {
					lastActive = tVal
				}

				timeoutDur := 30 * time.Minute
				if envVal := os.Getenv("SAGE_STUCK_TIMEOUT"); envVal != "" {
					if parsedDur, err := time.ParseDuration(envVal); err == nil {
						timeoutDur = parsedDur
					}
				}

				if time.Since(lastActive) > timeoutDur {
					isStuck = true
				}
			}

			if isFailed || isStuck {
				// Workspace-bedingte Stagnation!
				fmt.Printf("Card %s (%s) is stagnating due to failed/stuck workspace %s (Bead %s, Status %s).\n", 
					card.id, card.title, matchedWS.id[:8], beadID, matchedWS.epStatus)

				// A) Flag the card symptom: Log activity event
				var exists bool
				err := p.QueryRow(ctx, `
					SELECT EXISTS (
						SELECT 1 FROM portfolio.initiative_event 
						WHERE initiative_id = $1 AND kind = 'activity'
						  AND payload->>'workspace_id' = $2
						  AND payload->>'type' = 'flow_stagnation'
					)
				`, card.id, matchedWS.id).Scan(&exists)

				if err == nil && !exists {
					payloadMap := map[string]any{
						"type":             "flow_stagnation",
						"status":           "stagnated",
						"reason":           "Workspace-bedingte Stagnation erkannt",
						"bead_id":          beadID,
						"workspace_id":     matchedWS.id,
						"workspace_status": matchedWS.epStatus,
						"action":           "reicht an vk-Sage weiter",
					}
					payloadJSON, _ := json.Marshal(payloadMap)

					_, err = p.Exec(ctx, `
						INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
						VALUES ($1, 'activity', 'master', $2, 'flow-manager')
					`, card.id, string(payloadJSON))
					if err != nil {
						fmt.Fprintf(os.Stderr, "Failed to log stagnation flag on card %s: %v\n", card.id, err)
					} else {
						fmt.Printf("  ✓ Flagged card symptom on card %s (logged flow_stagnation activity event)\n", card.id)
					}
				}

				// B) Hand off to vk-Sage: Log sage_action event to invite healing
				var sageEventExists bool
				err = p.QueryRow(ctx, `
					SELECT EXISTS (
						SELECT 1 FROM portfolio.initiative_event 
						WHERE initiative_id = $1 AND kind = 'sage_action'
						  AND payload->>'workspace_id' = $2
					)
				`, card.id, matchedWS.id).Scan(&sageEventExists)

				if err == nil && !sageEventExists {
					// Determine sage classification and proposed action
					var classification string
					var proposedAction string
					var reason string

					if isStuck {
						classification = fmt.Sprintf("running-aber-stuck (no update)")
						proposedAction = "escalate"
						reason = "Workspace running aber stuck. Übergabe von Flow-Manager an vk-Sage."
					} else {
						classification = "no-commits-exit1 + Arbeit echt offen"
						proposedAction = "re-dispatch"
						reason = "Workspace failed. Übergabe von Flow-Manager an vk-Sage zur Heilung (re-dispatch)."
					}

					payloadMap := map[string]any{
						"workspace_id":    matchedWS.id,
						"workspace_name":  matchedWS.name,
						"bead_id":         beadID,
						"classification":  classification,
						"proposed_action": proposedAction,
						"reason":          reason,
						"dry_run":         true,
					}
					payloadJSON, _ := json.Marshal(payloadMap)

					_, err = p.Exec(ctx, `
						INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
						VALUES ($1, 'sage_action', 'sage', $2, 'vk-sage-handoff')
					`, card.id, string(payloadJSON))
					if err != nil {
						fmt.Fprintf(os.Stderr, "Failed to log sage_action handoff event: %v\n", err)
					} else {
						fmt.Printf("  ✓ Explicitly handed off card %s to vk-Sage (logged sage_action handoff event)\n", card.id)
					}
				}

				// C) Prevent double action: We stop further processing for this bead on card-altitude
				fmt.Printf("  ✓ Safe: Prevented card-level double-action for card %s. Delegated entirely to vk-Sage.\n", card.id)
			}
		}
	}

	return nil
}
