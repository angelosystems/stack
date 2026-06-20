package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// IsProviderStressThrottled checks if the given provider is throttled due to 429 stress.
// This implements the Capacity Governor's Admission-Stress-Kriterium for rate limiting.
func IsProviderStressThrottled(ctx context.Context, provider string) (bool, string, error) {
	flag := strings.ToLower(os.Getenv("GOV_STRESS_429"))
	if flag != "true" && flag != "1" && flag != "on" && flag != "yes" {
		return false, "", nil
	}

	dir := os.Getenv("PORTFOLIO_PROJECTS_DIR")
	if dir == "" {
		dir = "/root/.claude/projects"
	}
	state := os.Getenv("PORTFOLIO_STATE_FILE_PATH")
	if state == "" {
		state = "/tmp/claude_parser_offsets.json"
	}

	// Governor consumes the Collector dataset (kein Doppel-Read).
	// ParseTranscriptsIncremental handles incremental reading with offsets.
	metrics, err := ParseTranscriptsIncremental(dir, state)
	if err != nil {
		return false, "", fmt.Errorf("failed to parse transcripts: %w", err)
	}

	m, exists := metrics[provider]
	if !exists {
		return false, "", nil
	}

	// Determine threshold. Default is 0.0 (any 429/overload rate throttles).
	threshold := 0.0
	if threshStr := os.Getenv("GOV_STRESS_429_THRESHOLD"); threshStr != "" {
		if val, err := strconv.ParseFloat(threshStr, 64); err == nil {
			threshold = val
		}
	}

	isThrottled := m.ErrorCount429 > 0 || m.ErrorCountOverloaded > 0 || m.ProxyCeilingRate > threshold
	if isThrottled {
		reason := fmt.Sprintf("provider %s is throttled due to 429/overload stress (rate: %.4f, errors: %d, overloads: %d)",
			provider, m.ProxyCeilingRate, m.ErrorCount429, m.ErrorCountOverloaded)
		return true, reason, nil
	}

	return false, "", nil
}
