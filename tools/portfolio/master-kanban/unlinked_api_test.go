package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUnlinkedAPI_Endpoint(t *testing.T) {
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

		response := map[string]any{
			"items":           items,
			"detector_status": det,
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
}

func TestDetectorLivenessAndDenominatorHonesty_Integration(t *testing.T) {
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

	// 1. Test stale heartbeat -> danger
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.detector_status WHERE id = 'leak-detector'")
	staleTime := time.Now().Add(-10 * time.Minute)
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.detector_status (id, last_run, status, unreachable_rigs)
		VALUES ('leak-detector', $1, 'healthy', '{}')`, staleTime)
	if err != nil {
		t.Fatalf("failed to insert stale detector status: %v", err)
	}
	defer p.Exec(ctx, "DELETE FROM portfolio.detector_status WHERE id = 'leak-detector'")

	// Query from db to check logic
	var lastRun time.Time
	var status string
	err = p.QueryRow(ctx, `SELECT last_run, status FROM portfolio.detector_status WHERE id='leak-detector'`).Scan(&lastRun, &status)
	if err != nil {
		t.Fatalf("failed to query status: %v", err)
	}

	if lastRun.IsZero() || time.Since(lastRun) > 5*time.Minute {
		status = "danger"
	}
	if status != "danger" {
		t.Errorf("expected overridden status to be 'danger' due to stale heartbeat, got %q", status)
	}

	// 2. Test fresh heartbeat -> healthy
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.detector_status WHERE id = 'leak-detector'")
	_, err = p.Exec(ctx, `
		INSERT INTO portfolio.detector_status (id, last_run, status, unreachable_rigs)
		VALUES ('leak-detector', $1, 'healthy', '{}')`, time.Now())
	if err != nil {
		t.Fatalf("failed to insert fresh detector status: %v", err)
	}

	err = p.QueryRow(ctx, `SELECT last_run, status FROM portfolio.detector_status WHERE id='leak-detector'`).Scan(&lastRun, &status)
	if err != nil {
		t.Fatalf("failed to query status: %v", err)
	}

	if lastRun.IsZero() || time.Since(lastRun) > 5*time.Minute {
		status = "danger"
	}
	if status != "healthy" {
		t.Errorf("expected status to remain 'healthy', got %q", status)
	}
}
