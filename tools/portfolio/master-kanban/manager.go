package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// FlowSignal represents the computed signals for an initiative
type FlowSignal struct {
	InitiativeID     string    `json:"initiative_id"`
	Firma            string    `json:"firma"`
	Stage            string    `json:"stage"`
	Title            string    `json:"title"`
	StageEnteredAt   time.Time `json:"stage_entered_at"`
	DaysInStage      float64   `json:"days_in_stage"`
	LastActivity     time.Time `json:"last_activity"`
	ActivityStaleHrs float64   `json:"activity_stale_hrs"`
	TotalBeads       int       `json:"total_beads"`
	ClosedBeads      int       `json:"closed_beads"`
	ActiveBeads      int       `json:"active_beads"`
	HasActiveWork    bool      `json:"has_active_work"`
}

// ProposalAction represents a proposed action for a flag
type ProposalAction struct {
	Label    string         `json:"label"`
	Endpoint string         `json:"endpoint"`
	Method   string         `json:"method"`
	Payload  map[string]any `json:"payload"`
}

// ManagerFlag represents a flagged issue detected on an initiative
type ManagerFlag struct {
	InitiativeID   string           `json:"initiative_id"`
	Firma          string           `json:"firma"`
	Stage          string           `json:"stage"`
	Title          string           `json:"title"`
	Type           string           `json:"type"` // "stagnation", "promote_ready", "stale", "wip_overflow"
	Classification string           `json:"classification"`
	Description    string           `json:"description"`
	Actions        []ProposalAction `json:"actions"`
}

// ManagerDigest represents the complete dashboard digest
type ManagerDigest struct {
	Stagnant     []ManagerFlag `json:"stagnant"`
	PromoteReady []ManagerFlag `json:"promote_ready"`
	Stale        []ManagerFlag `json:"stale"`
	WipOverflow  []ManagerFlag `json:"wip_overflow"`
	LastRun      time.Time     `json:"last_run"`
	Status       string        `json:"status"`
}

func startManagerSteward(p *pgxpool.Pool) {
	// Initialize status in DB on startup
	ctx := context.Background()
	_, _ = p.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS portfolio.manager_status (
			id             text PRIMARY KEY,
			last_run       timestamptz NOT NULL,
			status         text NOT NULL,
			error_message  text
		)
	`)
	_, _ = p.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS portfolio.manager_digest (
			id             text PRIMARY KEY,
			payload        jsonb NOT NULL,
			updated_at     timestamptz DEFAULT now() NOT NULL
		)
	`)
	_, _ = p.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS portfolio.manager_delivery (
			id             text PRIMARY KEY,
			last_delivered timestamptz NOT NULL,
			payload_hash   text NOT NULL
		)
	`)

	_, _ = p.Exec(ctx, `
		INSERT INTO portfolio.manager_status (id, last_run, status, error_message)
		VALUES ('manager-steward', now(), 'healthy', NULL)
		ON CONFLICT (id) DO UPDATE SET
			last_run = EXCLUDED.last_run,
			status = EXCLUDED.status,
			error_message = EXCLUDED.error_message
	`)

	go func() {
		// Run a sweep on startup
		_ = runManagerSweep(p)

		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			sweepErr := runManagerSweep(p)
			statusVal := "healthy"
			var errMsgVal *string
			if sweepErr != nil {
				statusVal = "alarm"
				strErr := sweepErr.Error()
				errMsgVal = &strErr
				fmt.Fprintf(os.Stderr, "Manager Steward: Sweep failed: %v\n", sweepErr)
			}

			_, _ = p.Exec(context.Background(), `
				INSERT INTO portfolio.manager_status (id, last_run, status, error_message)
				VALUES ('manager-steward', now(), $1, $2)
				ON CONFLICT (id) DO UPDATE SET
					last_run = EXCLUDED.last_run,
					status = EXCLUDED.status,
					error_message = EXCLUDED.error_message
			`, statusVal, errMsgVal)
		}
	}()
}

func runManagerSweep(p *pgxpool.Pool) error {
	ctx := context.Background()

	// 1. Fetch all unarchived initiatives
	rows, err := p.Query(ctx, `
		SELECT id, firma, stage, title, COALESCE(description, ''), created_at, updated_at
		FROM portfolio.initiative
		WHERE archived_at IS NULL
	`)
	if err != nil {
		return fmt.Errorf("failed to fetch initiatives: %w", err)
	}
	defer rows.Close()

	type initRow struct {
		id, firma, stage, title, description string
		created, updated                     time.Time
	}
	var initiatives []initRow
	for rows.Next() {
		var i initRow
		if err := rows.Scan(&i.id, &i.firma, &i.stage, &i.title, &i.description, &i.created, &i.updated); err == nil {
			initiatives = append(initiatives, i)
		}
	}

	// Connect to Beads Dolt database via solartownPool
	sp, spErr := solartownPool()

	// Setup structures for digest
	var stagnantFlags []ManagerFlag
	var promoteReadyFlags []ManagerFlag
	var staleFlags []ManagerFlag
	var wipOverflowFlags []ManagerFlag

	// Keep track of WIP counts per firma
	wipCounts := make(map[string]int)
	for _, init := range initiatives {
		if init.stage == "now" {
			wipCounts[init.firma]++
		}
	}

	for _, init := range initiatives {
		signal := FlowSignal{
			InitiativeID: init.id,
			Firma:        init.firma,
			Stage:        init.stage,
			Title:        init.title,
			LastActivity: init.updated,
		}

		// Calculate StageEnteredAt from portfolio.initiative_event (most recent stage move to current stage)
		var stageEntered time.Time
		err = p.QueryRow(ctx, `
			SELECT at FROM portfolio.initiative_event
			WHERE initiative_id = $1 AND kind = 'moved' AND to_stage = $2
			ORDER BY at DESC LIMIT 1
		`, init.id, init.stage).Scan(&stageEntered)
		if err != nil {
			stageEntered = init.created
		}
		signal.StageEnteredAt = stageEntered
		signal.DaysInStage = time.Since(stageEntered).Hours() / 24.0

		// Calculate LastActivity
		var maxEventAt time.Time
		err = p.QueryRow(ctx, `
			SELECT max(at) FROM portfolio.initiative_event
			WHERE initiative_id = $1
		`, init.id).Scan(&maxEventAt)
		if err == nil && !maxEventAt.IsZero() {
			signal.LastActivity = maxEventAt
		}
		signal.ActivityStaleHrs = time.Since(signal.LastActivity).Hours()

		// Gather verlinked beads and active/waiting workspaces
		var beads []LinkedBead
		var beadRefs []string
		linkRows, err := p.Query(ctx, `
			SELECT ref FROM portfolio.initiative_link
			WHERE initiative_id = $1 AND kind = 'bead'
		`, init.id)
		if err == nil {
			for linkRows.Next() {
				var ref string
				if linkRows.Scan(&ref) == nil {
					beadRefs = append(beadRefs, ref)
				}
			}
			linkRows.Close()
		}
		if spErr == nil && sp != nil {
			for _, ref := range beadRefs {
				var status string
				err := sp.QueryRow(ctx, "SELECT status FROM beads.issues WHERE id = $1 AND deleted_at IS NULL", ref).Scan(&status)
				if err == nil {
					beads = append(beads, LinkedBead{Ref: ref, Status: status})
				} else {
					beads = append(beads, LinkedBead{Ref: ref, Status: "unknown"})
				}
			}
		}

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
			`, init.id)
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

		var vkRefs []string
		vkLinkRows, err := p.Query(ctx, `
			SELECT ref FROM portfolio.initiative_link
			WHERE initiative_id = $1 AND kind = 'vk_workspace'
		`, init.id)
		if err == nil {
			for vkLinkRows.Next() {
				var ref string
				if vkLinkRows.Scan(&ref) == nil {
					vkRefs = append(vkRefs, ref)
				}
			}
			vkLinkRows.Close()
		}

		if len(vkRefs) > 0 {
			if _, statErr := os.Stat(vkDB); statErr == nil {
				for _, ref := range vkRefs {
					hexID := strings.ToUpper(strings.ReplaceAll(ref, "-", ""))
					query := fmt.Sprintf(`
						SELECT hex(w.id), COALESCE(ep.status, '')
						FROM workspaces w
						LEFT JOIN sessions s ON s.workspace_id = w.id
						LEFT JOIN execution_processes ep ON ep.session_id = s.id
						WHERE hex(w.id)='%s' AND w.archived = 0;
					`, hexID)
					cmd := exec.Command("sqlite3", "-readonly", vkDB, query)
					var out bytes.Buffer
					cmd.Stdout = &out
					if cmd.Run() == nil {
						wsLines := strings.Split(strings.TrimSpace(out.String()), "\n")
						for _, line := range wsLines {
							parts := strings.Split(line, "|")
							if len(parts) >= 2 {
								workspaces = append(workspaces, LinkedWorkspace{ID: parts[0], Status: parts[1]})
							}
						}
					}
				}
			}
		}

		// Calculate signal values based on gathered beads & workspaces
		signal.TotalBeads = len(beads)
		for _, b := range beads {
			if b.Status == "closed" {
				signal.ClosedBeads++
			} else if b.Status == "open" || b.Status == "in_progress" || b.Status == "hooked" {
				signal.ActiveBeads++
			}
		}
		for _, ws := range workspaces {
			if ws.Status == "running" {
				signal.HasActiveWork = true
			}
		}
		if signal.ActiveBeads > 0 {
			signal.HasActiveWork = true
		}

		// Check if lower layer (Reactor, vk-Sage, or running/waiting workspaces) is engaged.
		// Establishing escalation ladder Reactor -> vk-Sage -> Manager: Manager only acts if lower layers are not engaged.
		engaged, err := isLowerLayerEngagedManager(ctx, p, init.id, beadRefs, vkRefs)
		if err == nil && engaged {
			signal.HasActiveWork = true
		}

		// 1. Detection: Stagnation (stockend)
		stgThresh := GetStageThreshold(init.firma, init.stage)
		isStagnant := false
		if (init.stage == "now" || init.stage == "soon") && stgThresh > 0 && signal.ActivityStaleHrs > stgThresh.Hours() && !signal.HasActiveWork {
			isStagnant = true
		}

		if isStagnant {
			// Rule-based Diagnosis fallback
			diagnosis := "wartet-auf-Mensch"
			desc := fmt.Sprintf("Inaktivität seit %.1f Stunden in Stage '%s' ohne aktive Beads/Workspaces.", signal.ActivityStaleHrs, init.stage)
			proposedAction := ""
			confidence := "High"

			// Try to load the latest GLM diagnosis from portfolio.initiative_event
			var flowActionPayload string
			err := p.QueryRow(ctx, `
				SELECT payload::text FROM portfolio.initiative_event
				WHERE initiative_id = $1 AND kind = 'flow_action'
				ORDER BY at DESC LIMIT 1
			`, init.id).Scan(&flowActionPayload)
			if err == nil {
				var parsed struct {
					Category       string `json:"category"`
					Confidence     string `json:"confidence"`
					Reasoning      string `json:"reasoning"`
					ProposedAction string `json:"proposed_action"`
				}
				if json.Unmarshal([]byte(flowActionPayload), &parsed) == nil {
					if parsed.Category != "" {
						diagnosis = parsed.Category
					}
					if parsed.Reasoning != "" {
						desc = parsed.Reasoning
					}
					proposedAction = parsed.ProposedAction
					confidence = parsed.Confidence
				}
			}

			// Log Event (manager_flag) with Cooldown (once every 24 hours per initiative & flag type)
			err = logManagerFlagWithCooldown(p, init.id, "stagnation", "Stagnation: "+diagnosis, desc)
			if err == nil {
				var actions []ProposalAction
				// Low-Confidence underdrückt Aktions-Vorschläge (R-B / Acceptanz-Kriterium)
				if strings.ToLower(confidence) != "low" {
					if proposedAction != "" && proposedAction != "handover" {
						actions = append(actions, ProposalAction{
							Label:    proposedAction,
							Endpoint: "/api/dispatch",
							Method:   "POST",
							Payload: map[string]any{
								"id":   init.id,
								"note": fmt.Sprintf("Proposed action '%s' via Flow Manager", proposedAction),
							},
						})
					} else if init.firma == "quantbot" {
						actions = []ProposalAction{
							{
								Label:    "Eskalieren",
								Endpoint: "/api/escalate",
								Method:   "POST",
								Payload: map[string]any{
									"id":     init.id,
									"reason": "Eskalation wegen Stagnation (Live-Geld-Schutz)",
								},
							},
						}
					} else {
						actions = []ProposalAction{
							{
								Label:    "Re-Dispatch",
								Endpoint: "/api/dispatch",
								Method:   "POST",
								Payload: map[string]any{
									"id":   init.id,
									"lane": "hack",
									"note": "Re-dispatch stagnant initiative via Flow Manager",
								},
							},
							{
								Label:    "Eskalieren",
								Endpoint: "/api/escalate",
								Method:   "POST",
								Payload: map[string]any{
									"id":     init.id,
									"reason": "Eskalation wegen Stagnation (Inaktivität)",
								},
							},
						}
					}
				}

				stagnantFlags = append(stagnantFlags, ManagerFlag{
					InitiativeID:   init.id,
					Firma:          init.firma,
					Stage:          init.stage,
					Title:          init.title,
					Type:           "stagnation",
					Classification: "Stagnation: " + diagnosis,
					Description:    desc,
					Actions:        actions,
				})
			}
		}

		// 2. Detection: Promote-reif (promote-ready)
		// All linked beads closed, but stage is not 'done'
		if init.stage != "done" && signal.TotalBeads > 0 && signal.ClosedBeads == signal.TotalBeads {
originalDesc := fmt.Sprintf("Alle %d verlinkten Beads sind geschlossen, aber die Karte befindet sich noch in Stage '%s'.", signal.TotalBeads, init.stage)
			originalClassification := "Promote-reif"

			nowCount := wipCounts[init.firma]
			targetStage := GetPromoteTargetStage(ctx, p, sp, spErr, init.stage, init.firma, nowCount)

			originalActions := []ProposalAction{
				{
					Label:    "Ein-Klick-Promote",
					Endpoint: "/api/move",
					Method:   "POST",
					Payload: map[string]any{
						"id":    init.id,
						"stage": targetStage,
					},
				},
			}
			// Live-Geld-Schutz: quantbot niemals promoten, immer eskalieren
			if init.firma == "quantbot" {
				originalActions = []ProposalAction{{
					Label:    "Eskalieren",
					Endpoint: "/api/escalate",
					Method:   "POST",
					Payload: map[string]any{
						"id":     init.id,
						"reason": "Eskalation wegen Promote-Reife (Live-Geld-Schutz)",
					},
				}}
			}

			classification, description, actions, err := getOrRunDiagnosis(ctx, p, init.id, "promote_ready", originalDesc, originalClassification, originalActions, signal, init.title, init.description, init.stage, init.firma)
			// Live-Geld-Schutz: enforce Eskalieren even if GLM returned low-confidence
			if init.firma == "quantbot" {
				actions = []ProposalAction{{
					Label:    "Eskalieren",
					Endpoint: "/api/escalate",
					Method:   "POST",
					Payload:  map[string]any{"id": init.id, "reason": "Eskalation wegen Promote-Reife (Live-Geld-Schutz)"},
				}}
			}
			if err == nil {
				promoteReadyFlags = append(promoteReadyFlags, ManagerFlag{
					InitiativeID:   init.id,
					Firma:          init.firma,
					Stage:          init.stage,
					Title:          init.title,
					Type:           "promote_ready",
					Classification: classification,
					Description:    description,
					Actions:        actions,
				})
			}
		}

		// 3. Detection: Backlog-Fäule / Veraltet (stale)
		// Stage is 'idea' and stale threshold exceeded
		staleThresh := GetStageThreshold(init.firma, "idea")
		if init.stage == "idea" && staleThresh > 0 && signal.ActivityStaleHrs > staleThresh.Hours() {
			desc := fmt.Sprintf("Karte befindet sich seit %.1f Tagen ohne Aktivität in Stage 'idea' (Backlog-Fäule).", signal.ActivityStaleHrs/24.0)
			err := logManagerFlagWithCooldown(p, init.id, "stale", "Veraltet (Backlog-Fäule)", desc)
			if err == nil {
				staleFlags = append(staleFlags, ManagerFlag{
					InitiativeID:   init.id,
					Firma:          init.firma,
					Stage:          init.stage,
					Title:          init.title,
					Type:           "stale",
					Classification: "Veraltet (Backlog-Fäule)",
					Description:    desc,
					Actions: []ProposalAction{
						{
							Label:    "Review",
							Endpoint: "/api/comment",
							Method:   "POST",
							Payload: map[string]any{
								"id":   init.id,
								"text": "Review: noch relevant? (Automatische Nachfrage des Flow-Managers)",
							},
						},
						{
							Label:    "Archivieren",
							Endpoint: "/api/archive",
							Method:   "POST",
							Payload: map[string]any{
								"id": init.id,
							},
						},
					},
				})
			}
		}

		// 4. Detection: WIP-Überlauf (wip_overflow)
		// Stage is 'now' and WIP limit of current firma exceeded
		nowLimit, _ := getWIPLimits(init.firma)
		currentWIP := wipCounts[init.firma]
		if init.stage == "now" && currentWIP > nowLimit {
			desc := fmt.Sprintf("WIP-Limit für %s überschritten (%d Karten in NOW, Limit ist %d).", init.firma, currentWIP, nowLimit)
			err := logManagerFlagWithCooldown(p, init.id, "wip_overflow", "WIP-Überlauf", desc)
			if err == nil {
				wipOverflowFlags = append(wipOverflowFlags, ManagerFlag{
					InitiativeID:   init.id,
					Firma:          init.firma,
					Stage:          init.stage,
					Title:          init.title,
					Type:           "wip_overflow",
					Classification: "WIP-Überlauf",
					Description:    desc,
				})
			}
		}
	}

	// 5. Serialize and store the complete digest as the 'latest' record in portfolio.manager_digest
	digest := ManagerDigest{
		Stagnant:     stagnantFlags,
		PromoteReady: promoteReadyFlags,
		Stale:        staleFlags,
		WipOverflow:  wipOverflowFlags,
		LastRun:      time.Now(),
		Status:       "healthy",
	}

	digestBytes, err := json.Marshal(digest)
	if err != nil {
		return fmt.Errorf("failed to marshal manager digest: %w", err)
	}

	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.manager_digest (id, payload, updated_at)
		VALUES ('latest', $1::jsonb, now())
		ON CONFLICT (id) DO UPDATE SET
			payload = EXCLUDED.payload,
			updated_at = now()
	`, string(digestBytes))
	if err != nil {
		return fmt.Errorf("failed to save manager digest: %w", err)
	}

	_ = triggerManagerDigestDelivery(p, digest)

	return nil
}

func logManagerFlagWithCooldown(p *pgxpool.Pool, initiativeID string, flagType string, classification string, description string) error {
	ctx := context.Background()

	// Cooldown logic: check if an identical manager_flag event was logged for this initiative within the last 24 hours
	var exists bool
	err := p.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM portfolio.initiative_event
			WHERE initiative_id = $1 AND kind = 'manager_flag'
			  AND payload->>'type' = $2
			  AND at > now() - interval '24 hours'
		)
	`, initiativeID, flagType).Scan(&exists)

	if err == nil && !exists {
		payloadMap := map[string]any{
			"type":           flagType,
			"classification": classification,
			"description":    description,
		}
		payloadBytes, _ := json.Marshal(payloadMap)

		_, err := p.Exec(ctx, `
			INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
			VALUES ($1, 'manager_flag', 'master', $2::jsonb, 'flow-manager')
		`, initiativeID, string(payloadBytes))
		if err != nil {
			return fmt.Errorf("failed to insert manager_flag event: %w", err)
		}
	}

	return nil
}

func triggerManagerDigestDelivery(p *pgxpool.Pool, digest ManagerDigest) error {
	ctx := context.Background()

	// 1. Compute SHA256 hash of the flags in the current digest
	currentHash := computeDigestHash(digest)

	// 2. Fetch the last delivered record
	var lastDelivered time.Time
	var lastHash string
	err := p.QueryRow(ctx, `
		SELECT last_delivered, payload_hash FROM portfolio.manager_delivery WHERE id = 'latest'
	`).Scan(&lastDelivered, &lastHash)

	isFirstDelivery := false
	if err != nil {
		isFirstDelivery = true
	}

	// 3. Define the rules for active delivery
	shouldDeliver := false
	reason := ""

	cooldownPeriod := 24 * time.Hour
	if val := os.Getenv("MANAGER_DELIVERY_COOLDOWN"); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			cooldownPeriod = d
		}
	}

	// Minimum safety margin to prevent any rapid storm loop (e.g. 1 minute)
	minSafetyMargin := 1 * time.Minute

	if isFirstDelivery {
		// First run and we have actual findings to report
		if hasDigestFlags(digest) {
			shouldDeliver = true
			reason = "initial delivery of active flags"
		}
	} else {
		timeSinceLast := time.Since(lastDelivered)
		hashChanged := currentHash != lastHash

		if timeSinceLast >= cooldownPeriod {
			// Routine periodic review delivery
			if hasDigestFlags(digest) {
				shouldDeliver = true
				reason = fmt.Sprintf("periodic delivery (cooldown of %v elapsed)", cooldownPeriod)
			}
		} else if hashChanged && timeSinceLast >= minSafetyMargin {
			// Immediate delivery due to change of flagged issues on the board
			if hasDigestFlags(digest) {
				shouldDeliver = true
				reason = fmt.Sprintf("change detected in board flags (time since last: %v)", timeSinceLast.Round(time.Second))
			}
		}
	}

	if shouldDeliver {
		fmt.Printf("🩺 Flow Manager: Triggering active delivery of digest. Reason: %s\n", reason)

		subject := fmt.Sprintf("[Flow Manager] Board-Review-Digest - %d flags", countDigestFlags(digest))
		content := formatDigestMarkdown(digest)

		// A. Delivery via Mail (gt mail send)
		_ = sendMailDigest(subject, content)

		// B. Delivery via Fabric (JSON Push)
		sendFabricDigest(digest)

		// C. Delivery via Dashboard: log a system-wide notice or special notification event in portfolio.initiative_event
		// Let's log it on 'mb-master-kanban-build' (representing Kanban system) or insert a general event
		payloadMap := map[string]any{
			"subject": subject,
			"digest_summary": fmt.Sprintf("WIP Overflows: %d | Stagnant: %d | Promote Ready: %d | Stale: %d",
				len(digest.WipOverflow), len(digest.Stagnant), len(digest.PromoteReady), len(digest.Stale)),
			"timestamp": time.Now().Format(time.RFC3339),
		}
		payloadBytes, _ := json.Marshal(payloadMap)
		_, _ = p.Exec(ctx, `
			INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
			VALUES ('mb-master-kanban-build', 'commented', 'master', $1::jsonb, 'flow-manager')
			ON CONFLICT DO NOTHING
		`, string(payloadBytes))

		// 4. Update the delivery record
		_, err = p.Exec(ctx, `
			INSERT INTO portfolio.manager_delivery (id, last_delivered, payload_hash)
			VALUES ('latest', now(), $1)
			ON CONFLICT (id) DO UPDATE SET
				last_delivered = now(),
				payload_hash = EXCLUDED.payload_hash
		`, currentHash)
		if err != nil {
			return fmt.Errorf("failed to update manager_delivery: %w", err)
		}
	}

	return nil
}

func hasDigestFlags(digest ManagerDigest) bool {
	return len(digest.Stagnant) > 0 || len(digest.PromoteReady) > 0 || len(digest.Stale) > 0 || len(digest.WipOverflow) > 0
}

func countDigestFlags(digest ManagerDigest) int {
	return len(digest.Stagnant) + len(digest.PromoteReady) + len(digest.Stale) + len(digest.WipOverflow)
}

func computeDigestHash(digest ManagerDigest) string {
	// Only hash the flags, not LastRun
	temp := struct {
		Stagnant     []ManagerFlag `json:"stagnant"`
		PromoteReady []ManagerFlag `json:"promote_ready"`
		Stale        []ManagerFlag `json:"stale"`
		WipOverflow  []ManagerFlag `json:"wip_overflow"`
	}{
		Stagnant:     digest.Stagnant,
		PromoteReady: digest.PromoteReady,
		Stale:        digest.Stale,
		WipOverflow:  digest.WipOverflow,
	}
	b, _ := json.Marshal(temp)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func formatDigestMarkdown(digest ManagerDigest) string {
	var sb strings.Builder
	sb.WriteString("# 🩺 Kanban-Flow-Manager Board-Review-Digest\n")
	sb.WriteString(fmt.Sprintf("Generated at: %s\n\n", digest.LastRun.Format("2006-01-02 15:04:05")))

	if len(digest.WipOverflow) > 0 {
		sb.WriteString("## ⚠ WIP-Overflow (WIP-Überlauf)\n")
		for _, flag := range digest.WipOverflow {
			sb.WriteString(fmt.Sprintf("- **%s** (%s): %s\n", flag.Title, flag.Firma, flag.Description))
		}
		sb.WriteString("\n")
	}

	if len(digest.Stagnant) > 0 {
		sb.WriteString("## ⏳ Stagnation (Stockende Initiativen)\n")
		for _, flag := range digest.Stagnant {
			sb.WriteString(fmt.Sprintf("- **%s** (Firma: %s, Stage: %s)\n", flag.Title, flag.Firma, flag.Stage))
			sb.WriteString(fmt.Sprintf("  - *ID*: `%s` | *Diagnose*: %s\n", flag.InitiativeID, flag.Classification))
			sb.WriteString(fmt.Sprintf("  - *Detail*: %s\n", flag.Description))
		}
		sb.WriteString("\n")
	}

	if len(digest.PromoteReady) > 0 {
		sb.WriteString("## 🚀 Promote-Ready (Promote-reife Initiativen)\n")
		for _, flag := range digest.PromoteReady {
			sb.WriteString(fmt.Sprintf("- **%s** (Firma: %s, Stage: %s)\n", flag.Title, flag.Firma, flag.Stage))
			sb.WriteString(fmt.Sprintf("  - *ID*: `%s` | *Detail*: %s\n", flag.InitiativeID, flag.Description))
		}
		sb.WriteString("\n")
	}

	if len(digest.Stale) > 0 {
		sb.WriteString("## 🍂 Stale (Veraltet / Backlog-Fäule)\n")
		for _, flag := range digest.Stale {
			sb.WriteString(fmt.Sprintf("- **%s** (Firma: %s, Stage: %s)\n", flag.Title, flag.Firma, flag.Stage))
			sb.WriteString(fmt.Sprintf("  - *ID*: `%s` | *Detail*: %s\n", flag.InitiativeID, flag.Description))
		}
		sb.WriteString("\n")
	}

	if len(digest.WipOverflow) == 0 && len(digest.Stagnant) == 0 && len(digest.PromoteReady) == 0 && len(digest.Stale) == 0 {
		sb.WriteString("Everything is flowing beautifully! No flags detected on the board today. 🌸\n")
	}

	return sb.String()
}

func sendMailDigest(subject string, content string) error {
	cmd := exec.Command("gt", "mail", "send", "mayor/", "-s", subject, "--stdin")
	cmd.Stdin = strings.NewReader(content)

	var errBytes bytes.Buffer
	cmd.Stderr = &errBytes

	err := cmd.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Flow Manager: Failed to send mail via gt: %v, stderr: %s\n", err, errBytes.String())
		simFile := "/tmp/flow-manager-mail-sim.txt"
		_ = os.WriteFile(simFile, []byte(fmt.Sprintf("Subject: %s\n\n%s", subject, content)), 0644)
	} else {
		fmt.Printf("✓ Flow Manager: Successfully delivered Board-Review-Digest via gt mail to mayor/\n")
	}
	return nil
}

func sendFabricDigest(digest ManagerDigest) {
	webhookURL := os.Getenv("FABRIC_WEBHOOK_URL")
	if webhookURL == "" {
		fmt.Println("[Fabric Push] (Simulated): Successfully pushed Digest payload to Fabric.")
		return
	}

	payloadBytes, err := json.Marshal(digest)
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", webhookURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	cl := &http.Client{}
	resp, err := cl.Do(req)
	if err == nil {
		resp.Body.Close()
		fmt.Println("[Fabric Push]: Successfully pushed Digest payload to configured webhook.")
	} else {
		fmt.Fprintf(os.Stderr, "[Fabric Push] Error: %v\n", err)
	}
}

// GetPromoteTargetStage computes the promote target stage from the current stage based on the P2.4 Stage-Übergangs-Map (L7)
func GetPromoteTargetStage(ctx context.Context, p *pgxpool.Pool, sp *pgxpool.Pool, spErr error, currentStage string, firma string, nowCount int) string {
	targetStage := "done"
	switch currentStage {
	case "idea":
		hasCapacity := false
		if spErr == nil && sp != nil {
			firmaRig := map[string]string{
				"stayawesome": "stayawesomeOS",
				"solartown":   "testrig",
				"quantbot":    "quantumshift",
				"stack":       "stack",
				"angeloos":    "clean",
				"mariobrain":  "mariobrain",
			}
			rig := firmaRig[firma]
			if rig != "" {
				idlePolecats, err := getRigIdleCapacity(ctx, rig)
				if err == nil && idlePolecats > 0 {
					hasCapacity = true
				} else {
					var vkCount int
					err = sp.QueryRow(ctx, "SELECT count(*) FROM beads.issues WHERE rig=$1 AND status='hooked' AND assignee LIKE 'vk/%'", rig).Scan(&vkCount)
					if err == nil {
						vkSlots := 5 - vkCount
						if vkSlots > 0 {
							hasCapacity = true
						}
					}
				}
			}
		}
		nowLimit, _ := getWIPLimits(firma)
		if hasCapacity && nowCount < nowLimit {
			targetStage = "now"
		} else {
			targetStage = "soon"
		}
	case "soon":
		targetStage = "now"
	case "now":
		targetStage = "watching"
	case "watching":
		targetStage = "done"
	}
	return targetStage
}

func isLowerLayerEngagedManager(ctx context.Context, p *pgxpool.Pool, initID string, beadRefs []string, vkRefs []string) (bool, error) {
	// 1. Check for Active/Waiting Workspace in Vibe Kanban SQLite
	vkDB := envOr("VIBE_KANBAN_DB", "/root/.local/share/vibe-kanban/db.v2.sqlite")
	if _, statErr := os.Stat(vkDB); statErr == nil {
		var whereParts []string
		for _, ref := range vkRefs {
			hexID := strings.ToUpper(strings.ReplaceAll(ref, "-", ""))
			whereParts = append(whereParts, fmt.Sprintf("hex(w.id)='%s'", hexID))
		}
		for _, ref := range beadRefs {
			whereParts = append(whereParts, fmt.Sprintf("w.name LIKE '%%%s%%'", ref))
		}

		if len(whereParts) > 0 {
			sqliteQuery := fmt.Sprintf(`
				SELECT count(*)
				FROM workspaces w
				LEFT JOIN sessions s ON s.workspace_id = w.id
				LEFT JOIN execution_processes ep ON ep.session_id = s.id
				WHERE (ep.status = 'running' OR (w.created_at > datetime('now', '-15 minutes') AND w.archived = 0))
				  AND (%s);
			`, strings.Join(whereParts, " OR "))

			cmd := exec.Command("sqlite3", "-readonly", vkDB, sqliteQuery)
			var out bytes.Buffer
			cmd.Stdout = &out
			if cmd.Run() == nil {
				var count int
				if _, scanErr := fmt.Sscanf(strings.TrimSpace(out.String()), "%d", &count); scanErr == nil && count > 0 {
					return true, nil
				}
			}
		}
	}

	// 2. Check for Open Reactor/Dispatch Attempt in PostgreSQL
	var hasRecentDispatch bool
	err := p.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM portfolio.initiative_event
			WHERE initiative_id = $1 AND kind = 'dispatched'
			  AND at > now() - interval '30 minutes'
		)
	`, initID).Scan(&hasRecentDispatch)
	if err == nil && hasRecentDispatch {
		return true, nil
	}

	// 3. Check if in vk-Sage's queue (healing or retry in progress)
	if len(beadRefs) > 0 {
		// A. Check active sage_lease
		var hasActiveLease bool
		err = p.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM portfolio.sage_lease
				WHERE bead_id = ANY($1) AND locked_until > now()
			)
		`, beadRefs).Scan(&hasActiveLease)
		if err == nil && hasActiveLease {
			return true, nil
		}

		// B. Check sage_heal_count (healing retries in progress: heal_count > 0 and heal_count < 2)
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
				return true, nil
			}
		}
	}

	return false, nil
}

const diagnosisSystemPrompt = `You are the Kanban-Flow-Manager Diagnosis Agent. Your job is to analyze a flagged Kanban card (initiative) on a master board and determine the root cause of its issue (the "Warum").

Analyze the card's details, metadata, linked beads progress, and the chronological timeline of recent card events (Kontext & Outcome) to determine why it is stagnant, promote-ready, stale, or overflowing WIP limits.

You must categorize the diagnosis into exactly one of these four categories:
1. "wartet-auf-Mensch" - The task is waiting for a human developer, reviewer, or operator. E.g., PR is waiting for review, manual feedback is required, a task stagnates with no active beads/errors, or a WIP limit overflow occurs because of slow manual progress.
2. "Workspace-gescheitert" - A workspace run or agent execution failed, exited with errors, or hit a blockade. E.g., tests failed, build crashed, or setup/provisioning error.
3. "fertig-nicht-promotet" - All linked beads are done or closed, but the card remains in an active stage (e.g. NOW or SOON) and has not been moved to DONE.
4. "verlassen" - The card is stale or abandoned. E.g., extremely long inactivity in the IDEA/backlog stage, or no updates/activity in a long time.

Provide a detailed explanation in German ("justification") describing the specific situation based on the event timeline and metadata.

Assess your confidence:
- "high": You are highly confident in this diagnosis.
- "low": There is high ambiguity, conflicting signals, or insufficient context to be sure. (Note: Low confidence will suppress automated action proposals, acting as an advisory-only warning).

You MUST return a JSON object with the following fields:
{
  "category": "wartet-auf-Mensch" | "Workspace-gescheitert" | "fertig-nicht-promotet" | "verlassen",
  "justification": "German explanation...",
  "confidence": "high" | "low"
}

Do NOT output any markdown formatting other than optionally wrapping the JSON in a "json" code block. Only return the JSON object.`

func getFallbackCategory(flagType string) string {
	switch flagType {
	case "stagnation":
		return "wartet-auf-Mensch"
	case "promote_ready":
		return "fertig-nicht-promotet"
	case "stale":
		return "verlassen"
	case "wip_overflow":
		return "wartet-auf-Mensch"
	default:
		return "wartet-auf-Mensch"
	}
}

func getOrRunDiagnosis(
	ctx context.Context,
	p *pgxpool.Pool,
	initiativeID string,
	flagType string,
	originalDesc string,
	originalClassification string,
	originalActions []ProposalAction,
	signal FlowSignal,
	title string,
	cardDescription string,
	stage string,
	firma string,
) (string, string, []ProposalAction, error) {
	// 1. Try to find an existing event of kind 'manager_flag' and payload type = flagType within the last 24 hours
	var payloadBytes []byte
	err := p.QueryRow(ctx, `
		SELECT payload FROM portfolio.initiative_event
		WHERE initiative_id = $1 AND kind = 'manager_flag'
		  AND payload->>'type' = $2
		  AND at > now() - interval '24 hours'
		ORDER BY at DESC LIMIT 1
	`, initiativeID, flagType).Scan(&payloadBytes)

	if err == nil {
		var payloadMap map[string]any
		if json.Unmarshal(payloadBytes, &payloadMap) == nil {
			classification, _ := payloadMap["classification"].(string)
			description, _ := payloadMap["description"].(string)
			confidence, _ := payloadMap["confidence"].(string)

			actions := originalActions
			if confidence == "low" {
				actions = []ProposalAction{}
			}
			return classification, description, actions, nil
		}
	}

	// 2. No recent event found: run the GLM-5.1 diagnosis
	rows, err := p.Query(ctx, `
		SELECT kind, source_backend, from_stage, to_stage, payload, actor, at
		FROM portfolio.initiative_event
		WHERE initiative_id = $1
		ORDER BY at DESC
		LIMIT 15
	`, initiativeID)

	var events []string
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var kind, sourceBackend string
			var fromStage, toStage, actor *string
			var payloadB []byte
			var at time.Time
			if rows.Scan(&kind, &sourceBackend, &fromStage, &toStage, &payloadB, &actor, &at) == nil {
				fromStr := ""
				if fromStage != nil {
					fromStr = *fromStage
				}
				toStr := ""
				if toStage != nil {
					toStr = *toStage
				}
				actorStr := ""
				if actor != nil {
					actorStr = *actor
				}
				payloadStr := ""
				if len(payloadB) > 0 {
					payloadStr = string(payloadB)
				}
				events = append(events, fmt.Sprintf("- [%s] Event: %s (Backend: %s, Actor: %s) From: '%s' To: '%s' Payload: %s",
					at.Format("2006-01-02 15:04:05"), kind, sourceBackend, actorStr, fromStr, toStr, payloadStr))
			}
		}
	}

	// Reverse events to be chronological (oldest to newest)
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	var promptBuilder strings.Builder
	promptBuilder.WriteString("Analyze this flagged Kanban card and diagnose the issue:\n\n")
	promptBuilder.WriteString(fmt.Sprintf("Card ID: %s\n", initiativeID))
	promptBuilder.WriteString(fmt.Sprintf("Title: %s\n", title))
	promptBuilder.WriteString(fmt.Sprintf("Description: %s\n", cardDescription))
	promptBuilder.WriteString(fmt.Sprintf("Stage: %s\n", stage))
	promptBuilder.WriteString(fmt.Sprintf("Company (Firma): %s\n", firma))
	promptBuilder.WriteString(fmt.Sprintf("Flag Type: %s\n", flagType))
	promptBuilder.WriteString(fmt.Sprintf("Original Flag Description: %s\n\n", originalDesc))

	promptBuilder.WriteString("--- BEADS & WORKSPACE SIGNALS ---\n")
	promptBuilder.WriteString(fmt.Sprintf("Total linked beads: %d\n", signal.TotalBeads))
	promptBuilder.WriteString(fmt.Sprintf("Closed beads: %d\n", signal.ClosedBeads))
	promptBuilder.WriteString(fmt.Sprintf("Active beads: %d\n", signal.ActiveBeads))
	promptBuilder.WriteString(fmt.Sprintf("Has active work (workspace running): %v\n", signal.HasActiveWork))
	promptBuilder.WriteString(fmt.Sprintf("Inactivity time: %.1f hours\n\n", signal.ActivityStaleHrs))

	promptBuilder.WriteString("--- RECENT EVENT HISTORY TIMELINE (CHRONOLOGICAL) ---\n")
	if len(events) == 0 {
		promptBuilder.WriteString("(No recent events found in database)\n")
	} else {
		for _, ev := range events {
			promptBuilder.WriteString(ev + "\n")
		}
	}

	category := ""
	justification := ""
	confidence := "low"

	key := envOr("ZAI_KEY", "")
	if key == "" {
		// Default fallback for test environment or missing key - retain high confidence to preserve actions
		fmt.Fprintln(os.Stderr, "Flow Manager: ZAI_KEY not set, using default offline fallback")
		category = getFallbackCategory(flagType)
		justification = "Automatische Diagnose-Vorschau (LLM-Verbindung nicht verfügbar)."
		confidence = "high"
	} else {
		resp, glmErr := callGlm(diagnosisSystemPrompt, []map[string]string{
			{"role": "user", "content": promptBuilder.String()},
		})

		if glmErr != nil {
			fmt.Fprintf(os.Stderr, "Flow Manager: GLM call failed for %s: %v\n", initiativeID, glmErr)
			category = getFallbackCategory(flagType)
			justification = "Automatische Diagnose-Vorschau (LLM-Verbindung fehlgeschlagen)."
			confidence = "high"
		} else {
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

			type DiagnosisResult struct {
				Category      string `json:"category"`
				Justification string `json:"justification"`
				Confidence    string `json:"confidence"`
			}
			var res DiagnosisResult
			if unmarshalErr := json.Unmarshal([]byte(cleanResp), &res); unmarshalErr != nil {
				fmt.Fprintf(os.Stderr, "Flow Manager: Failed to parse GLM JSON: %v, raw: %s\n", unmarshalErr, resp)
				category = getFallbackCategory(flagType)
				justification = "Automatische Diagnose-Vorschau (Diagnose JSON-Format fehlerhaft)."
				confidence = "high"
			} else {
				category = res.Category
				justification = res.Justification
				confidence = strings.ToLower(strings.TrimSpace(res.Confidence))

				if category != "wartet-auf-Mensch" && category != "Workspace-gescheitert" && category != "fertig-nicht-promotet" && category != "verlassen" {
					fmt.Fprintf(os.Stderr, "Flow Manager: Invalid GLM category: %s, falling back\n", category)
					category = getFallbackCategory(flagType)
				}
				if confidence != "high" && confidence != "low" {
					confidence = "low"
				}
			}
		}
	}

	classification := ""
	switch flagType {
	case "stagnation":
		classification = "Stagnation: " + category
	case "promote_ready":
		classification = "Promote-reif: " + category
	case "stale":
		classification = "Veraltet (Backlog-Fäule): " + category
	case "wip_overflow":
		classification = "WIP-Überlauf: " + category
	default:
		classification = originalClassification
	}

	description := fmt.Sprintf("%s\n\n**Diagnose-Begründung**: %s", originalDesc, justification)

	actions := originalActions
	if confidence == "low" {
		actions = []ProposalAction{}
	}

	// Insert event into portfolio.initiative_event so it's cached for 24h
	payloadMap := map[string]any{
		"type":           flagType,
		"classification": classification,
		"description":    description,
		"category":       category,
		"justification":  justification,
		"confidence":     confidence,
	}
	payloadBytes, _ = json.Marshal(payloadMap)

	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
		VALUES ($1, 'manager_flag', 'master', $2::jsonb, 'flow-manager')
	`, initiativeID, string(payloadBytes))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Flow Manager: Failed to log manager_flag event: %v\n", err)
	}

	return classification, description, actions, nil
}
