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

// FlowDiagnosis represents the structured output of the GLM diagnosis.
type FlowDiagnosis struct {
	Category       string `json:"category"`        // "wartet-auf-Mensch", "Workspace-gescheitert", "fertig-nicht-promotet", "verlassen"
	Confidence     string `json:"confidence"`      // "High", "Low"
	Reasoning      string `json:"reasoning"`       // 2-3 sentence diagnostic reasoning
	ProposedAction string `json:"proposed_action"` // empty if Confidence is Low
}

type FlowInitiative struct {
	ID          string
	Firma       string
	Stage       string
	Title       string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type LinkedBead struct {
	Ref    string
	Status string
}

type LinkedWorkspace struct {
	ID     string
	Status string
}

type FlowEvent struct {
	Kind          string
	SourceBackend string
	FromStage     string
	ToStage       string
	Payload       string
	Actor         string
	At            time.Time
}

func cmdFlowManager() *cobra.Command {
	var dryRun bool
	c := &cobra.Command{
		Use:   "flow-manager",
		Short: "Runs the Kanban Flow-Manager to diagnose stagnant/stale cards and propose actions",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := connect()
			return runFlowManager(p, dryRun)
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "Analyze and diagnose but do not write events to the database")
	return c
}

func runFlowManager(p *pgxpool.Pool, dryRun bool) error {
	ctx := context.Background()
	fmt.Println("=== 🩺 Kanban Flow-Manager ===")

	// 1. Fetch unarchived initiatives
	rows, err := p.Query(ctx, `
		SELECT id, firma, stage, title, COALESCE(description, ''), created_at, updated_at 
		FROM portfolio.initiative 
		WHERE archived_at IS NULL
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return fmt.Errorf("failed to query initiatives: %w", err)
	}
	defer rows.Close()

	var initiatives []FlowInitiative
	for rows.Next() {
		var init FlowInitiative
		if err := rows.Scan(&init.ID, &init.Firma, &init.Stage, &init.Title, &init.Description, &init.CreatedAt, &init.UpdatedAt); err != nil {
			return err
		}
		initiatives = append(initiatives, init)
	}

	// 2. Fetch WIP counts per company in 'now'
	wipCounts := make(map[string]int)
	for _, init := range initiatives {
		if init.Stage == "now" {
			wipCounts[init.Firma]++
		}
	}

	// 2b. Echtes Stagnationsmaß: last_activity aus den relevanten Events, EINE
	// Query (GROUP BY) statt N+1. init.updated_at ist unbrauchbar, weil
	// trg_initiative_stage_change es bei JEDEM Update setzt — auch beim
	// flow_action-Rauschen. flow_action/activity zählen bewusst NICHT als
	// Aktivität; fehlt ein Event, ist created_at der Fallback.
	lastActivity := make(map[string]time.Time)
	laRows, err := p.Query(ctx, `
		SELECT initiative_id, MAX(at)
		  FROM portfolio.initiative_event
		 WHERE kind IN ('moved','commented','completed','linked','created','edited','dispatched')
		 GROUP BY initiative_id
	`)
	if err == nil {
		for laRows.Next() {
			var id string
			var at time.Time
			if laRows.Scan(&id, &at) == nil {
				lastActivity[id] = at
			}
		}
		laRows.Close()
	}

	var diagnosedCards []DiagnosedCard

	// 3. For each initiative, gather context and diagnose if flagged
	for _, init := range initiatives {
		// A. Get verlinked beads and their statuses
		sp, err := solartownPool()
		var beads []LinkedBead
		if err == nil {
			linkRows, err := p.Query(ctx, `
				SELECT ref FROM portfolio.initiative_link 
				WHERE initiative_id = $1 AND kind = 'bead'
			`, init.ID)
			if err == nil {
				var refs []string
				for linkRows.Next() {
					var ref string
					if err := linkRows.Scan(&ref); err == nil {
						refs = append(refs, ref)
					}
				}
				linkRows.Close()

				for _, ref := range refs {
					var status string
					err := sp.QueryRow(ctx, "SELECT status FROM beads.issues WHERE id = $1 AND deleted_at IS NULL", ref).Scan(&status)
					if err == nil {
						beads = append(beads, LinkedBead{Ref: ref, Status: status})
					} else {
						// Fallback if not found or deleted
						beads = append(beads, LinkedBead{Ref: ref, Status: "unknown"})
					}
				}
			}
		}

		// B. Get active workspaces
		var workspaces []LinkedWorkspace
		vkDB := envOr("VIBE_KANBAN_DB", "/root/.local/share/vibe-kanban/db.v2.sqlite")
		if _, err := os.Stat(vkDB); err == nil {
			wsQuery := fmt.Sprintf(`
				SELECT hex(w.id), COALESCE(ep.status, '')
				FROM workspaces w
				JOIN sessions s ON s.workspace_id = w.id
				LEFT JOIN execution_processes ep ON ep.session_id = s.id
				WHERE w.name LIKE '%%%s%%' AND w.archived = 0
				ORDER BY ep.created_at DESC;
			`, init.ID)
			sqliteCmd := exec.Command("sqlite3", "-readonly", vkDB, wsQuery)
			var wsOut bytes.Buffer
			sqliteCmd.Stdout = &wsOut
			if err := sqliteCmd.Run(); err == nil {
				wsLines := strings.Split(strings.TrimSpace(wsOut.String()), "\n")
				for _, line := range wsLines {
					parts := strings.Split(line, "|")
					if len(parts) >= 2 {
						workspaces = append(workspaces, LinkedWorkspace{ID: parts[0], Status: parts[1]})
					}
				}
			}
		}

		// Check if lower layers (Reactor, vk-Sage) are engaged (MUST-FIX Nygard/Newman)
		engaged, reason, err := isLowerLayerEngaged(ctx, p, init.ID, beads, workspaces)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ❌ Error checking lower layer engagement for card %s: %v\n", init.ID, err)
			continue
		}
		if engaged {
			fmt.Printf("Skipping card %s (%s, Stage: %s, Firma: %s) because lower layers are engaged: %s\n\n", init.ID, init.Title, init.Stage, init.Firma, reason)
			continue
		}

		// watching→done: evidenzbasierter Vollzug (WP1). Delivery-Beleg +
		// (je nach Ledger-Lage) Live-Deploy. Läuft für jede nicht-engagierte
		// watching-Karte, unabhängig vom Flag-Schritt. quantbot: propose-only
		// (Regel 3, Live-Geld-Nähe) — Vorschlag als Event, kein Auto-Move
		// (WP1-Nachtrag).
		if init.Stage == "watching" {
			ev, derr := gatherDoneEvidence(ctx, p, init.ID)
			if derr != nil {
				fmt.Fprintf(os.Stderr, "  ❌ done-Evidenz für %s: %v\n", init.ID, derr)
			} else if move, why := watchingDoneDecision(ev); move {
				proposeOnly := init.Firma == "quantbot"
				evidence := map[string]any{
					"reason":             why,
					"has_delivery":       ev.HasDelivery,
					"deploy_status":      ev.DeployStatus,
					"software_in_ledger": ev.SoftwareInLedger,
				}
				if dryRun {
					verb := "vollziehen"
					if proposeOnly {
						verb = "vorschlagen (propose-only quantbot)"
					}
					fmt.Printf("[dry-run] würde %s (%s) watching→done %s (%s)\n", init.ID, init.Title, verb, why)
				} else if moved, reason, aerr := applyStageProposal(ctx, p, init.ID, "done", evidence, "flow-manager", proposeOnly); aerr != nil {
					fmt.Fprintf(os.Stderr, "  ❌ watching→done-Vollzug für %s: %v\n", init.ID, aerr)
				} else if moved {
					fmt.Printf("  ✓ %s (%s) watching→done vollzogen (%s)\n", init.ID, init.Title, why)
					continue
				} else if reason == "propose-only" {
					fmt.Printf("  ⋯ %s (%s) watching→done VORGESCHLAGEN (propose-only quantbot, %s)\n", init.ID, init.Title, why)
				}
			}
		}

		// C. Get recent events
		eventRows, err := p.Query(ctx, `
			SELECT kind, source_backend, COALESCE(from_stage, ''), COALESCE(to_stage, ''), COALESCE(payload::text, '{}'), COALESCE(actor, ''), at 
			FROM portfolio.initiative_event 
			WHERE initiative_id = $1 
			ORDER BY at DESC 
			LIMIT 5
		`, init.ID)
		var events []FlowEvent
		if err == nil {
			for eventRows.Next() {
				var ev FlowEvent
				if err := eventRows.Scan(&ev.Kind, &ev.SourceBackend, &ev.FromStage, &ev.ToStage, &ev.Payload, &ev.Actor, &ev.At); err == nil {
					events = append(events, ev)
				}
			}
			eventRows.Close()
		}

		// D. Evaluate flagging rules (L2)
		var flaggedReasons []string

		// WIP Limit Check — Limit aus der vereinheitlichten Quelle (getWIPLimits,
		// inkl. code-factory=4 nach dem Rename; früher: Firma unbekannt ⇒ 0).
		if init.Stage == "now" {
			limit, _ := getWIPLimits(init.Firma)
			if limit > 0 && wipCounts[init.Firma] > limit {
				flaggedReasons = append(flaggedReasons, fmt.Sprintf("WIP-Überlauf: %d karten in NOW (limit %d)", wipCounts[init.Firma], limit))
			}
		}

		// Promote-reif Check
		hasBeads := len(beads) > 0
		allBeadsClosed := true
		for _, b := range beads {
			if b.Status != "closed" {
				allBeadsClosed = false
				break
			}
		}
		if init.Stage != "done" && hasBeads && allBeadsClosed {
			flaggedReasons = append(flaggedReasons, "Promote-reif: alle verlinkten Beads sind closed")
		}

		// Stagnation Check
		hasActiveWorkspace := false
		for _, ws := range workspaces {
			if ws.Status == "running" || ws.Status == "waiting" {
				hasActiveWorkspace = true
				break
			}
		}
		hasActiveBeads := false
		for _, b := range beads {
			if b.Status == "open" || b.Status == "in_progress" {
				hasActiveBeads = true
				break
			}
		}

		// Inaktivität aus echter last_activity (Fallback created_at), NICHT aus
		// updated_at (siehe 2b).
		activityAt, ok := lastActivity[init.ID]
		if !ok {
			activityAt = init.CreatedAt
		}
		timeInactivity := time.Since(activityAt)
		stagnationThreshold := GetStageThreshold(init.Firma, init.Stage)
		if (init.Stage == "now" || init.Stage == "soon") && stagnationThreshold > 0 && timeInactivity > stagnationThreshold && !hasActiveWorkspace && !hasActiveBeads {
			flaggedReasons = append(flaggedReasons, fmt.Sprintf("Stagnation: %v tage stille, keine aktive arbeit (workspace/beads)", int(timeInactivity.Hours()/24)))
		}

		// Backlog-Fäule Check
		staleThreshold := GetStageThreshold(init.Firma, "idea")
		if init.Stage == "idea" && staleThreshold > 0 && time.Since(init.CreatedAt) > staleThreshold && len(beads) == 0 && len(events) <= 1 {
			flaggedReasons = append(flaggedReasons, fmt.Sprintf("Backlog-Fäule: über %v tage unbewegt in IDEA", int(staleThreshold.Hours()/24)))
		}

		// If flagged, run GLM diagnosis!
		if len(flaggedReasons) > 0 {
			// Eskalations-Leiter: Reactor -> vk-Sage -> Manager
			// Check if any of the lower layers are engaged for this initiative.

			// 1. Check for active/waiting Workspace
			hasActiveWorkspace := false
			for _, ws := range workspaces {
				if ws.Status == "running" || ws.Status == "waiting" {
					hasActiveWorkspace = true
					break
				}
			}

			// 2. Check for open Reactor attempt
			openReactor := hasOpenReactorAttempt(events)

			// 3. Check if in vk-Sage's queue (active lease)
			hasSageLease := false
			for _, b := range beads {
				var lockedUntil time.Time
				err := p.QueryRow(ctx, `
					SELECT locked_until FROM portfolio.sage_lease WHERE bead_id = $1
				`, b.Ref).Scan(&lockedUntil)
				if err == nil && lockedUntil.After(time.Now()) {
					hasSageLease = true
					break
				}
			}

			if hasActiveWorkspace || openReactor || hasSageLease {
				var engaged []string
				if hasActiveWorkspace {
					engaged = append(engaged, "active Workspace")
				}
				if openReactor {
					engaged = append(engaged, "open Reactor attempt")
				}
				if hasSageLease {
					engaged = append(engaged, "vk-Sage's queue/lease")
				}
				fmt.Printf("Skipping flagged card %s (%s) because lower layers are engaged: %s\n\n",
					init.ID, init.Title, strings.Join(engaged, ", "))
				continue
			}

			// now→watching: Promote-reif + stage=now + Beads>0 ⇒ evidenzbasierter
			// Vollzug (WP1). Nur stage now (soon/idea bleiben Urteilsfall).
			// quantbot: propose-only (Regel 3) — Vorschlag als Event, kein
			// Auto-Move (WP1-Nachtrag). GLM/Digest laufen danach unverändert
			// weiter.
			if init.Stage == "now" && hasBeads && allBeadsClosed {
				proposeOnly := init.Firma == "quantbot"
				closedCount := 0
				for _, b := range beads {
					if b.Status == "closed" {
						closedCount++
					}
				}
				evidence := map[string]any{
					"reason":       "promote-reif",
					"beads_total":  len(beads),
					"beads_closed": closedCount,
				}
				if dryRun {
					verb := "vollziehen"
					if proposeOnly {
						verb = "vorschlagen (propose-only quantbot)"
					}
					fmt.Printf("[dry-run] würde %s (%s) now→watching %s (%d/%d beads closed)\n", init.ID, init.Title, verb, closedCount, len(beads))
				} else if moved, reason, aerr := applyStageProposal(ctx, p, init.ID, "watching", evidence, "flow-manager", proposeOnly); aerr != nil {
					fmt.Fprintf(os.Stderr, "  ❌ now→watching-Vollzug für %s: %v\n", init.ID, aerr)
				} else if moved {
					fmt.Printf("  ✓ %s (%s) now→watching vollzogen (%d/%d beads closed)\n", init.ID, init.Title, closedCount, len(beads))
				} else if reason == "propose-only" {
					fmt.Printf("  ⋯ %s (%s) now→watching VORGESCHLAGEN (propose-only quantbot, %d/%d beads closed)\n", init.ID, init.Title, closedCount, len(beads))
				}
			}

			fmt.Printf("Diagnosing flagged card %s (%s, Stage: %s, Firma: %s)\n", init.ID, init.Title, init.Stage, init.Firma)
			fmt.Printf("  -> Flagged reasons: %s\n", strings.Join(flaggedReasons, " | "))

			diagnosis, err := diagnoseFlaggedCard(init, beads, workspaces, events, flaggedReasons)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ❌ Error diagnosing card %s: %v\n", init.ID, err)
				continue
			}

			if init.Firma == "quantbot" {
				diagnosis.ProposedAction = "escalate"
			} else if diagnosis.Category == "Workspace-gescheitert" {
				diagnosis.ProposedAction = "handover"
			}

			fmt.Printf("  -> Result: Category=%s, Confidence=%s\n", diagnosis.Category, diagnosis.Confidence)
			fmt.Printf("  -> Reasoning: %s\n", diagnosis.Reasoning)
			if diagnosis.ProposedAction != "" {
				fmt.Printf("  -> Proposed Action: %s\n", diagnosis.ProposedAction)
			} else {
				fmt.Println("  -> Proposed Action: (None - suppressed due to Low Confidence)")
			}

			if !dryRun {
				// Event-Delta-Gate (WP1): flow_action nur schreiben, wenn sich der
				// Flag-Satz seit dem jüngsten flow_action-Event geändert hat. Das
				// killt den 30-s-Churn (413k Bestands-Events) an der Quelle;
				// verworfene Alternative war ein reines Cron-Delete ohne Gate — das
				// behebt die Quelle nicht und verwässert last_activity weiter.
				newHash := flagsHash(flaggedReasons)
				if flowActionChanged(lastFlowActionHash(ctx, p, init.ID), flaggedReasons) {
					payloadMap := map[string]any{
						"flagged_reasons": flaggedReasons,
						"flags_hash":      newHash,
						"category":        diagnosis.Category,
						"confidence":      diagnosis.Confidence,
						"reasoning":       diagnosis.Reasoning,
						"proposed_action": diagnosis.ProposedAction,
					}
					payloadBytes, _ := json.Marshal(payloadMap)

					_, err = p.Exec(ctx, `
						INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor, at)
						VALUES ($1, 'flow_action', 'flow_manager', $2, 'flow-manager', now())
					`, init.ID, string(payloadBytes))
					if err != nil {
						fmt.Fprintf(os.Stderr, "  ❌ Failed to write event for %s: %v\n", init.ID, err)
					} else {
						fmt.Printf("  ✓ Event 'flow_action' logged on %s\n", init.ID)
					}
				} else {
					fmt.Printf("  · flow_action für %s unverändert — Delta-Gate hält\n", init.ID)
				}

				// If category is "Workspace-gescheitert" (Workspace-bedingte Stagnation),
				// execute the explicit handover path: log a 'sage_action' event on the Initiative.
				if diagnosis.Category == "Workspace-gescheitert" {
					var targetWSID string
					for _, ws := range workspaces {
						if ws.Status == "failed" || ws.Status == "killed" {
							targetWSID = ws.ID
							break
						}
					}
					if targetWSID == "" && len(workspaces) > 0 {
						targetWSID = workspaces[0].ID
					}
					if targetWSID == "" {
						targetWSID = "00000000000000000000000000000000"
					}

					if targetWSID != "" {
						actionType := "handover"
						reasonMsg := fmt.Sprintf("Manager Handover (Workspace-bedingte Stagnation): %s", diagnosis.Reasoning)
						if init.Firma == "quantbot" {
							actionType = "escalate"
							reasonMsg = fmt.Sprintf("Live-Geld-Schutz (Workspace-bedingte Stagnation): %s", diagnosis.Reasoning)
						}

						handoverPayload := map[string]any{
							"workspace_id":    targetWSID,
							"action":          actionType,
							"reason":          reasonMsg,
							"source":          "manager",
						}
						handoverBytes, err := json.Marshal(handoverPayload)
						if err == nil {
							_, err = p.Exec(ctx, `
								INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor, at)
								VALUES ($1, 'sage_action', 'sage', $2, 'flow-manager', now())
							`, init.ID, string(handoverBytes))
							if err != nil {
								fmt.Fprintf(os.Stderr, "  ❌ Failed to write handover/escalate sage_action event for %s: %v\n", init.ID, err)
							} else {
								if init.Firma == "quantbot" {
									fmt.Printf("  ✓ Escalated stagnant workspace %s to vk-Sage (sage_action event logged with action=escalate)\n", targetWSID)
								} else {
									fmt.Printf("  ✓ Handed over stagnant workspace %s to vk-Sage (sage_action event logged)\n", targetWSID)
								}
							}
						}
					}
				}
			}

			// Accumulate for global digest report
			diagnosedCards = append(diagnosedCards, DiagnosedCard{
				Initiative:     init,
				FlaggedReasons: flaggedReasons,
				Diagnosis:      diagnosis,
			})
			fmt.Println()
		} else {
			// Card is not flagged. Check if the previous flow_action event was flagged, and clear it.
			var lastPayloadStr string
			err := p.QueryRow(ctx, `
				SELECT payload::text FROM portfolio.initiative_event 
				WHERE initiative_id = $1 AND kind = 'flow_action' 
				ORDER BY at DESC LIMIT 1
			`, init.ID).Scan(&lastPayloadStr)
			if err == nil {
				var lastPayload map[string]any
				if json.Unmarshal([]byte(lastPayloadStr), &lastPayload) == nil {
					reasons, _ := lastPayload["flagged_reasons"].([]any)
					if len(reasons) > 0 {
						// Previous state was flagged, now cleared! Log a clearing event.
						// (Eigenes Gate: schreibt nur beim Übergang flagged→leer, weil
						// der nächste Sweep flagged_reasons=[] sieht.)
						if !dryRun {
							payloadMap := map[string]any{
								"flagged_reasons": []string{},
								"flags_hash":      flagsHash(nil),
								"category":        "",
								"confidence":      "",
								"reasoning":       "",
								"proposed_action": "",
							}
							payloadBytes, _ := json.Marshal(payloadMap)
							_, err = p.Exec(ctx, `
								INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor, at)
								VALUES ($1, 'flow_action', 'flow_manager', $2, 'flow-manager', now())
							`, init.ID, string(payloadBytes))
							if err == nil {
								fmt.Printf("  ✓ Cleared stagnation flag for card %s\n", init.ID)
							}
						}
					}
				}
			}
		}
	}

	// 4. Generate and actively deliver Board-Review-Digest if we have diagnosed
	// cards or Zuordnungs-Findings (Eingangs-Gate-PRD W5)
	zuordnung := buildZuordnungSection(ctx, p)
	if len(diagnosedCards) > 0 || zuordnung != "" {
		digest := generateDigestReport(diagnosedCards) + zuordnung
		fmt.Println("=== 📨 Delivering Board-Review-Digest ===")
		if err := deliverDigest(digest, dryRun); err != nil {
			fmt.Fprintf(os.Stderr, "  ❌ Failed to deliver digest: %v\n", err)
		}
	} else {
		fmt.Println("No flagged cards found. Board is in perfect shape. No digest to deliver.")
	}

	return nil
}

// buildZuordnungSection meldet Zuordnungs-Drift (ADR-0011): tier-lose Karten,
// Karten mit triage:parent-check und echte Inbox-Reste. Leer = alles sauber.
func buildZuordnungSection(ctx context.Context, p *pgxpool.Pool) string {
	var sb strings.Builder

	collect := func(header string, query string) int {
		rows, err := p.Query(ctx, query)
		if err != nil {
			return 0
		}
		defer rows.Close()
		var items []string
		for rows.Next() {
			var id, detail string
			if rows.Scan(&id, &detail) == nil {
				items = append(items, fmt.Sprintf("  - `%s` — %s", id, detail))
			}
		}
		if len(items) == 0 {
			return 0
		}
		sb.WriteString(fmt.Sprintf("%s (%d):\n", header, len(items)))
		limit := len(items)
		if limit > 10 {
			limit = 10
		}
		for _, it := range items[:limit] {
			sb.WriteString(it + "\n")
		}
		if len(items) > limit {
			sb.WriteString(fmt.Sprintf("  - … %d weitere\n", len(items)-limit))
		}
		sb.WriteString("\n")
		return len(items)
	}

	total := 0
	total += collect("- **Karten ohne tier**",
		`SELECT id, firma || ' / ' || stage FROM portfolio.initiative
		  WHERE tier IS NULL AND archived_at IS NULL ORDER BY created_at DESC`)
	total += collect("- **Karten ohne bewussten parent_plan** (`triage:parent-check`)",
		`SELECT t.initiative_id, i.firma || ' / ' || i.stage
		   FROM portfolio.initiative_tag t
		   JOIN portfolio.initiative i ON i.id = t.initiative_id AND i.archived_at IS NULL
		  WHERE t.kind='triage' AND t.value='parent-check' ORDER BY t.added_at DESC`)
	total += collect("- **Karten ohne auflösbaren tier-Default** (`triage:tier-check`)",
		`SELECT t.initiative_id, i.firma || ' / ' || i.stage
		   FROM portfolio.initiative_tag t
		   JOIN portfolio.initiative i ON i.id = t.initiative_id AND i.archived_at IS NULL
		  WHERE t.kind='triage' AND t.value='tier-check' ORDER BY t.added_at DESC`)
	total += collect("- **tier nur per Repo-Default gesetzt** (`tier-source=default` — Fehlklassifikations-Kandidaten)",
		`SELECT t.initiative_id, i.firma || ' / ' || COALESCE(i.tier,'?')
		   FROM portfolio.initiative_tag t
		   JOIN portfolio.initiative i ON i.id = t.initiative_id AND i.archived_at IS NULL
		  WHERE t.kind='tier-source' AND t.value='default' ORDER BY t.added_at DESC`)
	total += collect("- **Inbox-Reste** (unlinked, nach Transienten-Filter)",
		`SELECT id, COALESCE(firma,'?') || ' — ' || left(title, 80)
		   FROM portfolio.unlinked_item ORDER BY discovered_at DESC`)

	if total == 0 {
		return ""
	}
	return "## 🧭 Zuordnung (ADR-0011)\n\n" + sb.String()
}

type DiagnosedCard struct {
	Initiative     FlowInitiative
	FlaggedReasons []string
	Diagnosis      FlowDiagnosis
}

func generateDigestReport(cards []DiagnosedCard) string {
	var sb strings.Builder
	sb.WriteString("# 🩺 KANBAN FLOW-MANAGER BOARD REVIEW DIGEST\n\n")
	sb.WriteString(fmt.Sprintf("Generated at: %s\n\n", time.Now().Format(time.RFC1123)))

	// Aggregate metrics
	stagnantCount := 0
	promoteCount := 0
	rotCount := 0
	wipCount := 0

	for _, c := range cards {
		for _, r := range c.FlaggedReasons {
			rLower := strings.ToLower(r)
			if strings.Contains(rLower, "stagnation") {
				stagnantCount++
			} else if strings.Contains(rLower, "promote") {
				promoteCount++
			} else if strings.Contains(rLower, "fäule") || strings.Contains(rLower, "backlog") {
				rotCount++
			} else if strings.Contains(rLower, "wip") {
				wipCount++
			}
		}
	}

	sb.WriteString("## 📊 Summary Metrics\n")
	sb.WriteString(fmt.Sprintf("- **Total Flagged Cards:** %d\n", len(cards)))
	sb.WriteString(fmt.Sprintf("- **Stagnant cards (Stagnation):** %d\n", stagnantCount))
	sb.WriteString(fmt.Sprintf("- **Promotion-ready cards (Promote-reif):** %d\n", promoteCount))
	sb.WriteString(fmt.Sprintf("- **Backlog Rot cards (Backlog-Fäule):** %d\n", rotCount))
	sb.WriteString(fmt.Sprintf("- **WIP Overflows (WIP-Überlauf):** %d\n\n", wipCount))

	sb.WriteString("## 🔍 Detailed Diagnoses\n\n")
	for i, c := range cards {
		sb.WriteString(fmt.Sprintf("### %d. %s (`%s`)\n", i+1, c.Initiative.Title, c.Initiative.ID))
		sb.WriteString(fmt.Sprintf("- **Company:** %s\n", c.Initiative.Firma))
		sb.WriteString(fmt.Sprintf("- **Current Stage:** %s\n", c.Initiative.Stage))
		sb.WriteString("- **Flagged Reasons:**\n")
		for _, r := range c.FlaggedReasons {
			sb.WriteString(fmt.Sprintf("  - %s\n", r))
		}
		sb.WriteString(fmt.Sprintf("- **Diagnosis Category:** `%s` (Confidence: %s)\n", c.Diagnosis.Category, c.Diagnosis.Confidence))
		sb.WriteString(fmt.Sprintf("- **Diagnostic Reasoning:** %s\n", c.Diagnosis.Reasoning))
		if c.Diagnosis.ProposedAction != "" {
			sb.WriteString(fmt.Sprintf("- **Proposed Action:** `%s`\n", c.Diagnosis.ProposedAction))
		} else {
			sb.WriteString("- **Proposed Action:** *(None - Low Confidence)*\n")
		}
		sb.WriteString("\n---\n\n")
	}

	return sb.String()
}

func deliverDigest(digest string, dryRun bool) error {
	recipient := os.Getenv("PORTFOLIO_DIGEST_RECIPIENT")
	if recipient == "" {
		recipient = "mariobrain/"
	}

	subject := "🩺 Flow-Manager Board-Review Digest"
	if dryRun {
		subject = "[DRY-RUN] " + subject
		fmt.Printf("Dry-run mode: would send digest mail to %s\n", recipient)
		return nil
	}

	cmd := exec.Command("gt", "mail", "send", recipient, "-s", subject, "--stdin")
	cmd.Stdin = strings.NewReader(digest)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run gt mail send: %w, output: %s", err, out.String())
	}
	fmt.Printf("✓ Digest successfully sent to %s via gt mail\n", recipient)
	return nil
}

func diagnoseFlaggedCard(init FlowInitiative, beads []LinkedBead, workspaces []LinkedWorkspace, events []FlowEvent, flaggedReasons []string) (FlowDiagnosis, error) {
	// Construct context string
	var contextBuilder strings.Builder
	contextBuilder.WriteString(fmt.Sprintf("Card Title: %s\n", init.Title))
	contextBuilder.WriteString(fmt.Sprintf("Firma/Company: %s\n", init.Firma))
	contextBuilder.WriteString(fmt.Sprintf("Stage: %s\n", init.Stage))
	contextBuilder.WriteString(fmt.Sprintf("Description: %s\n", init.Description))
	contextBuilder.WriteString(fmt.Sprintf("Created At: %v\n", init.CreatedAt.Format(time.RFC3339)))
	contextBuilder.WriteString(fmt.Sprintf("Updated At: %v\n", init.UpdatedAt.Format(time.RFC3339)))
	contextBuilder.WriteString("Reasons why this card is FLAGGED on the board:\n")
	for _, reason := range flaggedReasons {
		contextBuilder.WriteString(fmt.Sprintf(" - %s\n", reason))
	}

	contextBuilder.WriteString("\nVerlinked Beads and Statuses:\n")
	if len(beads) == 0 {
		contextBuilder.WriteString(" - None verlinked\n")
	} else {
		for _, b := range beads {
			contextBuilder.WriteString(fmt.Sprintf(" - Bead %s: status=%s\n", b.Ref, b.Status))
		}
	}

	contextBuilder.WriteString("\nActive Workspaces and Statuses:\n")
	if len(workspaces) == 0 {
		contextBuilder.WriteString(" - None verlinked\n")
	} else {
		for _, ws := range workspaces {
			contextBuilder.WriteString(fmt.Sprintf(" - Workspace %s: status=%s\n", ws.ID, ws.Status))
		}
	}

	contextBuilder.WriteString("\nRecent Event History on the Card:\n")
	if len(events) == 0 {
		contextBuilder.WriteString(" - No recent activity\n")
	} else {
		for _, ev := range events {
			contextBuilder.WriteString(fmt.Sprintf(" - Event %s by %s via %s (At: %v, From: %s, To: %s, Payload: %s)\n",
				ev.Kind, ev.Actor, ev.SourceBackend, ev.At.Format(time.RFC3339), ev.FromStage, ev.ToStage, ev.Payload))
		}
	}

	systemPrompt := `You are the Kanban-Flow-Manager, an expert AI overseer analyzing a card/initiative's stagnation, progress, or staleness.
Your goal is to diagnose the "Why" (das Warum) behind the card's flagged status.

Analyze the given card context, verlinked beads, active workspaces, and recent events.
Based on this context, classify the card's current state and diagnose the "Why" into EXACTLY one of these four categories:
1. "wartet-auf-Mensch" (waiting on human: e.g. needs triage, design decision, no active work and needs manual direction, or waiting for approval)
2. "Workspace-gescheitert" (workspace failed: e.g. active coding worktree failed, code run-reason codingagent exited 1 with no commits, or multiple failed retries)
3. "fertig-nicht-promotet" (completed but not promoted: e.g. all verlinked beads are closed but the card stage is not 'done')
4. "verlassen" (abandoned: e.g. stale backlog item, no verlinked beads, very old, not being worked on at all)

You must output a strict JSON object with the following schema:
{
  "category": "wartet-auf-Mensch" | "Workspace-gescheitert" | "fertig-nicht-promotet" | "verlassen",
  "confidence": "High" | "Low",
  "reasoning": "A concise, 2-3 sentence diagnostic reasoning of why this category fits the card.",
  "proposed_action": "A clear flow action (e.g. 'move stage to done', 're-dispatch bead st-xyz', 'archive', 'ask owner for input') OR empty if confidence is Low"
}

CRITICAL RULES:
- If confidence is "Low", the "proposed_action" field MUST be empty. Do not propose any action when confidence is Low.
- "category" must match one of the four categories exactly.
- Keep the response as valid, parseable JSON only. Do not add markdown wrapping or preambles outside of the JSON object.
`

	messages := []map[string]string{
		{"role": "user", "content": contextBuilder.String()},
	}

	resp, err := callGlm(systemPrompt, messages)
	if err != nil {
		return FlowDiagnosis{}, fmt.Errorf("call GLM error: %w", err)
	}

	cleanResp := strings.TrimSpace(resp)
	if strings.HasPrefix(cleanResp, "```") {
		if idx := strings.Index(cleanResp, "\n"); idx != -1 {
			cleanResp = cleanResp[idx+1:]
		}
		if idx := strings.LastIndex(cleanResp, "```"); idx != -1 {
			cleanResp = cleanResp[:idx]
		}
		cleanResp = strings.TrimSpace(cleanResp)
	}

	var diagnosis FlowDiagnosis
	if err := json.Unmarshal([]byte(cleanResp), &diagnosis); err != nil {
		return FlowDiagnosis{}, fmt.Errorf("failed to parse GLM JSON response: %v, raw response: %s", err, resp)
	}

	// Double check rule: if confidence is Low, empty the proposed action
	if strings.ToLower(diagnosis.Confidence) == "low" {
		diagnosis.Confidence = "Low"
		diagnosis.ProposedAction = ""
	} else {
		diagnosis.Confidence = "High"
	}

	return diagnosis, nil
}

// isLowerLayerEngaged checks if lower layers (Reactor, vk-Sage) are currently engaged on an initiative.
// These are:
// - active/waiting workspaces (kein aktiver/wartender Workspace)
// - open Reactor attempts or active/waiting beads in the queue (kein offener Reactor-Versuch, nicht in vk-Sages Queue)
// - active vk-Sage lease/lock on any verlinked beads
func isLowerLayerEngaged(ctx context.Context, p *pgxpool.Pool, initID string, beads []LinkedBead, workspaces []LinkedWorkspace) (bool, string, error) {
	// 1. Check for active or waiting workspaces (kein aktiver/wartender Workspace).
	// A workspace is active or waiting if its status is running, queued, waiting, pending, or empty (freshly setup).
	for _, ws := range workspaces {
		status := strings.ToLower(ws.Status)
		if status == "running" || status == "queued" || status == "waiting" || status == "pending" || status == "" {
			return true, fmt.Sprintf("active/waiting workspace exists (ID: %s, Status: '%s')", ws.ID, ws.Status), nil
		}
	}

	// 2. Check for open Reactor attempts or active/waiting beads in the queue.
	// If any verlinked bead is in progress or open or hooked (i.e. status is not 'closed'), the lower layers are engaged.
	for _, b := range beads {
		status := strings.ToLower(b.Status)
		if status != "closed" && status != "unknown" {
			return true, fmt.Sprintf("bead %s has active/pending status: '%s'", b.Ref, b.Status), nil
		}
	}

	// 3. Check for active vk-Sage lease/lock on any verlinked beads.
	for _, b := range beads {
		var activeLeaseExists bool
		err := p.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM portfolio.sage_lease 
				WHERE bead_id = $1 AND locked_until > NOW()
			)
		`, b.Ref).Scan(&activeLeaseExists)
		if err == nil && activeLeaseExists {
			return true, fmt.Sprintf("active vk-Sage lease exists for bead %s", b.Ref), nil
		}
	}

	// 4. Check for open Reactor attempt (kein offener Reactor-Versuch)
	eventRows, err := p.Query(ctx, `
		SELECT kind, source_backend, COALESCE(from_stage, ''), COALESCE(to_stage, ''), COALESCE(payload::text, '{}'), COALESCE(actor, ''), at 
		FROM portfolio.initiative_event 
		WHERE initiative_id = $1 
		ORDER BY at DESC 
		LIMIT 5
	`, initID)
	if err == nil {
		var events []FlowEvent
		for eventRows.Next() {
			var ev FlowEvent
			if err := eventRows.Scan(&ev.Kind, &ev.SourceBackend, &ev.FromStage, &ev.ToStage, &ev.Payload, &ev.Actor, &ev.At); err == nil {
				events = append(events, ev)
			}
		}
		eventRows.Close()

		if hasOpenReactorAttempt(events) {
			return true, "open Reactor attempt exists", nil
		}
	}

	// 5. Check if in vk-Sage's queue (healing or retry in progress: heal_count > 0 and heal_count < 2)
	if len(beads) > 0 {
		var beadRefs []string
		for _, b := range beads {
			beadRefs = append(beadRefs, b.Ref)
		}

		var hasActiveHeals bool
		err = p.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM portfolio.sage_heal_count
				WHERE bead_id = ANY($1) AND heal_count > 0 AND heal_count < 2
			)
		`, beadRefs).Scan(&hasActiveHeals)
		if err == nil && hasActiveHeals {
			// Double check if an escalation event has already been logged.
			var hasEscalated bool
			err = p.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM portfolio.initiative_event
					WHERE initiative_id = $1 AND kind = 'sage_action'
					  AND payload->>'action' = 'escalate'
				)
			`, initID).Scan(&hasEscalated)
			if err == nil && !hasEscalated {
				return true, "vk-Sage retry/healing is in progress (in vk-Sage's queue)", nil
			}
		}
	}

	return false, "", nil
}

func hasOpenReactorAttempt(events []FlowEvent) bool {
	for _, ev := range events {
		if ev.Kind == "deployed" {
			var payload map[string]any
			if err := json.Unmarshal([]byte(ev.Payload), &payload); err == nil {
				status, _ := payload["status"].(string)
				if status != "healthy" && status != "failed" && status != "rolled-back" && status != "blocked_migrations" && status != "" {
					return true
				}
			}
		}
		if ev.Kind == "dispatched" && time.Since(ev.At) < 15*time.Minute {
			hasNewerActivity := false
			for _, other := range events {
				if other.At.After(ev.At) && (other.Kind == "deployed" || other.Kind == "workspace_started") {
					hasNewerActivity = true
					break
				}
			}
			if !hasNewerActivity {
				return true
			}
		}
	}
	return false
}

var flowManagerChan = make(chan struct{}, 1)

func startFlowManagerSteward(p *pgxpool.Pool) {
	// Initialize status in db on startup
	_, _ = p.Exec(context.Background(),
		`INSERT INTO portfolio.sage_status (id, last_run, status, error_message)
		 VALUES ('flow-manager', now(), 'healthy', NULL)
		 ON CONFLICT (id) DO UPDATE SET
		    last_run = EXCLUDED.last_run,
		    status = EXCLUDED.status,
		    error_message = EXCLUDED.error_message`)

	go func() {
		// Run a full check on startup to initialize and catch up
		_ = runFlowManager(p, false)

		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			var checkErr error

			select {
			case <-flowManagerChan:
				// Edge-triggered: run flow manager
				checkErr = runFlowManager(p, false)
			case <-ticker.C:
				// Periodic: run flow manager
				checkErr = runFlowManager(p, false)
			}

			statusVal := "healthy"
			var errMsgVal *string
			if checkErr != nil {
				statusVal = "alarm"
				strErr := checkErr.Error()
				errMsgVal = &strErr
				fmt.Fprintf(os.Stderr, "Flow Manager Steward: check failed: %v\n", checkErr)
			}

			_, _ = p.Exec(context.Background(),
				`INSERT INTO portfolio.sage_status (id, last_run, status, error_message)
				 VALUES ('flow-manager', now(), $1, $2)
				 ON CONFLICT (id) DO UPDATE SET
				    last_run = EXCLUDED.last_run,
				    status = EXCLUDED.status,
				    error_message = EXCLUDED.error_message`, statusVal, errMsgVal)
		}
	}()
}
