//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLaneBadges_Enrichment(t *testing.T) {
	dsn := mkIntegrationDSN(t)

	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skip("skipping integration test; db not reachable:", err)
	}
	defer p.Close()

	if err := p.Ping(ctx); err != nil {
		t.Skip("skipping integration test; db ping failed:", err)
	}

	testInitiativeID := "init-lane-test"
	testBeadID1 := "bead-lane-test-1"
	testBeadID2 := "bead-lane-test-2"

	// 1. Clean up old test data
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitiativeID)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	sp, err := solartownPool()
	if err == nil {
		_, _ = sp.Exec(ctx, "DELETE FROM beads.labels WHERE issue_id IN ($1, $2)", testBeadID1, testBeadID2)
		_, _ = sp.Exec(ctx, "DELETE FROM beads.issues WHERE id IN ($1, $2)", testBeadID1, testBeadID2)
	}

	// 2. Create test initiative
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative (id, title, stage, stage_locked_by_human, firma, primary_backend)
		VALUES ($1, 'Lane Badge Test Initiative', 'idea', false, 'stayawesome', 'plan_file')
	`, testInitiativeID)
	if err != nil {
		t.Fatalf("failed to insert test initiative: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = $1", testInitiativeID)
	defer p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id = $1", testInitiativeID)

	// 3. Link two test beads to the initiative
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		VALUES ($1, 'bead', $2), ($1, 'bead', $3)
	`, testInitiativeID, testBeadID1, testBeadID2)
	if err != nil {
		t.Fatalf("failed to insert test links: %v", err)
	}

	// 4. Set lane labels on the beads in Dolt
	if sp != nil {
		// Insert issues first to satisfy foreign key constraint on labels
		_, err = sp.Exec(ctx, "INSERT INTO beads.issues (id, rig, title, status) VALUES ($1, 'stayawesomeOS', 'Test Issue 1', 'open')", testBeadID1)
		if err != nil {
			t.Fatalf("failed to insert issue 1: %v", err)
		}
		defer sp.Exec(ctx, "DELETE FROM beads.issues WHERE id IN ($1, $2)", testBeadID1, testBeadID2)

		_, err = sp.Exec(ctx, "INSERT INTO beads.issues (id, rig, title, status) VALUES ($1, 'stayawesomeOS', 'Test Issue 2', 'open')", testBeadID2)
		if err != nil {
			t.Fatalf("failed to insert issue 2: %v", err)
		}

		// bead 1 -> lane:hacker
		_, err = sp.Exec(ctx, "INSERT INTO beads.labels (issue_id, rig, label) VALUES ($1, 'stayawesomeOS', 'lane:hacker')", testBeadID1)
		if err != nil {
			t.Fatalf("failed to insert label for bead 1: %v", err)
		}
		defer sp.Exec(ctx, "DELETE FROM beads.labels WHERE issue_id IN ($1, $2)", testBeadID1, testBeadID2)

		// bead 2 -> lane:hacker
		_, err = sp.Exec(ctx, "INSERT INTO beads.labels (issue_id, rig, label) VALUES ($1, 'stayawesomeOS', 'lane:hacker')", testBeadID2)
		if err != nil {
			t.Fatalf("failed to insert label for bead 2: %v", err)
		}
	}

	// 5. Query /api/initiatives handler directly
	req, err := http.NewRequest("GET", "/api/initiatives", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	rr := httptest.NewRecorder()
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
			}
		}

		w.Header().Set("Content-Type", "application/json")
		if items == nil {
			items = []map[string]any{}
		}
		json.NewEncoder(w).Encode(items)
	})

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var items []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	found := false
	for _, item := range items {
		if item["id"] == testInitiativeID {
			found = true
			if item["lane"] != "hacker" {
				t.Errorf("expected lane to be 'hacker', got %q", item["lane"])
			}
		}
	}

	if !found {
		t.Errorf("test initiative %s not found in /api/initiatives response", testInitiativeID)
	}
}
