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
		SELECT id, firma, stage, title, created_at, updated_at
		FROM portfolio.initiative
		WHERE archived_at IS NULL
	`)
	if err != nil {
		return fmt.Errorf("failed to fetch initiatives: %w", err)
	}
	defer rows.Close()

	type initRow struct {
		id, firma, stage, title string
		created, updated        time.Time
	}
	var initiatives []initRow
	for rows.Next() {
		var i initRow
		if err := rows.Scan(&i.id, &i.firma, &i.stage, &i.title, &i.created, &i.updated); err == nil {
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

	// Thresholds configurable via environment variables
	stagnationThresholdNow := 48 * time.Hour
	stagnationThresholdSoon := 168 * time.Hour
	staleThresholdIdea := 720 * time.Hour // 30 days

	if val := os.Getenv("MANAGER_STAGNATION_THRESHOLD_NOW"); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			stagnationThresholdNow = d
		}
	}
	if val := os.Getenv("MANAGER_STAGNATION_THRESHOLD_SOON"); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			stagnationThresholdSoon = d
		}
	}
	if val := os.Getenv("MANAGER_STALE_THRESHOLD_IDEA"); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			staleThresholdIdea = d
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

		// Fetch bead count details if Dolt is reachable
		if spErr == nil && sp != nil {
			var beadRefs []string
			linkRows, err := p.Query(ctx, `
				SELECT ref FROM portfolio.initiative_link
				WHERE initiative_id = $1 AND kind = 'bead'
			`, init.id)
			if err == nil {
				defer linkRows.Close()
				for linkRows.Next() {
					var ref string
					if linkRows.Scan(&ref) == nil {
						beadRefs = append(beadRefs, ref)
					}
				}
			}

			if len(beadRefs) > 0 {
				signal.TotalBeads = len(beadRefs)
				// Query beads status in Dolt
				beadRows, err := sp.Query(ctx, `
					SELECT status FROM beads.issues
					WHERE id = ANY($1)
				`, beadRefs)
				if err == nil {
					defer beadRows.Close()
					for beadRows.Next() {
						var status string
						if beadRows.Scan(&status) == nil {
							if status == "closed" {
								signal.ClosedBeads++
							} else if status == "open" || status == "in_progress" || status == "hooked" {
								signal.ActiveBeads++
							}
						}
					}
				}
			}

			// Check Vibe Kanban links for running executions/workspaces
			var vkRefs []string
			vkLinkRows, err := p.Query(ctx, `
				SELECT ref FROM portfolio.initiative_link
				WHERE initiative_id = $1 AND kind = 'vk_workspace'
			`, init.id)
			if err == nil {
				defer vkLinkRows.Close()
				for vkLinkRows.Next() {
					var ref string
					if vkLinkRows.Scan(&ref) == nil {
						vkRefs = append(vkRefs, ref)
					}
				}
			}

			if len(vkRefs) > 0 {
				// Query Vibe Kanban SQLite DB for active running executions
				vkDB := envOr("VIBE_KANBAN_DB", "/root/.local/share/vibe-kanban/db.v2.sqlite")
				if _, statErr := os.Stat(vkDB); statErr == nil {
					for _, ref := range vkRefs {
						hexID := strings.ToUpper(strings.ReplaceAll(ref, "-", ""))
						query := fmt.Sprintf(`
							SELECT count(*) 
							FROM workspaces w
							JOIN sessions s ON s.workspace_id = w.id
							JOIN execution_processes ep ON ep.session_id = s.id
							WHERE hex(w.id)='%s' AND ep.status='running';
						`, hexID)
						cmd := exec.Command("sqlite3", "-readonly", vkDB, query)
						var out bytes.Buffer
						cmd.Stdout = &out
						if cmd.Run() == nil {
							var count int
							if _, scanErr := fmt.Sscanf(strings.TrimSpace(out.String()), "%d", &count); scanErr == nil && count > 0 {
								signal.HasActiveWork = true
							}
						}
					}
				}
			}
		}

		if signal.ActiveBeads > 0 {
			signal.HasActiveWork = true
		}

		// 1. Detection: Stagnation (stockend)
		// NOW threshold: 48h, SOON threshold: 168h
		isStagnant := false
		if (init.stage == "now" && signal.ActivityStaleHrs > stagnationThresholdNow.Hours() && !signal.HasActiveWork) ||
			(init.stage == "soon" && signal.ActivityStaleHrs > stagnationThresholdSoon.Hours() && !signal.HasActiveWork) {
			isStagnant = true
		}

		if isStagnant {
			// Rule-based Diagnosis
			diagnosis := "wartet-auf-Mensch"
			desc := fmt.Sprintf("Inaktivität seit %.1f Stunden in Stage '%s' ohne aktive Beads/Workspaces.", signal.ActivityStaleHrs, init.stage)

			// Log Event (manager_flag) with Cooldown (once every 24 hours per initiative & flag type)
			err := logManagerFlagWithCooldown(p, init.id, "stagnation", "Stagnation: "+diagnosis, desc)
			if err == nil {
				stagnantFlags = append(stagnantFlags, ManagerFlag{
					InitiativeID:   init.id,
					Firma:          init.firma,
					Stage:          init.stage,
					Title:          init.title,
					Type:           "stagnation",
					Classification: "Stagnation: " + diagnosis,
					Description:    desc,
					Actions: []ProposalAction{
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
					},
				})
			}
		}

		// 2. Detection: Promote-reif (promote-ready)
		// All linked beads closed, but stage is not 'done'
		if init.stage != "done" && signal.TotalBeads > 0 && signal.ClosedBeads == signal.TotalBeads {
			desc := fmt.Sprintf("Alle %d verlinkten Beads sind geschlossen, aber die Karte befindet sich noch in Stage '%s'.", signal.TotalBeads, init.stage)
			err := logManagerFlagWithCooldown(p, init.id, "promote_ready", "Promote-reif", desc)
			if err == nil {
				nowCount := wipCounts[init.firma]
				targetStage := GetPromoteTargetStage(ctx, p, sp, spErr, init.stage, init.firma, nowCount)
				promoteReadyFlags = append(promoteReadyFlags, ManagerFlag{
					InitiativeID:   init.id,
					Firma:          init.firma,
					Stage:          init.stage,
					Title:          init.title,
					Type:           "promote_ready",
					Classification: "Promote-reif",
					Description:    desc,
					Actions: []ProposalAction{
						{
							Label:    "Ein-Klick-Promote",
							Endpoint: "/api/move",
							Method:   "POST",
							Payload: map[string]any{
								"id":    init.id,
								"stage": targetStage,
							},
						},
					},
				})
			}
		}

		// 3. Detection: Backlog-Fäule / Veraltet (stale)
		// Stage is 'idea' and stale threshold exceeded
		if init.stage == "idea" && signal.ActivityStaleHrs > staleThresholdIdea.Hours() {
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
