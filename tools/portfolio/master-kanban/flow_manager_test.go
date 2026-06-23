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

func TestHasOpenReactorAttempt(t *testing.T) {
	// 1. Empty events
	if hasOpenReactorAttempt(nil) {
		t.Error("expected false for nil/empty events")
	}

	// 2. Terminal statuses for deployed events
	testsTerminal := []struct {
		status   string
		expected bool
	}{
		{"healthy", false},
		{"failed", false},
		{"rolled-back", false},
		{"blocked_migrations", false},
		{"", false},
		{"running", true},
		{"deploying", true},
		{"unknown_non_terminal", true},
	}

	for _, tc := range testsTerminal {
		payload := `{"status":"` + tc.status + `"}`
		events := []FlowEvent{
			{
				Kind:    "deployed",
				Payload: payload,
				At:      time.Now(),
			},
		}
		res := hasOpenReactorAttempt(events)
		if res != tc.expected {
			web_status := tc.status
			t.Errorf("status %q: expected %v, got %v", web_status, tc.expected, res)
		}
	}

	// 3. Dispatched recently with no newer activity -> open reactor attempt
	eventsRecentDispatch := []FlowEvent{
		{
			Kind: "dispatched",
			At:   time.Now().Add(-5 * time.Minute),
		},
	}
	if !hasOpenReactorAttempt(eventsRecentDispatch) {
		t.Error("expected true for recent dispatch with no newer events")
	}

	// 4. Dispatched recently but with newer terminal deployment -> no open reactor attempt
	eventsDispatchWithNewerDeploy := []FlowEvent{
		{
			Kind: "dispatched",
			At:   time.Now().Add(-10 * time.Minute),
		},
		{
			Kind:    "deployed",
			Payload: `{"status":"healthy"}`,
			At:      time.Now().Add(-5 * time.Minute),
		},
	}
	if hasOpenReactorAttempt(eventsDispatchWithNewerDeploy) {
		t.Error("expected false for recent dispatch followed by healthy deploy")
	}

	// 5. Dispatched recently but with newer workspace started -> no open reactor attempt
	eventsDispatchWithNewerWorkspace := []FlowEvent{
		{
			Kind: "dispatched",
			At:   time.Now().Add(-10 * time.Minute),
		},
		{
			Kind: "workspace_started",
			At:   time.Now().Add(-5 * time.Minute),
		},
	}
	if hasOpenReactorAttempt(eventsDispatchWithNewerWorkspace) {
		t.Error("expected false for recent dispatch followed by workspace_started")
	}

	// 6. Dispatched more than 15 minutes ago with no newer activity -> no open reactor attempt (not "recently")
	eventsOldDispatch := []FlowEvent{
		{
			Kind: "dispatched",
			At:   time.Now().Add(-20 * time.Minute),
		},
	}
	if hasOpenReactorAttempt(eventsOldDispatch) {
		t.Error("expected false for dispatched older than 15 minutes with no newer events")
	}
}

func TestDiagnoseFlaggedCard(t *testing.T) {
	// 1. Setup mock GLM server
	var mockGLMResponse string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Anthropic-compatible JSON envelope
		resp := map[string]any{
			"content": []map[string]string{
				{
					"type": "text",
					"text": mockGLMResponse,
				},
			},
		}
		b, _ := json.Marshal(resp)
		w.Write(b)
	}))
	defer server.Close()

	// Override environment variables for callGlm
	os.Setenv("ZAI_KEY", "test-key-abc")
	os.Setenv("REVIEWER_BASE_URL", server.URL)
	defer func() {
		os.Unsetenv("ZAI_KEY")
		os.Unsetenv("REVIEWER_BASE_URL")
	}()

	init := FlowInitiative{
		ID:          "st-test-card",
		Firma:       "stayawesome",
		Stage:       "now",
		Title:       "Test Card",
		Description: "Just a test card",
		CreatedAt:   time.Now().Add(-10 * 24 * time.Hour),
		UpdatedAt:   time.Now().Add(-5 * 24 * time.Hour),
	}

	beads := []LinkedBead{
		{Ref: "st-123", Status: "closed"},
	}
	workspaces := []LinkedWorkspace{
		{ID: "WS1", Status: "failed"},
	}
	events := []FlowEvent{
		{Kind: "dispatched", Actor: "reactor", At: time.Now().Add(-4 * 24 * time.Hour)},
	}
	flaggedReasons := []string{"Stagnation: 5 tage stille, keine aktive arbeit"}

	// Test Case A: High Confidence
	mockGLMResponse = `{
		"category": "Workspace-gescheitert",
		"confidence": "High",
		"reasoning": "The verlinked workspace WS1 has failed, and the card has been stale for 5 days.",
		"proposed_action": "re-dispatch bead st-123"
	}`

	diagnosis, err := diagnoseFlaggedCard(init, beads, workspaces, events, flaggedReasons)
	if err != nil {
		t.Fatalf("diagnoseFlaggedCard failed: %v", err)
	}

	if diagnosis.Category != "Workspace-gescheitert" {
		t.Errorf("expected Category Workspace-gescheitert, got %q", diagnosis.Category)
	}
	if diagnosis.Confidence != "High" {
		t.Errorf("expected Confidence High, got %q", diagnosis.Confidence)
	}
	if diagnosis.ProposedAction != "re-dispatch bead st-123" {
		t.Errorf("expected ProposedAction 're-dispatch bead st-123', got %q", diagnosis.ProposedAction)
	}

	// Test Case B: Low Confidence (should empty ProposedAction)
	mockGLMResponse = `{
		"category": "wartet-auf-Mensch",
		"confidence": "Low",
		"reasoning": "Unclear why it is stagnant. Needs human review.",
		"proposed_action": "manual review"
	}`

	diagnosis, err = diagnoseFlaggedCard(init, beads, workspaces, events, flaggedReasons)
	if err != nil {
		t.Fatalf("diagnoseFlaggedCard failed: %v", err)
	}

	if diagnosis.Category != "wartet-auf-Mensch" {
		t.Errorf("expected Category wartet-auf-Mensch, got %q", diagnosis.Category)
	}
	if diagnosis.Confidence != "Low" {
		t.Errorf("expected Confidence Low, got %q", diagnosis.Confidence)
	}
	if diagnosis.ProposedAction != "" {
		t.Errorf("expected ProposedAction to be emptied for Low confidence, got %q", diagnosis.ProposedAction)
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
