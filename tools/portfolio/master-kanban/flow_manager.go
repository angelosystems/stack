package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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

	// 2. Evidenz gebatcht (WP1): EINE Link-Query + EINE Bead-Status-Query +
	// EIN sqlite3-Lauf fuer alle Workspaces — statt per-Karte-Einzelqueries
	// und ~190 sqlite-Spawns pro Sweep (werkstatt pids-Cap).
	beadRefsByCard, beadStatusByRef := loadBeadEvidence(ctx, p)
	allWorkspaces := loadAllWorkspaces()
	eventsByCard := loadRecentEvents(ctx, p)

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

	diagnosedCount := 0
	// GLM-Sicherungsschalter: scheitert eine Diagnose an Limit/Erreichbarkeit,
	// versucht dieser Sweep KEINE weitere (Flag-Delta bleibt offen, der
	// naechste Sweep probiert wieder EINE). Ohne den Schalter rannten ~49
	// offene Deltas pro Runde ins tote Z.ai-Wochenlimit (429-Hammer).
	glmDown := false

	// 3. For each initiative, gather context and diagnose if flagged
	for _, init := range initiatives {
		// A. Beads + Stati aus den Batch-Maps (Semantik wie vorher: nicht
		// gefundener/geloeschter Bead = "unknown").
		beads := beadsFor(init.ID, beadRefsByCard, beadStatusByRef)

		// mk-pipeline-ampel WP2: Bead-Zaehler an der Karte spiegeln (Cache fuer
		// die Summary-View — cross-DB geht dort nicht). Nur bei Delta schreiben.
		if len(beads) > 0 {
			closed := 0
			for _, b := range beads {
				if b.Status == "closed" {
					closed++
				}
			}
			_, _ = p.Exec(ctx, `UPDATE portfolio.initiative
			     SET beads_closed=$1, beads_total=$2
			     WHERE id=$3 AND (beads_closed IS DISTINCT FROM $1 OR beads_total IS DISTINCT FROM $2)`,
				closed, len(beads), init.ID)
		}

		// B. Workspaces aus dem globalen Snapshot — Matching ueber Bead-Refs
		// (Workspaces heissen sol-<bead-id>) + Karten-ID-Fallback. Der alte
		// LIKE '%karten-id%'-Lookup traf praktisch nie (WP1: blindes Auge).
		workspaces := workspacesFor(init.ID, beadRefsByCard[init.ID], allWorkspaces)

		// idea/soon→now: Arbeits-Evidenz-Vollzug (ADR-0011 now-Eintritt:
		// "Execution gestartet — erster Bead hooked/in_progress ODER Workspace
		// live"). Greift NUR bei vergebener Lane (Dispatch-Gate hat geoeffnet);
		// quantbot propose-only (Regel 3), locked respektiert applyStageProposal.
		if init.Stage == "idea" || init.Stage == "soon" {
			arbeitLaeuft := false
			for _, b := range beads {
				if b.Status == "hooked" || b.Status == "in_progress" {
					arbeitLaeuft = true
					break
				}
			}
			if !arbeitLaeuft {
				for _, ws := range workspaces {
					if ws.Status == "running" || ws.Status == "waiting" {
						arbeitLaeuft = true
						break
					}
				}
			}
			if arbeitLaeuft {
				var laneVergeben bool
				_ = p.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM portfolio.initiative_tag t
				     WHERE t.initiative_id=$1 AND t.kind='lane')`, init.ID).Scan(&laneVergeben)
				if laneVergeben {
					proposeOnly := init.Firma == "quantbot"
					if moved, reason, err := applyStageProposal(ctx, p, init.ID, "now",
						map[string]any{"evidence": "arbeit-laeuft (bead hooked/in_progress oder workspace aktiv)"},
						"flow-manager", proposeOnly); err == nil && moved {
						fmt.Printf("→ %s: %s→now (Arbeits-Evidenz)\n", init.ID, init.Stage)
					} else if err == nil && reason != "" {
						_ = reason
					}
				}
			}
		}

		// Events aus dem Batch (WP3: eine LATERAL-Query pro Sweep statt einer
		// Query pro Karte hier + einer zweiten in isLowerLayerEngaged).
		events := eventsByCard[init.ID]

		// Check if lower layers (Reactor, vk-Sage) are engaged (MUST-FIX Nygard/Newman)
		engaged, reason, err := isLowerLayerEngaged(ctx, p, init.ID, beads, workspaces, events)
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

		// now→watching: eigenstaendiger evidenzbasierter Vollzug (WP1: aus dem
		// Flag-Zweig herausgezogen — er hing an der GLM-Diagnose-Schleife).
		// idea/soon mit allen Beads closed ist bewusst KEIN Vollzugsfall:
		// das meldet die Findings-Klasse promote-sackgasse (Verwalter-Urteil).
		hasBeads := len(beads) > 0
		allBeadsClosed := true
		for _, b := range beads {
			if b.Status != "closed" {
				allBeadsClosed = false
				break
			}
		}
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

		// D. Evaluate flagging rules (L2). WIP-Ueberlauf ist KEIN Karten-Flag
		// mehr (WP1): Ueberbuchung ist eine Firma-Bedingung, kein Karten-Defekt
		// — das Cockpit zeigt sie (WIP-Pill + rote Spalte); frueher flaggte sie
		// jede now-Karte der Firma einzeln und trieb ~50 GLM-Diagnosen pro
		// Sweep. Promote-reif ist ebenfalls kein Flag mehr: now vollzieht oben,
		// idea/soon meldet die View-Klasse promote-sackgasse.
		var flaggedReasons []string

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

		// If flagged, run GLM diagnosis — aber NUR bei Flag-Delta (WP1):
		// unveraenderte Flag-Saetze kosten weder einen GLM-Call noch ein Event.
		// (Der Ist-Zustand diagnostizierte jede geflaggte Karte JEDE Runde neu
		// — 1.038 GLM-Calls in 90 min, bis das Z.ai-Wochenlimit fiel. Der
		// aeussere isLowerLayerEngaged-Skip oben hat engagierte Karten bereits
		// per continue aussortiert — der fruehere Recheck hier war toter Code.)
		if len(flaggedReasons) > 0 {
			newHash := flagsHash(flaggedReasons)
			if !flowActionChanged(lastFlowActionHash(ctx, p, init.ID), flaggedReasons) {
				continue // Delta-Gate hält: bekannter Zustand, nichts zu tun
			}
			if glmDown {
				continue // Sicherungsschalter: GLM ist in dieser Runde down
			}

			fmt.Printf("Diagnosing flagged card %s (%s, Stage: %s, Firma: %s)\n", init.ID, init.Title, init.Stage, init.Firma)
			fmt.Printf("  -> Flagged reasons: %s\n", strings.Join(flaggedReasons, " | "))

			diagnosis, err := diagnoseFlaggedCard(init, beads, workspaces, events, flaggedReasons)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ❌ Error diagnosing card %s: %v\n", init.ID, err)
				if strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "rate_limit") ||
					strings.Contains(err.Error(), "call GLM error") {
					glmDown = true
					fmt.Println("  ⛔ GLM nicht verfuegbar — keine weiteren Diagnose-Versuche in dieser Runde (Deltas bleiben offen)")
				}
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
				// Flag-Delta ist oben bereits geprüft (ein Gate für Diagnose UND
				// Event) — hier wird der neue Zustand nur noch persistiert. Das
				// flow_action-Event speist auch die steward_findings-Klasse
				// flow-diagnose (portfolio-029) — der Reflex/Verwalter-Kreislauf
				// ist der Meldeweg (der tote gt-mail-Digest entfiel, WP1).
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
							"workspace_id": targetWSID,
							"action":       actionType,
							"reason":       reasonMsg,
							"source":       "manager",
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

			diagnosedCount++
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

	// 4. Kein Mail-Digest mehr (WP1): der gt-mail-Weg scheiterte im
	// serve-Prozess IMMER ("not in a Gas Town workspace") und die
	// Zuordnungs-Inhalte sind laengst eigene steward_findings-Klassen.
	// Meldeweg der Diagnosen = flow_action-Events → View-Klasse
	// flow-diagnose → board-pflege-Reflex → Verwalter.
	if diagnosedCount > 0 {
		fmt.Printf("=== %d Karte(n) neu/geaendert diagnostiziert (flow_action → steward_findings) ===\n", diagnosedCount)
	} else {
		fmt.Println("Keine Flag-Deltas in dieser Runde.")
	}

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
// events kommen seit WP3 vom Aufrufer (Sweep-Batch loadRecentEvents) statt
// aus einer eigenen per-Karte-Query.
func isLowerLayerEngaged(ctx context.Context, p *pgxpool.Pool, initID string, beads []LinkedBead, workspaces []LinkedWorkspace, events []FlowEvent) (bool, string, error) {
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
	if hasOpenReactorAttempt(events) {
		return true, "open Reactor attempt exists", nil
	}

	// 5. Check if in vk-Sage's queue (healing or retry in progress: heal_count > 0 and heal_count < 2)
	if len(beads) > 0 {
		var beadRefs []string
		for _, b := range beads {
			beadRefs = append(beadRefs, b.Ref)
		}

		var hasActiveHeals bool
		err := p.QueryRow(ctx, `
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
