package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCapacityHostAPI_Endpoint(t *testing.T) {
	dsn := os.Getenv("QUANTBOT_DSN")
	if dsn == "" {
		dsn = "postgres://quantbot@127.0.0.1:54330/quantbot?sslmode=disable"
	}

	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skip("skipping integration test; quantbot db not reachable:", err)
	}
	defer p.Close()

	if err := p.Ping(ctx); err != nil {
		t.Skip("skipping integration test; quantbot db ping failed:", err)
	}

	// Call the getter function directly
	capData, err := getHostCapacityGo(ctx)
	if err != nil {
		t.Fatalf("failed to call getHostCapacityGo: %v", err)
	}

	if capData == nil {
		t.Fatal("expected non-nil capData")
	}

	// Create test handler
	mux := http.NewServeMux()
	mux.HandleFunc("/api/capacity-host", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		c, err := getHostCapacityGo(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(c)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	resp, err := http.Get(server.URL + "/api/capacity-host")
	if err != nil {
		t.Fatalf("failed to send request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
	}

	var data HostCapacityGo
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify fields exist and have plausible values
	if data.CPU < 0.0 || data.CPU > 100.0 {
		t.Errorf("unexpected CPU value: %f", data.CPU)
	}
	if data.RAM < 0.0 || data.RAM > 100.0 {
		t.Errorf("unexpected RAM value: %f", data.RAM)
	}
	if data.Disk < 0.0 || data.Disk > 100.0 {
		t.Errorf("unexpected Disk value: %f", data.Disk)
	}
	if data.Headroom < 0.0 || data.Headroom > 100.0 {
		t.Errorf("unexpected Headroom value: %f", data.Headroom)
	}
	if data.GovernorVerdict == "" {
		t.Error("expected non-empty governor verdict")
	}
}
