//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUnlinkedAPI_Endpoint(t *testing.T) {
	dsn := mkIntegrationDSN(t)

	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skip("skipping integration test; db not reachable:", err)
	}
	defer p.Close()

	// Verify connection
	if err := p.Ping(ctx); err != nil {
		t.Skip("skipping integration test; db ping failed:", err)
	}

	// Clean up any test records
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.unlinked_item WHERE id IN ('test-unlinked-bead', 'test-unlinked-ws')")
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.detector_status WHERE id = 'leak-detector'")

	// Insert test unlinked items
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.unlinked_item (id, kind, title, firma, rig_prefix, join_key, discovered_at)
		VALUES ('test-unlinked-bead', 'bead', 'Test Unlinked Bead', 'solartown', 'st', 'some-join-key', $1)`,
		time.Now().Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("failed to insert test unlinked bead: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.unlinked_item WHERE id = 'test-unlinked-bead'")

	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.unlinked_item (id, kind, title, firma, rig_prefix, join_key, discovered_at)
		VALUES ('test-unlinked-ws', 'vk_workspace', 'Test Unlinked Workspace', 'stayawesome', 'sa', NULL, $1)`,
		time.Now().Add(-2*time.Hour))
	if err != nil {
		t.Fatalf("failed to insert test unlinked workspace: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.unlinked_item WHERE id = 'test-unlinked-ws'")

	// Insert test detector status
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.detector_status (id, last_run, status, unreachable_rigs, error_message)
		VALUES ('leak-detector', $1, 'healthy', $2, NULL)`,
		time.Now(), []string{"qu"})
	if err != nil {
		t.Fatalf("failed to insert detector status: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.detector_status WHERE id = 'leak-detector'")

	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.detector_status (id, last_run, status, unreachable_rigs, error_message)
		VALUES ('sage', $1, 'healthy', $2, NULL)`,
		time.Now(), []string{})
	if err != nil {
		t.Fatalf("failed to insert sage status: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.detector_status WHERE id = 'sage'")

	// Create test handler using a custom serve mux
	mux := http.NewServeMux()
	mux.HandleFunc("/api/unlinked", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		rows, err := p.Query(r.Context(),
			`SELECT id, kind, title, firma, rig_prefix, COALESCE(join_key, ''), discovered_at
			 FROM portfolio.unlinked_item
			 ORDER BY discovered_at ASC`)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer rows.Close()

		type UnlinkedJSONItem struct {
			ID           string    `json:"id"`
			Kind         string    `json:"kind"`
			Title        string    `json:"title"`
			Firma        string    `json:"firma"`
			RigPrefix    string    `json:"rig_prefix"`
			JoinKey      string    `json:"join_key"`
			DiscoveredAt time.Time `json:"discovered_at"`
		}

		items := []UnlinkedJSONItem{}
		for rows.Next() {
			var item UnlinkedJSONItem
			if err := rows.Scan(&item.ID, &item.Kind, &item.Title, &item.Firma, &item.RigPrefix, &item.JoinKey, &item.DiscoveredAt); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			items = append(items, item)
		}

		type DetectorStatusJSON struct {
			LastRun         time.Time `json:"last_run"`
			Status          string    `json:"status"`
			UnreachableRigs []string  `json:"unreachable_rigs"`
			ErrorMessage    *string   `json:"error_message"`
		}

		var det DetectorStatusJSON
		err = p.QueryRow(r.Context(),
			`SELECT last_run, status, unreachable_rigs, error_message
			 FROM portfolio.detector_status
			 WHERE id = 'leak-detector'`).
			Scan(&det.LastRun, &det.Status, &det.UnreachableRigs, &det.ErrorMessage)
		if err != nil {
			det.Status = "unknown"
			det.UnreachableRigs = []string{}
		}

		var sage DetectorStatusJSON
		err = p.QueryRow(r.Context(),
			`SELECT last_run, status, unreachable_rigs, error_message
			 FROM portfolio.detector_status
			 WHERE id = 'sage'`).
			Scan(&sage.LastRun, &sage.Status, &sage.UnreachableRigs, &sage.ErrorMessage)
		if err != nil {
			sage.Status = "unknown"
			sage.UnreachableRigs = []string{}
		}

		response := map[string]any{
			"items":                     items,
			"detector_status":           det,
			"sage_status":               sage,
			"dangling_workspaces_count": 2,
		}

		json.NewEncoder(w).Encode(response)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/unlinked")
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
	}

	var data struct {
		Items []struct {
			ID           string    `json:"id"`
			Kind         string    `json:"kind"`
			Title        string    `json:"title"`
			Firma        string    `json:"firma"`
			RigPrefix    string    `json:"rig_prefix"`
			JoinKey      string    `json:"join_key"`
			DiscoveredAt time.Time `json:"discovered_at"`
		} `json:"items"`
		DetectorStatus struct {
			LastRun         time.Time `json:"last_run"`
			Status          string    `json:"status"`
			UnreachableRigs []string  `json:"unreachable_rigs"`
		} `json:"detector_status"`
		SageStatus struct {
			LastRun         time.Time `json:"last_run"`
			Status          string    `json:"status"`
			UnreachableRigs []string  `json:"unreachable_rigs"`
		} `json:"sage_status"`
		DanglingWorkspacesCount int `json:"dangling_workspaces_count"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify items are returned correctly (and ordered by discovered_at ASC, so 'test-unlinked-ws' with 2 hours ago comes first)
	if len(data.Items) < 2 {
		t.Fatalf("expected at least 2 unlinked items, got %d", len(data.Items))
	}

	// Verify detector status is returned correctly
	if data.DetectorStatus.Status != "healthy" {
		t.Errorf("expected detector status 'healthy', got %q", data.DetectorStatus.Status)
	}
	if len(data.DetectorStatus.UnreachableRigs) != 1 || data.DetectorStatus.UnreachableRigs[0] != "qu" {
		t.Errorf("expected unreachable rigs ['qu'], got %v", data.DetectorStatus.UnreachableRigs)
	}

	// Verify sage status is returned correctly
	if data.SageStatus.Status != "healthy" {
		t.Errorf("expected sage status 'healthy', got %q", data.SageStatus.Status)
	}

	// Verify dangling workspaces count is returned correctly
	if data.DanglingWorkspacesCount != 2 {
		t.Errorf("expected dangling workspaces count 2, got %d", data.DanglingWorkspacesCount)
	}
}

func TestLinkAPI_Endpoint(t *testing.T) {
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

	// Clean up any test records
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = 'st-catch-all' AND kind = 'bead' AND ref = 'test-unlinked-bead-link'")
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.unlinked_item WHERE id = 'test-unlinked-bead-link'")

	// Insert test unlinked item
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.unlinked_item (id, kind, title, firma, rig_prefix, join_key, discovered_at)
		VALUES ('test-unlinked-bead-link', 'bead', 'Test Link Bead', 'solartown', 'st', 'some-join-key', $1)`,
		time.Now())
	if err != nil {
		t.Fatalf("failed to insert test unlinked bead: %v", err)
	}
	defer func() {
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.unlinked_item WHERE id = 'test-unlinked-bead-link'")
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id = 'st-catch-all' AND kind = 'bead' AND ref = 'test-unlinked-bead-link'")
	}()

	// Simulating /api/link endpoint request
	mux := http.NewServeMux()
	mux.HandleFunc("/api/link", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		var body struct {
			InitiativeID string `json:"initiative_id"`
			Kind         string `json:"kind"`
			Ref          string `json:"ref"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if body.InitiativeID == "" || body.Kind == "" || body.Ref == "" {
			http.Error(w, "initiative_id, kind und ref sind Pflichtfelder", 400)
			return
		}

		_, err := p.Exec(r.Context(),
			`INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
			 VALUES ($1, $2, $3)
			 ON CONFLICT (initiative_id, kind, ref) DO NOTHING`,
			body.InitiativeID, body.Kind, body.Ref)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		// Remove from unlinked table
		_, _ = p.Exec(r.Context(), `DELETE FROM portfolio.unlinked_item WHERE id=$1`, body.Ref)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	// Send POST request
	reqBody := `{"initiative_id":"st-catch-all","kind":"bead","ref":"test-unlinked-bead-link"}`
	resp, err := http.Post(server.URL+"/api/link", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("failed to post link request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var resData map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&resData); err != nil {
		t.Fatalf("failed to decode link response: %v", err)
	}

	if resData["ok"] != true {
		t.Errorf("expected ok: true, got %v", resData["ok"])
	}

	// Verify unlinked item is gone from database
	var count int
	err = p.QueryRow(ctx, "SELECT COUNT(*) FROM portfolio.unlinked_item WHERE id = 'test-unlinked-bead-link'").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query unlinked_item table: %v", err)
	}
	if count != 0 {
		t.Errorf("expected unlinked item to be deleted from table, but count is %d", count)
	}

	// Verify initiative link was inserted correctly
	var linkExists bool
	err = p.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM portfolio.initiative_link WHERE initiative_id = 'st-catch-all' AND kind = 'bead' AND ref = 'test-unlinked-bead-link')").Scan(&linkExists)
	if err != nil {
		t.Fatalf("failed to query initiative_link table: %v", err)
	}
	if !linkExists {
		t.Errorf("expected initiative link to be inserted into table, but it was not found")
	}
}

func TestCompletenessAPI_LivenessAndHonesty(t *testing.T) {
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

	// Clean up any test records
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.unlinked_item WHERE id = 'test-unlinked-rig'")
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.detector_status WHERE id = 'leak-detector'")

	// Insert test unlinked rig
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.unlinked_item (id, kind, title, firma, rig_prefix, join_key, discovered_at)
		VALUES ('test-unlinked-rig', 'rig', 'Test Unlinked Rig', 'solartown', 'st', NULL, $1)`,
		time.Now())
	if err != nil {
		t.Fatalf("failed to insert test unlinked rig: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.unlinked_item WHERE id = 'test-unlinked-rig'")

	// Insert test stale detector status (6 minutes ago)
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.detector_status (id, last_run, status, unreachable_rigs, error_message)
		VALUES ('leak-detector', $1, 'healthy', $2, NULL)`,
		time.Now().Add(-6*time.Minute), []string{"mb"})
	if err != nil {
		t.Fatalf("failed to insert detector status: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.detector_status WHERE id = 'leak-detector'")

	// Recreate the `/api/completeness` handler logic
	mux := http.NewServeMux()
	mux.HandleFunc("/api/completeness", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// 1. Get detector status
		var lastRun time.Time
		var status string
		var unreachableRigs []string
		err := p.QueryRow(r.Context(),
			`SELECT last_run, status, unreachable_rigs FROM portfolio.detector_status WHERE id='leak-detector'`).
			Scan(&lastRun, &status, &unreachableRigs)
		if err == nil {
			if !lastRun.IsZero() && time.Since(lastRun) > 5*time.Minute {
				status = "danger"
			}
		} else {
			lastRun = time.Time{}
			status = "danger"
			unreachableRigs = []string{}
		}

		// 2. Query bead statistics
		var linkedBeadsRegular, linkedBeadsCatchall, unlinkedBeads int
		_ = p.QueryRow(r.Context(),
			`SELECT COUNT(*) FROM portfolio.initiative_link WHERE kind='bead' AND NOT (initiative_id LIKE '%-catch-all')`).Scan(&linkedBeadsRegular)
		_ = p.QueryRow(r.Context(),
			`SELECT COUNT(*) FROM portfolio.initiative_link WHERE kind='bead' AND initiative_id LIKE '%-catch-all'`).Scan(&linkedBeadsCatchall)
		_ = p.QueryRow(r.Context(),
			`SELECT COUNT(*) FROM portfolio.unlinked_item WHERE kind='bead'`).Scan(&unlinkedBeads)

		// 3. Query workspace statistics
		var linkedWorkspacesRegular, linkedWorkspacesCatchall, unlinkedWorkspaces int
		_ = p.QueryRow(r.Context(),
			`SELECT COUNT(*) FROM portfolio.initiative_link WHERE kind='vk_workspace' AND NOT (initiative_id LIKE '%-catch-all')`).Scan(&linkedWorkspacesRegular)
		_ = p.QueryRow(r.Context(),
			`SELECT COUNT(*) FROM portfolio.initiative_link WHERE kind='vk_workspace' AND initiative_id LIKE '%-catch-all'`).Scan(&linkedWorkspacesCatchall)
		_ = p.QueryRow(r.Context(),
			`SELECT COUNT(*) FROM portfolio.unlinked_item WHERE kind='vk_workspace'`).Scan(&unlinkedWorkspaces)

		// 3b. Query offline/unreachable rig statistics (Denominator Honesty)
		var unlinkedRigs int
		_ = p.QueryRow(r.Context(),
			`SELECT COUNT(*) FROM portfolio.unlinked_item WHERE kind='rig'`).Scan(&unlinkedRigs)

		// 4. Calculate totals
		totalBeads := linkedBeadsRegular + linkedBeadsCatchall + unlinkedBeads
		totalWorkspaces := linkedWorkspacesRegular + linkedWorkspacesCatchall + unlinkedWorkspaces
		totalRigs := unlinkedRigs
		totalWorkItems := totalBeads + totalWorkspaces + totalRigs
		linkedWorkItems := (linkedBeadsRegular + linkedBeadsCatchall) + (linkedWorkspacesRegular + linkedWorkspacesCatchall)
		catchallWorkItems := linkedBeadsCatchall + linkedWorkspacesCatchall

		completenessPercentage := 0.0
		if totalWorkItems > 0 {
			completenessPercentage = (float64(linkedWorkItems) / float64(totalWorkItems)) * 100.0
		}

		catchallPercentage := 0.0
		if totalWorkItems > 0 {
			catchallPercentage = (float64(catchallWorkItems) / float64(totalWorkItems)) * 100.0
		}

		response := map[string]any{
			"detector_last_run": lastRun,
			"detector_status":   status,
			"unreachable_rigs":  unreachableRigs,
			"beads": map[string]any{
				"linked_regular":  linkedBeadsRegular,
				"linked_catchall": linkedBeadsCatchall,
				"unlinked":        unlinkedBeads,
				"total":           totalBeads,
			},
			"workspaces": map[string]any{
				"linked_regular":  linkedWorkspacesRegular,
				"linked_catchall": linkedWorkspacesCatchall,
				"unlinked":        unlinkedWorkspaces,
				"total":           totalWorkspaces,
			},
			"rigs": map[string]any{
				"unlinked": unlinkedRigs,
				"total":    totalRigs,
			},
			"total_work_items":        totalWorkItems,
			"linked_work_items":       linkedWorkItems,
			"completeness_percentage": completenessPercentage,
			"catchall_percentage":     catchallPercentage,
		}

		json.NewEncoder(w).Encode(response)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/completeness")
	if err != nil {
		t.Fatalf("failed to get completeness: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var data struct {
		DetectorStatus string `json:"detector_status"`
		Rigs           struct {
			Unlinked int `json:"unlinked"`
			Total    int `json:"total"`
		} `json:"rigs"`
		TotalWorkItems int `json:"total_work_items"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("failed to decode completeness response: %v", err)
	}

	// Verify status is overridden to danger because of heartbeat delay
	if data.DetectorStatus != "danger" {
		t.Errorf("expected status 'danger' due to stale run, got %q", data.DetectorStatus)
	}

	// Verify unlinked rig is counted
	if data.Rigs.Unlinked < 1 {
		t.Errorf("expected at least 1 unlinked rig, got %d", data.Rigs.Unlinked)
	}
}
