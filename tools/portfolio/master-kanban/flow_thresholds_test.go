package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestParseThresholdDuration(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
		hasError bool
	}{
		{"3d", 3 * 24 * time.Hour, false},
		{"5 days", 5 * 24 * time.Hour, false},
		{"1day", 24 * time.Hour, false},
		{"2w", 14 * 24 * time.Hour, false},
		{"1 weeks", 7 * 24 * time.Hour, false},
		{"1.5mo", 45 * 24 * time.Hour, false}, // 1.5 * 30 days = 45 days
		{"2 months", 60 * 24 * time.Hour, false},
		{"12h", 12 * time.Hour, false},
		{"30m", 30 * time.Minute, false},
		{"", 0, true},
		{"invalid", 0, true},
	}

	for _, tc := range tests {
		actual, err := ParseThresholdDuration(tc.input)
		if tc.hasError {
			if err == nil {
				t.Errorf("expected error for input %q, but got none", tc.input)
			}
		} else {
			if err != nil {
				t.Errorf("unexpected error for input %q: %v", tc.input, err)
			}
			if actual != tc.expected {
				t.Errorf("ParseThresholdDuration(%q) = %v; expected %v", tc.input, actual, tc.expected)
			}
		}
	}
}

func TestGetStageThreshold(t *testing.T) {
	// 1. Verify default values
	t.Run("Defaults", func(t *testing.T) {
		tests := []struct {
			stage    string
			expected time.Duration
		}{
			{"now", 3 * 24 * time.Hour},
			{"soon", 14 * 24 * time.Hour},
			{"idea", 90 * 24 * time.Hour},
			{"watching", 30 * 24 * time.Hour},
			{"done", 0},
			{"unknown", 0},
		}

		for _, tc := range tests {
			actual := GetStageThreshold("anycompany", tc.stage)
			if actual != tc.expected {
				t.Errorf("GetStageThreshold(anycompany, %q) = %v; expected %v", tc.stage, actual, tc.expected)
			}
		}
	})

	// 2. Verify stage-level environment variable override
	t.Run("StageOverride", func(t *testing.T) {
		os.Setenv("PORTFOLIO_THRESHOLD_NOW", "5d")
		defer os.Unsetenv("PORTFOLIO_THRESHOLD_NOW")

		actual := GetStageThreshold("anycompany", "now")
		expected := 5 * 24 * time.Hour
		if actual != expected {
			t.Errorf("expected overridden threshold to be %v, got %v", expected, actual)
		}
	})

	// 3. Verify company-stage-level environment variable override has higher priority
	t.Run("CompanyStageOverride", func(t *testing.T) {
		os.Setenv("PORTFOLIO_THRESHOLD_NOW", "5d")
		os.Setenv("PORTFOLIO_THRESHOLD_NOW_STAYAWESOME", "12h")
		defer func() {
			os.Unsetenv("PORTFOLIO_THRESHOLD_NOW")
			os.Unsetenv("PORTFOLIO_THRESHOLD_NOW_STAYAWESOME")
		}()

		// For stayawesome, it should use the company-stage specific override
		actualSa := GetStageThreshold("stayawesome", "now")
		expectedSa := 12 * time.Hour
		if actualSa != expectedSa {
			t.Errorf("expected stayawesome specific threshold to be %v, got %v", expectedSa, actualSa)
		}

		// For another company, it should fall back to the stage-level override
		actualOther := GetStageThreshold("solartown", "now")
		expectedOther := 5 * 24 * time.Hour
		if actualOther != expectedOther {
			t.Errorf("expected other company threshold to fall back to %v, got %v", expectedOther, actualOther)
		}
	})
}

func TestFlowThresholdsAPI(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/flow-thresholds", nil)
	rr := httptest.NewRecorder()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		thresholds := map[string]map[string]string{}
		firmas := []string{"stayawesome", "solartown", "quantbot", "mariobrain", "stack", "angeloos"}
		stages := []string{"now", "soon", "idea", "watching", "done"}
		for _, f := range firmas {
			thresholds[f] = map[string]string{}
			for _, s := range stages {
				thresholds[f][s] = GetStageThreshold(f, s).String()
			}
		}
		json.NewEncoder(w).Encode(thresholds)
	})

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status OK, got %v", rr.Code)
	}

	var data map[string]map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &data); err != nil {
		t.Fatalf("failed to parse response body: %v", err)
	}

	val, ok := data["stayawesome"]["now"]
	if !ok || val != "72h0m0s" {
		t.Errorf("expected stayawesome now threshold to be 72h0m0s, got %q", val)
	}
}

func TestGetPromoteTarget(t *testing.T) {
	tests := []struct {
		name        string
		stage       string
		hasCapacity bool
		nowCount    int
		nowLimit    int
		expected    string
		hasError    bool
	}{
		// idea tests
		{"idea with capacity under limit", "idea", true, 2, 5, "now", false},
		{"idea without capacity under limit", "idea", false, 2, 5, "soon", false},
		{"idea with capacity at limit", "idea", true, 5, 5, "soon", false},
		{"idea with capacity over limit", "idea", true, 6, 5, "soon", false},
		{"IDEA uppercase with capacity", "IDEA ", true, 1, 5, "now", false},

		// soon tests
		{"soon stage", "soon", false, 0, 0, "now", false},

		// now tests
		{"now stage", "now", false, 0, 0, "watching", false},

		// watching tests
		{"watching stage", "watching", false, 0, 0, "done", false},
		{" watching with whitespace", " watching", false, 0, 0, "done", false},

		// done terminal tests
		{"done terminal", "done", false, 0, 0, "", true},

		// unknown tests
		{"unknown stage", "unknown", false, 0, 0, "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual, err := GetPromoteTarget(tc.stage, tc.hasCapacity, tc.nowCount, tc.nowLimit)
			if tc.hasError {
				if err == nil {
					t.Errorf("expected error promoting from stage %q, but got none", tc.stage)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error promoting from stage %q: %v", tc.stage, err)
				}
				if actual != tc.expected {
					t.Errorf("GetPromoteTarget(%q) = %q; expected %q", tc.stage, actual, tc.expected)
				}
			}
		})
	}
}

func TestGetPromoteTargetStage(t *testing.T) {
	ctx := context.Background()

	// 1. Test static non-idea transitions
	cases := []struct {
		current  string
		expected string
	}{
		{"soon", "now"},
		{"now", "watching"},
		{"watching", "done"},
		{"done", "done"}, // default fallback or done case
		{"invalid", "done"},
	}

	for _, tc := range cases {
		actual := GetPromoteTargetStage(ctx, nil, nil, fmt.Errorf("no db"), tc.current, "solartown", 0)
		if actual != tc.expected {
			t.Errorf("GetPromoteTargetStage(nil, nil, ..., %q, ...) = %q; expected %q", tc.current, actual, tc.expected)
		}
	}

	// 2. Test idea transition when there is no database / capacity (fallback to soon)
	actualSoon := GetPromoteTargetStage(ctx, nil, nil, fmt.Errorf("no db"), "idea", "solartown", 0)
	if actualSoon != "soon" {
		t.Errorf("expected idea with no db capacity to promote to 'soon', got %q", actualSoon)
	}

	// 3. Test idea transition with database pool (and mock stPool)
	dsn := envOr("PORTFOLIO_DSN", "postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain?sslmode=disable")
	pPool, err := pgxpool.New(ctx, dsn)
	if err == nil {
		defer pPool.Close()

		oldStPool := stPool
		stPool = pPool
		defer func() {
			stPool = oldStPool
		}()

		testAgentID := "testrig-polecat-testagent"
		_, _ = pPool.Exec(ctx, "DELETE FROM beads.labels WHERE issue_id = $1", testAgentID)
		_, _ = pPool.Exec(ctx, "DELETE FROM beads.issues WHERE id = $1", testAgentID)

		// A. Test when idle polecats > 0 (hasCapacity = true)
		_, err = pPool.Exec(ctx, `
			INSERT INTO beads.issues (id, rig, status, assignee, title, created_at, updated_at)
			VALUES ($1, 'testrig', 'open', 'unassigned', 'Agent issue', now(), now())
		`, testAgentID)
		if err == nil {
			_, _ = pPool.Exec(ctx, `
				INSERT INTO beads.labels (issue_id, rig, label, created_at)
				VALUES ($1, 'testrig', 'gt:agent', now())
			`, testAgentID)
			
			// GetPromoteTargetStage with nowCount = 0 (under limit 3) -> should be "now"
			target := GetPromoteTargetStage(ctx, pPool, pPool, nil, "idea", "solartown", 0)
			if target != "now" {
				t.Errorf("expected target stage 'now' when there is capacity, got %q", target)
			}

			// GetPromoteTargetStage with nowCount = 4 (above limit 3) -> should be "soon"
			targetAbove := GetPromoteTargetStage(ctx, pPool, pPool, nil, "idea", "solartown", 4)
			if targetAbove != "soon" {
				t.Errorf("expected target stage 'soon' when nowCount is above WIP limit, got %q", targetAbove)
			}
		}

		// Clean up
		_, _ = pPool.Exec(ctx, "DELETE FROM beads.labels WHERE issue_id = $1", testAgentID)
		_, _ = pPool.Exec(ctx, "DELETE FROM beads.issues WHERE id = $1", testAgentID)
	}
}
