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

func TestFlowSignals_Enrichment(t *testing.T) {
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

	testInitiativeID := "init-flow-signals-test"
	testBeadID1 := "bead-flow-signals-test-1"

	// 1. Clean up old test data
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitiativeID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitiativeID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	sp, err := solartownPool()
	if err == nil {
		_, _ = sp.Exec(ctx, "DELETE FROM beads.issues WHERE id = $1", testBeadID1)
	}

	// 2. Create test initiative
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, title, stage, stage_locked_by_human, firma, primary_backend)
		VALUES ($1, 'Flow Signals Test Initiative', 'now', false, 'stayawesome', 'plan_file')
	`, testInitiativeID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id = $1", testInitiativeID)
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitiativeID)
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	// 3. Link a test bead to the initiative
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		VALUES ($1, 'bead', $2)
	`, testInitiativeID, testBeadID1)
	if err != nil {
		t.Fatalf("failed to insert test links: %v", err)
	}

	// 4. Set bead status in Dolt
	if sp != nil {
		_, err = sp.Exec(ctx, "INSERT INTO beads.issues (id, rig, title, status) VALUES ($1, 'stayawesomeOS', 'Test Issue 1', 'closed')", testBeadID1)
		if err != nil {
			t.Fatalf("failed to insert issue 1: %v", err)
		}
		defer sp.Exec(ctx, "DELETE FROM beads.issues WHERE id = $1", testBeadID1)
	}

	// 5. Insert stage move event to verify Zeit-in-Stage (48 hours ago)
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, from_stage, to_stage, at)
		VALUES ($1, 'moved', 'master', 'idea', 'now', $2)
	`, testInitiativeID, time.Now().Add(-48*time.Hour))
	if err != nil {
		t.Fatalf("failed to insert moved event: %v", err)
	}

	// 6. Set up the handler for /api/initiatives
	pool = p
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rows, err := p.Query(r.Context(), `SELECT row_to_json(s) FROM portfolio.initiative_summary s ORDER BY firma, stage, id`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()

		var items []map[string]any
		for rows.Next() {
			var j []byte
			if err := rows.Scan(&j); err != nil {
				continue
			}
			var item map[string]any
			if err := json.Unmarshal(j, &item); err == nil {
				items = append(items, item)
			}
		}

		// Enrich items with lane information based on linked beads
		if len(items) > 0 {
			initIDs := make([]string, 0, len(items))
			for _, item := range items {
				if id, ok := item["id"].(string); ok {
					initIDs = append(initIDs, id)
				}
			}

			initToBeads := make(map[string][]string)
			allBeadIDs := make([]string, 0)

			linkRows, err := p.Query(r.Context(), `
				SELECT initiative_id, ref 
				FROM portfolio.initiative_link 
				WHERE kind = 'bead' AND initiative_id = ANY($1)
			`, initIDs)
			if err == nil {
				defer linkRows.Close()
				for linkRows.Next() {
					var initID, beadID string
					if linkRows.Scan(&initID, &beadID) == nil {
						initToBeads[initID] = append(initToBeads[initID], beadID)
						allBeadIDs = append(allBeadIDs, beadID)
					}
				}
			}

			beadLanes := make(map[string]string)
			if len(allBeadIDs) > 0 {
				sp, err := solartownPool()
				if err == nil {
					labelRows, err := sp.Query(r.Context(), `
						SELECT issue_id, label 
						FROM beads.labels 
						WHERE label LIKE 'lane:%' AND deleted_at IS NULL AND issue_id = ANY($1)
					`, allBeadIDs)
					if err == nil {
						defer labelRows.Close()
						for labelRows.Next() {
							var issueID, label string
							if labelRows.Scan(&issueID, &label) == nil {
								laneName := strings.TrimPrefix(label, "lane:")
								beadLanes[issueID] = laneName
							}
						}
					}
				}
			}

			beadStatuses := make(map[string]string)
			if len(allBeadIDs) > 0 {
				sp, err := solartownPool()
				if err == nil {
					statusRows, err := sp.Query(r.Context(), `
						SELECT id, status 
						FROM beads.issues 
						WHERE id = ANY($1) AND deleted_at IS NULL
					`, allBeadIDs)
					if err == nil {
						defer statusRows.Close()
						for statusRows.Next() {
							var bID, bStatus string
							if statusRows.Scan(&bID, &bStatus) == nil {
								beadStatuses[bID] = bStatus
							}
						}
					}
				}
			}

			stageMoveTimes := make(map[string]time.Time)
			eventRows, err := p.Query(r.Context(), `
				SELECT initiative_id, to_stage, max(at) 
				FROM portfolio.initiative_event 
				WHERE kind = 'moved' AND initiative_id = ANY($1)
				GROUP BY initiative_id, to_stage
			`, initIDs)
			if err == nil {
				defer eventRows.Close()
				for eventRows.Next() {
					var initID, toStage string
					var at time.Time
					if eventRows.Scan(&initID, &toStage, &at) == nil {
						key := initID + ":" + toStage
						stageMoveTimes[key] = at
					}
				}
			}

			wipCounts := make(map[string]int)
			for _, item := range items {
				stage, _ := item["stage"].(string)
				firma, _ := item["firma"].(string)
				if stage == "now" {
					wipCounts[firma]++
				}
			}

			for _, item := range items {
				initID, _ := item["id"].(string)
				beads := initToBeads[initID]

				laneCounts := make(map[string]int)
				for _, beadID := range beads {
					if lane, ok := beadLanes[beadID]; ok {
						laneCounts[lane]++
					}
				}

				majorityLane := "untriagiert"
				maxCount := 0
				for lane, count := range laneCounts {
					if count > maxCount {
						maxCount = count
						majorityLane = lane
					} else if count == maxCount {
						if lane < majorityLane {
							majorityLane = lane
						}
					}
				}
				item["lane"] = majorityLane

				// Calculate the 4 flow signals
				currentStage, _ := item["stage"].(string)
				firma, _ := item["firma"].(string)
				updatedAt := parseTime(item["updated_at"])

				var entryTime time.Time
				if t, ok := stageMoveTimes[initID+":"+currentStage]; ok {
					entryTime = t
				} else {
					entryTime = updatedAt
				}
				timeInStageDays := time.Since(entryTime).Hours() / 24.0
				if timeInStageDays < 0 {
					timeInStageDays = 0
				}

				var lastActivityTime time.Time
				if item["last_activity"] != nil {
					lastActivityTime = parseTime(item["last_activity"])
				} else {
					lastActivityTime = updatedAt
				}
				activityStillnessDays := time.Since(lastActivityTime).Hours() / 24.0
				if activityStillnessDays < 0 {
					activityStillnessDays = 0
				}

				closedCount := 0
				totalCount := len(beads)
				for _, beadID := range beads {
					if status, ok := beadStatuses[beadID]; ok && status == "closed" {
						closedCount++
					}
				}

				companyWip := wipCounts[firma]
				companyLimit, _ := getWIPLimits(firma)

				item["flow_signals"] = map[string]any{
					"time_in_stage_days":      timeInStageDays,
					"activity_stillness_days": activityStillnessDays,
					"bead_progress": map[string]any{
						"closed": closedCount,
						"total":  totalCount,
					},
					"wip_vs_limit": map[string]any{
						"wip":   companyWip,
						"limit": companyLimit,
					},
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		if items == nil {
			items = []map[string]any{}
		}
		json.NewEncoder(w).Encode(items)
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	// 7. Call /api/initiatives
	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("failed to call test server: %v", err)
	}
	defer resp.Body.Close()

	var items []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	var targetItem map[string]any
	for _, item := range items {
		if item["id"] == testInitiativeID {
			targetItem = item
			break
		}
	}

	if targetItem == nil {
		t.Fatalf("test initiative not found in response list")
	}

	// 8. Validate the flow signals structure and values
	signals, ok := targetItem["flow_signals"].(map[string]any)
	if !ok {
		t.Fatalf("missing or invalid flow_signals object on enriched initiative")
	}

	timeInStage, ok := signals["time_in_stage_days"].(float64)
	if !ok || timeInStage < 1.9 || timeInStage > 2.1 {
		t.Errorf("expected time_in_stage_days to be around 2.0 (representing 48 hours), got %v", signals["time_in_stage_days"])
	}

	progress, ok := signals["bead_progress"].(map[string]any)
	if !ok {
		t.Fatalf("missing or invalid bead_progress on enriched initiative")
	}

	if progress["closed"].(float64) != 1 || progress["total"].(float64) != 1 {
		t.Errorf("expected bead progress closed=1 and total=1, got closed=%v and total=%v", progress["closed"], progress["total"])
	}

	wipLimit, ok := signals["wip_vs_limit"].(map[string]any)
	if !ok {
		t.Fatalf("missing or invalid wip_vs_limit on enriched initiative")
	}

	if wipLimit["wip"].(float64) < 1 {
		t.Errorf("expected WIP count to be at least 1, got %v", wipLimit["wip"])
	}
}
