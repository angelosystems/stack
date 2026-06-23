package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIsLowerLayerEngaged(t *testing.T) {
	dsn := os.Getenv("PORTFOLIO_DSN")
	if dsn == "" {
		dsn = "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable"
	}

	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skip("skipping integration test; db not reachable:", err)
	}
	defer p.Close()

	if err := p.Ping(ctx); err != nil {
		t.Skip("skipping integration test; db ping failed:", err)
	}

	testBeadID := "bead-flow-manager-hierarchy-test"

	// Ensure clean state in lease table
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.sage_lease WHERE bead_id = $1", testBeadID)
	defer p.Exec(ctx, "DELETE FROM portfolio.sage_lease WHERE bead_id = $1", testBeadID)

	// Test Case 1: No lower layers engaged
	beads := []LinkedBead{
		{Ref: testBeadID, Status: "closed"},
	}
	workspaces := []LinkedWorkspace{
		{ID: "ws1", Status: "completed"},
	}

	engaged, reason, err := isLowerLayerEngaged(ctx, p, "init-1", beads, workspaces)
	if err != nil {
		t.Fatalf("unexpected error in Case 1: %v", err)
	}
	if engaged {
		t.Errorf("expected not engaged in Case 1, but got engaged with reason: %s", reason)
	}

	// Test Case 2: Active/waiting workspace exists
	workspacesActive := []LinkedWorkspace{
		{ID: "ws1", Status: "running"},
	}
	engaged, reason, err = isLowerLayerEngaged(ctx, p, "init-1", beads, workspacesActive)
	if err != nil {
		t.Fatalf("unexpected error in Case 2: %v", err)
	}
	if !engaged {
		t.Errorf("expected engaged in Case 2 (active workspace), but got not engaged")
	} else if !strings.Contains(reason, "active/waiting workspace exists") {
		t.Errorf("unexpected reason in Case 2: %s", reason)
	}

	// Test Case 3: Bead is not closed
	beadsActive := []LinkedBead{
		{Ref: testBeadID, Status: "open"},
	}
	engaged, reason, err = isLowerLayerEngaged(ctx, p, "init-1", beadsActive, workspaces)
	if err != nil {
		t.Fatalf("unexpected error in Case 3: %v", err)
	}
	if !engaged {
		t.Errorf("expected engaged in Case 3 (bead active), but got not engaged")
	} else if !strings.Contains(reason, "active/pending status") {
		t.Errorf("unexpected reason in Case 3: %s", reason)
	}

	// Test Case 4: Active vk-Sage lease exists
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.sage_lease (bead_id, locked_until, locked_by, heal_counter, updated_at)
		VALUES ($1, $2, 'test-runner', 1, NOW())
	`, testBeadID, time.Now().Add(5*time.Minute))
	if err != nil {
		t.Fatalf("failed to insert test lease for Case 4: %v", err)
	}

	engaged, reason, err = isLowerLayerEngaged(ctx, p, "init-1", beads, workspaces)
	if err != nil {
		t.Fatalf("unexpected error in Case 4: %v", err)
	}
	if !engaged {
		t.Errorf("expected engaged in Case 4 (active lease exists), but got not engaged")
	} else if !strings.Contains(reason, "active vk-Sage lease exists") {
		t.Errorf("unexpected reason in Case 4: %s", reason)
	}
}

func TestDiagnoseFlaggedCard(t *testing.T) {
	type TextContent struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type MockResponse struct {
		Content []TextContent `json:"content"`
	}

	var mockText string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := MockResponse{
			Content: []TextContent{
				{
					Type: "text",
					Text: mockText,
				},
			},
		}
		b, _ := json.Marshal(resp)
		w.Write(b)
	}))
	defer server.Close()

	os.Setenv("ZAI_KEY", "test-key-123")
	os.Setenv("REVIEWER_BASE_URL", server.URL)
	defer func() {
		os.Unsetenv("ZAI_KEY")
		os.Unsetenv("REVIEWER_BASE_URL")
	}()

	init := FlowInitiative{
		ID:          "init-1",
		Firma:       "stayawesome",
		Stage:       "now",
		Title:       "Test Initiative",
		Description: "A stagnant test initiative",
		CreatedAt:   time.Now().Add(-10 * 24 * time.Hour),
		UpdatedAt:   time.Now().Add(-5 * 24 * time.Hour),
	}
	beads := []LinkedBead{{Ref: "bead-1", Status: "open"}}
	workspaces := []LinkedWorkspace{{ID: "ws-1", Status: "completed"}}
	events := []FlowEvent{}
	flaggedReasons := []string{"Stagnation: 5 tage stille"}

	// Case 1: High confidence
	mockText = `{"category": "wartet-auf-Mensch", "confidence": "High", "reasoning": "Needs manual triage as it has been stale for 5 days.", "proposed_action": "ask owner for input"}`
	diag, err := diagnoseFlaggedCard(init, beads, workspaces, events, flaggedReasons)
	if err != nil {
		t.Fatalf("unexpected error in Case 1: %v", err)
	}
	if diag.Category != "wartet-auf-Mensch" {
		t.Errorf("expected Category 'wartet-auf-Mensch', got '%s'", diag.Category)
	}
	if diag.Confidence != "High" {
		t.Errorf("expected Confidence 'High', got '%s'", diag.Confidence)
	}
	if diag.ProposedAction != "ask owner for input" {
		t.Errorf("expected ProposedAction 'ask owner for input', got '%s'", diag.ProposedAction)
	}

	// Case 2: Low confidence (suppress proposed action)
	mockText = `{"category": "Workspace-gescheitert", "confidence": "Low", "reasoning": "Unclear status.", "proposed_action": "do something"}`
	diag, err = diagnoseFlaggedCard(init, beads, workspaces, events, flaggedReasons)
	if err != nil {
		t.Fatalf("unexpected error in Case 2: %v", err)
	}
	if diag.Category != "Workspace-gescheitert" {
		t.Errorf("expected Category 'Workspace-gescheitert', got '%s'", diag.Category)
	}
	if diag.Confidence != "Low" {
		t.Errorf("expected Confidence 'Low', got '%s'", diag.Confidence)
	}
	if diag.ProposedAction != "" {
		t.Errorf("expected ProposedAction to be empty for Low confidence, got '%s'", diag.ProposedAction)
	}
}

func TestFlowManager_API(t *testing.T) {
	dsn := os.Getenv("PORTFOLIO_DSN")
	if dsn == "" {
		dsn = "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable"
	}

	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skip("skipping integration test; db not reachable:", err)
	}
	defer p.Close()

	if err := p.Ping(ctx); err != nil {
		t.Skip("skipping integration test; db ping failed:", err)
	}

	testInitID := "init-flow-manager-api-test"

	// Cleanup any previous test data
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)

	// Create test initiative
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, title, stage, stage_locked_by_human, firma, primary_backend)
		VALUES ($1, 'Flow Manager API Test Initiative', 'now', false, 'stayawesome', 'plan_file')
	`, testInitID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitID)
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitID)

	// Insert flow_action event for this initiative
	payloadMap := map[string]any{
		"flagged_reasons": []string{"Stagnation: 5 tage stille, keine aktive arbeit (workspace/beads)"},
		"category":        "wartet-auf-Mensch",
		"confidence":      "High",
		"reasoning":       "Card has been stagnant for 5 days with no active beads.",
		"proposed_action": "ask owner for input",
	}
	payloadBytes, _ := json.Marshal(payloadMap)

	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor, at)
		VALUES ($1, 'flow_action', 'flow_manager', $2, 'flow-manager', now())
	`, testInitID, string(payloadBytes))
	if err != nil {
		t.Fatalf("failed to insert test flow_action event: %v", err)
	}

	// Setup the endpoint handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fetch unarchived initiatives and their latest flow_action payload (if any)
		rows, err := p.Query(r.Context(), `
			WITH latest_flow_action AS (
				SELECT DISTINCT ON (initiative_id) initiative_id, payload, at
				FROM portfolio.initiative_event
				WHERE kind = 'flow_action'
				ORDER BY initiative_id, at DESC
			)
			SELECT i.id, i.title, i.stage, i.firma, COALESCE(fa.payload::text, '{}')
			FROM portfolio.initiative i
			LEFT JOIN latest_flow_action fa ON fa.initiative_id = i.id
			WHERE i.archived_at IS NULL
			ORDER BY i.id
		`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()

		type FlaggedCard struct {
			ID             string   `json:"id"`
			Title          string   `json:"title"`
			Stage          string   `json:"stage"`
			Firma          string   `json:"firma"`
			FlaggedReasons []string `json:"flagged_reasons"`
			Category       string   `json:"category"`
			Confidence     string   `json:"confidence"`
			Reasoning      string   `json:"reasoning"`
			ProposedAction string   `json:"proposed_action"`
		}

		var flaggedCards []FlaggedCard
		stockendCount := 0
		promoteCount := 0
		veraltetCount := 0

		for rows.Next() {
			var id, title, stage, firma, payloadStr string
			if err := rows.Scan(&id, &title, &stage, &firma, &payloadStr); err == nil {
				var payload map[string]any
				if json.Unmarshal([]byte(payloadStr), &payload) == nil {
					reasonsRaw, ok := payload["flagged_reasons"].([]any)
					if ok && len(reasonsRaw) > 0 {
						var reasons []string
						for _, rVal := range reasonsRaw {
							if rStr, ok := rVal.(string); ok {
								reasons = append(reasons, rStr)
							}
						}

						category, _ := payload["category"].(string)
						confidence, _ := payload["confidence"].(string)
						reasoning, _ := payload["reasoning"].(string)
						proposedAction, _ := payload["proposed_action"].(string)

						card := FlaggedCard{
							ID:             id,
							Title:          title,
							Stage:          stage,
							Firma:          firma,
							FlaggedReasons: reasons,
							Category:       category,
							Confidence:     confidence,
							Reasoning:      reasoning,
							ProposedAction: proposedAction,
						}
						flaggedCards = append(flaggedCards, card)

						if category == "wartet-auf-Mensch" || category == "Workspace-gescheitert" {
							stockendCount++
						} else if category == "fertig-nicht-promotet" {
							promoteCount++
						} else if category == "verlassen" {
							veraltetCount++
						}
					}
				}
			}
		}

		resp := map[string]any{
			"status":        "healthy",
			"last_run":      time.Now(),
			"flagged_cards": flaggedCards,
			"summary": map[string]any{
				"stockend":     stockendCount,
				"promote_reif": promoteCount,
				"veraltet":     veraltetCount,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("failed to request manager API: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode manager API response: %v", err)
	}

	if result["status"] != "healthy" {
		t.Errorf("expected status 'healthy', got %v", result["status"])
	}

	cards, ok := result["flagged_cards"].([]any)
	if !ok {
		t.Fatalf("missing or invalid flagged_cards in response")
	}

	var foundTestCard bool
	for _, cVal := range cards {
		c, ok := cVal.(map[string]any)
		if !ok {
			continue
		}
		if c["id"] == testInitID {
			foundTestCard = true
			if c["category"] != "wartet-auf-Mensch" {
				t.Errorf("expected category 'wartet-auf-Mensch', got %v", c["category"])
			}
			if c["proposed_action"] != "ask owner for input" {
				t.Errorf("expected proposed_action 'ask owner for input', got %v", c["proposed_action"])
			}
			reasons, ok := c["flagged_reasons"].([]any)
			if !ok || len(reasons) == 0 {
				t.Errorf("expected flagged reasons to be populated")
			} else if !strings.Contains(reasons[0].(string), "Stagnation") {
				t.Errorf("expected stagnation flagged reason, got %v", reasons[0])
			}
		}
	}

	if !foundTestCard {
		t.Errorf("test initiative not found in flagged cards")
	}
}

