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

type InitiativeFlow struct {
	ID           string
	Firma        string
	Stage        string
	Title        string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	LastActivity *time.Time
}

type WorkspaceState struct {
	Status   string
	Archived bool
}

type FlowDetectionResult struct {
	InitiativeID  string
	Firma         string
	Stage         string
	Title         string
	DetectionType string // "stagnation", "promote_ready", "backlog_rot", "wip_overflow"
	Reason        string
}

// cmdFlowManager implements the Kanban-Flow-Manager CLI command.
func cmdFlowManager() *cobra.Command {
	var vkDBPath string
	var writeEvents bool
	c := &cobra.Command{
		Use:   "flow-manager",
		Short: "Runs the Kanban Flow Manager to detect Stagnation, Promote-ready cards, Backlog Rot, and WIP Overflow",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := connect()
			if vkDBPath == "" {
				vkDBPath = envOr("VIBE_KANBAN_DB", "/root/.local/share/vibe-kanban/db.v2.sqlite")
			}
			return runFlowManager(p, vkDBPath, writeEvents)
		},
	}
	c.Flags().StringVar(&vkDBPath, "vk-db", "", "Path to vibe-kanban SQLite database")
	c.Flags().BoolVar(&writeEvents, "write-events", false, "Write activity events to portfolio.initiative_event")
	return c
}

// getWorkspaceStateMap queries workspace states from the vibe-kanban SQLite database.
func getWorkspaceStateMap(vkDB string) (map[string]WorkspaceState, error) {
	if _, err := os.Stat(vkDB); os.IsNotExist(err) {
		return nil, fmt.Errorf("vibe-kanban SQLite database not found at %s", vkDB)
	}

	query := `
		SELECT 
			hex(w.id),
			COALESCE(ep.status, ''),
			w.archived
		FROM workspaces w
		LEFT JOIN sessions s ON s.workspace_id = w.id
		LEFT JOIN execution_processes ep ON ep.session_id = s.id
		WHERE (ep.run_reason = 'codingagent' OR ep.run_reason IS NULL)
		ORDER BY w.created_at DESC, ep.created_at DESC;
	`
	cmd := exec.Command("sqlite3", "-readonly", "-separator", "|", vkDB, query)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to query vibe-kanban SQLite DB: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\x0a")
	states := make(map[string]WorkspaceState)

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}
		id := strings.ToLower(parts[0]) // normalize hex ID to lowercase
		status := parts[1]
		archived := parts[2] == "1"

		if _, ok := states[id]; !ok {
			states[id] = WorkspaceState{
				Status:   status,
				Archived: archived,
			}
		}
	}
	return states, nil
}

// getLinkedBeadsActive checks if any linked bead is active (i.e. status != 'closed').
// It first fetches the linked bead refs from the portfolio DB, and then queries their statuses
// from the Solartown dolt database to maintain strict database separation.
func getLinkedBeadsActive(ctx context.Context, p *pgxpool.Pool, initiativeID string) (bool, []string, error) {
	// 1. Fetch bead refs from portfolio DB
	rows, err := p.Query(ctx, `
		SELECT ref FROM portfolio.initiative_link
		WHERE initiative_id = $1 AND kind = 'bead'
	`, initiativeID)
	if err != nil {
		return false, nil, fmt.Errorf("failed to query linked bead refs: %w", err)
	}
	defer rows.Close()

	var allBeads []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return false, nil, err
		}
		allBeads = append(allBeads, ref)
	}

	if len(allBeads) == 0 {
		return false, nil, nil
	}

	// 2. Fetch statuses of these beads from Solartown Dolt DB
	sp, err := solartownPool()
	if err != nil {
		return false, nil, fmt.Errorf("failed to connect to solartown pool: %w", err)
	}

	beadRows, err := sp.Query(ctx, `
		SELECT id, COALESCE(status, 'open')
		FROM beads.issues
		WHERE id = ANY($1)
	`, allBeads)
	if err != nil {
		return false, nil, fmt.Errorf("failed to query beads statuses: %w", err)
	}
	defer beadRows.Close()

	beadStatuses := make(map[string]string)
	for beadRows.Next() {
		var id, status string
		if err := beadRows.Scan(&id, &status); err != nil {
			return false, nil, err
		}
		beadStatuses[id] = status
	}

	var activeBeads []string
	for _, b := range allBeads {
		status, ok := beadStatuses[b]
		// If not found in Dolt DB, default to "open" (active)
		if !ok {
			status = "open"
		}
		if status != "closed" {
			activeBeads = append(activeBeads, b)
		}
	}

	return len(activeBeads) > 0, allBeads, nil
}

// hasActiveWorkspace checks if any linked workspace is active (i.e. not archived and process not terminal).
func hasActiveWorkspace(ctx context.Context, p *pgxpool.Pool, initiativeID string, states map[string]WorkspaceState) (bool, []string, error) {
	rows, err := p.Query(ctx, `
		SELECT ref FROM portfolio.initiative_link 
		WHERE initiative_id = $1 AND kind = 'vk_workspace'
	`, initiativeID)
	if err != nil {
		return false, nil, err
	}
	defer rows.Close()

	var activeWorkspaces []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return false, nil, err
		}
		refNorm := strings.ToLower(ref)
		if wsState, ok := states[refNorm]; ok {
			isTerminal := wsState.Status == "completed" || wsState.Status == "failed" || wsState.Status == "killed"
			if !wsState.Archived && !isTerminal {
				activeWorkspaces = append(activeWorkspaces, ref)
			}
		}
	}
	return len(activeWorkspaces) > 0, activeWorkspaces, nil
}

// formatDuration formats duration d in a compact format like "3d" or "72h".
func formatDuration(d time.Duration) string {
	days := int(d.Hours() / 24)
	if days > 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

// printFlowReport outputs a beautifully formatted report of all detected flow anomalies.
func printFlowReport(results []FlowDetectionResult) {
	fmt.Println("=== 🩺 Kanban Flow Manager ===")
	fmt.Println("--------------------------------------------------------------------------------")
	fmt.Printf("%-15s | %-12s | %-10s | %-12s | %-40s\x0a", "ID", "Firma", "Stage", "Detection", "Reason")
	fmt.Println("--------------------------------------------------------------------------------")
	for _, res := range results {
		fmt.Printf("%-15s | %-12s | %-10s | %-12s | %s\x0a", res.InitiativeID, res.Firma, res.Stage, res.DetectionType, res.Reason)
	}
	fmt.Println("--------------------------------------------------------------------------------")
}

// runFlowManager implements the core flow detection logic for Stagnation, Promote-ready, Backlog Rot, and WIP Overflow.
func runFlowManager(p *pgxpool.Pool, vkDB string, writeEvents bool) error {
	ctx := context.Background()

	// 1. Fetch workspace states
	states, err := getWorkspaceStateMap(vkDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not load workspace states: %v\x0a", err)
		states = make(map[string]WorkspaceState)
	}

	// 2. Fetch all initiatives
	rows, err := p.Query(ctx, `
		SELECT id, firma, stage, title, created_at, updated_at, last_activity
		FROM portfolio.initiative_summary
		WHERE archived_at IS NULL
	`)
	if err != nil {
		return fmt.Errorf("failed to query initiatives: %w", err)
	}
	defer rows.Close()

	var initiatives []InitiativeFlow
	for rows.Next() {
		var init InitiativeFlow
		if err := rows.Scan(&init.ID, &init.Firma, &init.Stage, &init.Title, &init.CreatedAt, &init.UpdatedAt, &init.LastActivity); err != nil {
			return fmt.Errorf("failed to scan initiative: %w", err)
		}
		initiatives = append(initiatives, init)
	}

	var results []FlowDetectionResult

	// Group NOW stage cards per company for WIP-Überlauf
	firmaNowCounts := make(map[string][]InitiativeFlow)
	for _, init := range initiatives {
		if init.Stage == "now" {
			firmaNowCounts[init.Firma] = append(firmaNowCounts[init.Firma], init)
		}
	}

	// 3. Process each initiative for Stagnation, Promote-ready, Backlog Rot
	for _, init := range initiatives {
		lastActive := init.CreatedAt
		if init.LastActivity != nil {
			lastActive = *init.LastActivity
		}
		silence := time.Since(lastActive)

		// A. Stagnation (Stagnation)
		if init.Stage == "now" || init.Stage == "soon" {
			threshold := 72 * time.Hour // NOW stage default: 3 days
			if init.Stage == "soon" {
				threshold = 336 * time.Hour // SOON stage default: 14 days
			}

			if silence > threshold {
				hasActiveBead, _, err := getLinkedBeadsActive(ctx, p, init.ID)
				if err != nil {
					return fmt.Errorf("failed to check active beads: %w", err)
				}
				hasActiveWS, _, err := hasActiveWorkspace(ctx, p, init.ID, states)
				if err != nil {
					return fmt.Errorf("failed to check active workspaces: %w", err)
				}

				if !hasActiveBead && !hasActiveWS {
					results = append(results, FlowDetectionResult{
						InitiativeID:  init.ID,
						Firma:         init.Firma,
						Stage:         init.Stage,
						Title:         init.Title,
						DetectionType: "stagnation",
						Reason:        fmt.Sprintf("Silent for %s (threshold %s) with NO active Bead/Workspace", formatDuration(silence), formatDuration(threshold)),
					})
				}
			}
		}

		// B. Promote-ready (Promote-reif)
		if init.Stage == "idea" || init.Stage == "soon" || init.Stage == "now" {
			hasActiveBead, allBeads, err := getLinkedBeadsActive(ctx, p, init.ID)
			if err != nil {
				return fmt.Errorf("failed to check beads: %w", err)
			}
			// Promote-ready is triggered if we have at least one linked bead, and ALL linked beads are closed
			if len(allBeads) > 0 && !hasActiveBead {
				results = append(results, FlowDetectionResult{
					InitiativeID:  init.ID,
					Firma:         init.Firma,
					Stage:         init.Stage,
					Title:         init.Title,
					DetectionType: "promote_ready",
					Reason:        fmt.Sprintf("All linked beads (%d) are closed, but card stage is still %s", len(allBeads), init.Stage),
				})
			}
		}

		// C. Backlog Rot (Backlog-Fäule)
		if init.Stage == "idea" {
			threshold := 30 * 24 * time.Hour // IDEA stage default: 30 days
			if silence > threshold {
				results = append(results, FlowDetectionResult{
					InitiativeID:  init.ID,
					Firma:         init.Firma,
					Stage:         init.Stage,
					Title:         init.Title,
					DetectionType: "backlog_rot",
					Reason:        fmt.Sprintf("In IDEA stage for %s (threshold %s) without movement or activity", formatDuration(silence), formatDuration(threshold)),
				})
			}
		}
	}

	// D. WIP Overflow (WIP-Überlauf)
	for firma, cards := range firmaNowCounts {
		nowLimit, _ := getWIPLimits(firma)
		if len(cards) > nowLimit {
			for _, card := range cards {
				results = append(results, FlowDetectionResult{
					InitiativeID:  card.ID,
					Firma:         card.Firma,
					Stage:         card.Stage,
					Title:         card.Title,
					DetectionType: "wip_overflow",
					Reason:        fmt.Sprintf("WIP limit exceeded for %s: %d cards in NOW stage (limit: %d)", firma, len(cards), nowLimit),
				})
			}
		}
	}

	// Print report to standard output
	printFlowReport(results)

	// 4. Optionally write events with a 3-day cooldown per initiative/detection type
	if writeEvents {
		for _, res := range results {
			var exists bool
			err := p.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM portfolio.initiative_event
					WHERE initiative_id = $1 AND kind = 'activity'
					  AND payload->>'detection_type' = $2
					  AND at > NOW() - INTERVAL '3 days'
				)
			`, res.InitiativeID, res.DetectionType).Scan(&exists)
			if err != nil {
				return fmt.Errorf("failed to check idempotence for %s: %w", res.InitiativeID, err)
			}

			if !exists {
				payload := map[string]any{
					"detection_type": res.DetectionType,
					"reason":         res.Reason,
					"detected_at":    time.Now().Format(time.RFC3339),
				}
				payloadBytes, _ := json.Marshal(payload)

				_, err := p.Exec(ctx, `
					INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
					VALUES ($1, 'activity', 'master', $2::jsonb, 'flow-manager')
				`, res.InitiativeID, string(payloadBytes))
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error writing event for initiative %s: %v\x0a", res.InitiativeID, err)
				} else {
					fmt.Printf("✓ Emitted flow event on initiative %s: %s\x0a", res.InitiativeID, res.DetectionType)
				}
			}
		}
	}

	return nil
}
