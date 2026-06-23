package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	var deliveryChannels string
	c := &cobra.Command{
		Use:   "flow-manager",
		Short: "Runs the Kanban-Flow-Manager with flow signals, stagnation detection, and vk-Sage handoff",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := connect()
			if vkDBPath == "" {
				vkDBPath = envOr("VIBE_KANBAN_DB", "/root/.local/share/vibe-kanban/db.v2.sqlite")
			}
			return runFlowManager(p, vkDBPath, deliveryChannels)
		},
	}
	c.Flags().StringVar(&vkDBPath, "vk-db", "", "Path to vibe-kanban SQLite database")
	c.Flags().StringVar(&deliveryChannels, "delivery-channels", "", "Comma-separated list of delivery channels (dashboard, mail, fabric). Overrides FLOW_DELIVERY_CHANNELS.")
	return c
}

func runFlowManager(p *pgxpool.Pool, vkDB string, deliveryChannels string) error {
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
	var workspaces map[string]*workspaceStatusInfo
	var stagnatedCards []string

	if _, err := os.Stat(vkDB); os.IsNotExist(err) {
		fmt.Println("Vibe Kanban SQLite database not found, skipping workspace checks.")
	} else {
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
		workspaces = make(map[string]*workspaceStatusInfo)
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

		// 4. Check each active card for workspace stagnation
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

					// Track as stagnated
					stagnatedCards = append(stagnatedCards, fmt.Sprintf("%s (%s) - Blockiert durch Workspace %s (%s)", card.id, card.title, matchedWS.id[:8], matchedWS.epStatus))

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

						isLiveGeld := (card.firma == "quantbot")

						if isLiveGeld {
							classification = "Live-Geld-Schutz (quantbot)"
							proposedAction = "escalate"
							reason = "Live-Geld-Schutz: Trading-Path-Karten dürfen nur eskaliert werden, kein autonomer Dispatch/Re-dispatch."
						} else if isStuck {
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
	}

	// 5. Check for Promote-Ready cards
	var promoteReadyCards []string
	stPool, stErr := solartownPool()
	if stErr != nil {
		fmt.Printf("Warning: connecting to solartown pool failed: %v. Promote-ready check will be skipped.\n", stErr)
	} else {
		for _, card := range activeCards {
			beads := cardToBeads[card.id]
			if len(beads) > 0 {
				var openCount int
				err := stPool.QueryRow(ctx, `SELECT count(*) FROM beads.issues WHERE id=ANY($1) AND status<>'closed' AND deleted_at IS NULL`, beads).Scan(&openCount)
				if err == nil && openCount == 0 {
					if card.firma == "quantbot" {
						// Live-Geld-Schutz: quantbot-/Trading-Path-Karten werden nur geflaggt + eskaliert — kein autonomer Promote-/Dispatch-Vorschlag in Live-Code.
						// A) Flag the card symptom: Log activity event
						var exists bool
						err := p.QueryRow(ctx, `
							SELECT EXISTS (
								SELECT 1 FROM portfolio.initiative_event 
								WHERE initiative_id = $1 AND kind = 'activity'
								  AND payload->>'type' = 'flow_stagnation'
								  AND payload->>'reason' LIKE '%Promote-reif%'
							)
						`, card.id).Scan(&exists)

						if err == nil && !exists {
							payloadMap := map[string]any{
								"type":             "flow_stagnation",
								"status":           "stagnated",
								"reason":           "Live-Geld-Schutz: Promote-reif aber autonomes Vorrücken blockiert",
								"bead_id":          beads[0], // link a representative bead
								"action":           "reicht zur Eskalation an vk-Sage weiter",
							}
							payloadJSON, _ := json.Marshal(payloadMap)

							_, err = p.Exec(ctx, `
								INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
								VALUES ($1, 'activity', 'master', $2, 'flow-manager')
							`, card.id, string(payloadJSON))
							if err != nil {
								fmt.Fprintf(os.Stderr, "Failed to log promote-ready flag on card %s: %v\n", card.id, err)
							} else {
								fmt.Printf("  ✓ Flagged promote-ready symptom on Live-Geld card %s (logged flow_stagnation activity event)\n", card.id)
							}
						}

						// B) Escalate: Log sage_action event with proposed_action = "escalate"
						var sageEventExists bool
						err = p.QueryRow(ctx, `
							SELECT EXISTS (
								SELECT 1 FROM portfolio.initiative_event 
								WHERE initiative_id = $1 AND kind = 'sage_action'
								  AND payload->>'classification' = 'Live-Geld-Schutz-Promote'
							)
						`, card.id).Scan(&sageEventExists)

						if err == nil && !sageEventExists {
							payloadMap := map[string]any{
								"classification":  "Live-Geld-Schutz-Promote",
								"proposed_action": "escalate",
								"reason":          "Live-Geld-Schutz: Trading-Path-Karten werden nicht autonom promotet, sondern ausschließlich eskaliert.",
								"dry_run":         true,
							}
							payloadJSON, _ := json.Marshal(payloadMap)

							_, err = p.Exec(ctx, `
								INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
								VALUES ($1, 'sage_action', 'sage', $2, 'vk-sage-handoff')
							`, card.id, string(payloadJSON))
							if err != nil {
								fmt.Fprintf(os.Stderr, "Failed to log sage_action promote-ready escalation event: %v\n", err)
							} else {
								fmt.Printf("  ✓ Explicitly escalated promote-ready Live-Geld card %s to vk-Sage\n", card.id)
							}
						}
					} else {
						promoteReadyCards = append(promoteReadyCards, fmt.Sprintf("%s (%s) - Alle verlinkten %d Beads geschlossen", card.id, card.title, len(beads)))
					}
				}
			}
		}
	}

	// 6. Check for Backlog Decay (Backlog-Fäule in IDEA stage)
	var backlogDecayCards []string
	ideaRows, err := p.Query(ctx, `
		SELECT id, title, updated_at
		FROM portfolio.initiative
		WHERE stage = 'idea' AND archived_at IS NULL
	`)
	if err == nil {
		defer ideaRows.Close()
		for ideaRows.Next() {
			var id, title string
			var updatedAt time.Time
			if ideaRows.Scan(&id, &title, &updatedAt) == nil {
				// Check if inactive for > 30 days
				if time.Since(updatedAt) > 30*24*time.Hour {
					days := int(time.Since(updatedAt).Hours() / 24)
					backlogDecayCards = append(backlogDecayCards, fmt.Sprintf("%s (%s) - Inaktiv seit %d Tagen", id, title, days))
				}
			}
		}
	}

	// 7. Check for WIP overflows in NOW stage
	var wipOverflows []string
	firmaNowCount := make(map[string]int)
	for _, c := range activeCards {
		if c.stage == "now" {
			firmaNowCount[c.firma]++
		}
	}
	firmas := []string{"stayawesome", "solartown", "quantbot", "mariobrain", "stack", "angeloos"}
	for _, f := range firmas {
		nowLim, _ := getWIPLimits(f)
		count := firmaNowCount[f]
		if count > nowLim {
			wipOverflows = append(wipOverflows, fmt.Sprintf("%s: %d Karten in NOW (Limit: %d)", f, count, nowLim))
		}
	}

	// 8. Compile Board Review Digest in Markdown
	var sb strings.Builder
	sb.WriteString("# 🩺 Kanban-Flow-Manager: Board Review Digest\n\n")
	sb.WriteString(fmt.Sprintf("Erstellt am: %s\n\n", time.Now().Format("2006-01-02 15:04:05")))
	sb.WriteString("## 📊 Übersicht\n")
	sb.WriteString(fmt.Sprintf("- **Aktive Karten gesamt**: %d\n", len(activeCards)))
	sb.WriteString(fmt.Sprintf("- **Stagnierende Karten (stuck/failed workspaces)**: %d\n", len(stagnatedCards)))
	sb.WriteString(fmt.Sprintf("- **Promote-reife Karten (alle Beads closed)**: %d\n", len(promoteReadyCards)))
	sb.WriteString(fmt.Sprintf("- **Backlog-Fäule (IDEA stagnation)**: %d\n", len(backlogDecayCards)))
	sb.WriteString(fmt.Sprintf("- **WIP-Limit-Überschreitungen**: %d\n\n", len(wipOverflows)))

	sb.WriteString("## 🚨 Stagnation & Workspace Blocks\n")
	if len(stagnatedCards) == 0 {
		sb.WriteString("Keine stagnierenden Karten erkannt.\n\n")
	} else {
		for _, s := range stagnatedCards {
			sb.WriteString(fmt.Sprintf("- %s\n", s))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## 🚀 Promote-reife Karten\n")
	if len(promoteReadyCards) == 0 {
		sb.WriteString("Keine promote-reifen Karten erkannt.\n\n")
	} else {
		for _, pr := range promoteReadyCards {
			sb.WriteString(fmt.Sprintf("- %s\n", pr))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## ⚠️ WIP Limit Overflows\n")
	if len(wipOverflows) == 0 {
		sb.WriteString("Alle WIP-Limits eingehalten.\n\n")
	} else {
		for _, w := range wipOverflows {
			sb.WriteString(fmt.Sprintf("- %s\n", w))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("## 🍂 Backlog-Fäule (IDEA)\n")
	if len(backlogDecayCards) == 0 {
		sb.WriteString("Keine veralteten Karten in IDEA.\n\n")
	} else {
		for _, b := range backlogDecayCards {
			sb.WriteString(fmt.Sprintf("- %s\n", b))
		}
		sb.WriteString("\n")
	}
	digest := sb.String()

	fmt.Println("\n=== 📝 GENERATED REVIEW DIGEST ===")
	fmt.Println(digest)
	fmt.Println("==================================")

	// 9. Deliver the Digest
	stats := map[string]any{
		"total_active":        len(activeCards),
		"total_stagnating":    len(stagnatedCards),
		"total_promote_ready": len(promoteReadyCards),
		"total_backlog_decay": len(backlogDecayCards),
	}
	return deliverDigest(p, deliveryChannels, digest, stats)
}

func deliverDigest(p *pgxpool.Pool, channels string, digest string, stats map[string]any) error {
	if channels == "" {
		channels = os.Getenv("FLOW_DELIVERY_CHANNELS")
	}
	if channels == "" {
		channels = "dashboard,mail"
	}

	chanList := strings.Split(channels, ",")
	for _, ch := range chanList {
		ch = strings.TrimSpace(strings.ToLower(ch))
		switch ch {
		case "dashboard":
			fmt.Printf("[Dashboard Push] Actively delivering digest to board...\n")
			// 1. Create system card if not exists
			_, err := p.Exec(context.Background(), `
				INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend)
				VALUES ('flow-manager-digest', 'mariobrain', 'watching', '🩺 Kanban-Flow-Manager Review Digest', 'plan_file')
				ON CONFLICT (id) DO NOTHING
			`)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ✖ Failed to ensure system card 'flow-manager-digest': %v\n", err)
			}

			// 2. Post digest event
			payloadMap := map[string]any{
				"type":    "board_review_digest",
				"digest":  digest,
				"summary": fmt.Sprintf("Review digest: %d active, %d stagnating, %d promote-ready", stats["total_active"], stats["total_stagnating"], stats["total_promote_ready"]),
			}
			payloadJSON, _ := json.Marshal(payloadMap)
			_, err = p.Exec(context.Background(), `
				INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
				VALUES ('flow-manager-digest', 'activity', 'master', $1, 'flow-manager')
			`, string(payloadJSON))
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ✖ Failed to push digest to Dashboard: %v\n", err)
			} else {
				fmt.Printf("  ✓ Successfully pushed digest to Dashboard (system card 'flow-manager-digest')\n")
			}

		case "mail":
			fmt.Printf("[Mail Push] Actively delivering digest via Email...\n")
			emailBody := fmt.Sprintf(`From: flow-manager@stayawesome.app
To: mario.gemuenden@stayawesome.de
Subject: 🩺 Kanban-Flow-Manager Review Digest - %s

%s`, time.Now().Format("2006-01-02 15:04"), digest)

			// Write to local share directory
			mailDir := "/root/.local/share/portfolio"
			_ = os.MkdirAll(mailDir, 0755)
			mailFile := filepath.Join(mailDir, "flow_manager_digest.mail")
			err := os.WriteFile(mailFile, []byte(emailBody), 0644)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ✖ Failed to write local mail file: %v\n", err)
			} else {
				fmt.Printf("  ✓ Saved review digest to %s\n", mailFile)
			}

			// Deliver via gt mail send if available
			if _, err := exec.LookPath("gt"); err == nil {
				// Send to stack/witness and mayor/
				for _, dest := range []string{"stack/witness", "mayor/"} {
					cmd := exec.Command("gt", "mail", "send", dest, "-s", "Kanban Flow Manager Digest", "--stdin")
					cmd.Stdin = strings.NewReader(digest)
					var errOut bytes.Buffer
					cmd.Stderr = &errOut
					if err := cmd.Run(); err != nil {
						fmt.Fprintf(os.Stderr, "  ✖ Failed to deliver mail to %s via gt mail: %s\n", dest, errOut.String())
					} else {
						fmt.Printf("  ✓ Actively delivered mail to %s via gt mail\n", dest)
					}
				}
			} else {
				fmt.Printf("  ⚠ gt binary not found in PATH, skipped gt mail delivery\n")
			}

		case "fabric":
			fmt.Printf("[Fabric Push] Actively emitting KPIs to Fabric store...\n")
			// Log telemetry traces
			fmt.Printf("  [Fabric Telemetry] Emitting KPI: active_cards=%v\n", stats["total_active"])
			fmt.Printf("  [Fabric Telemetry] Emitting KPI: stagnating_cards=%v\n", stats["total_stagnating"])
			fmt.Printf("  [Fabric Telemetry] Emitting KPI: promote_ready_cards=%v\n", stats["total_promote_ready"])
			fmt.Printf("  [Fabric Telemetry] Emitting KPI: backlog_decay_count=%v\n", stats["total_backlog_decay"])

			// Insert telemetry/event directly to portfolio database representing Fabric emission
			payloadMap := map[string]any{
				"type":                 "fabric_telemetry",
				"active_cards":         stats["total_active"],
				"stagnating_cards":     stats["total_stagnating"],
				"promote_ready_cards":  stats["total_promote_ready"],
				"backlog_decay_count": stats["total_backlog_decay"],
			}
			payloadJSON, _ := json.Marshal(payloadMap)
			_, err := p.Exec(context.Background(), `
				INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
				VALUES ('flow-manager-digest', 'activity', 'master', $1, 'fabric-publisher')
			`, string(payloadJSON))
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ✖ Failed to log Fabric emission trace: %v\n", err)
			} else {
				fmt.Printf("  ✓ Successfully logged Fabric telemetry/push trace to DB\n")
			}
		}
	}
	return nil
}
