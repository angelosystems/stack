package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// GetStageThreshold returns the threshold duration for a given stage and company (firma).
// It resolves the duration in this order:
// 1. Environment variable: PORTFOLIO_THRESHOLD_<STAGE>_<FIRMA> (e.g. PORTFOLIO_THRESHOLD_NOW_STAYAWESOME)
// 2. Environment variable: PORTFOLIO_THRESHOLD_<STAGE> (e.g. PORTFOLIO_THRESHOLD_NOW)
// 3. Default conservative threshold:
//    - "now": 3 days (72h)
//    - "soon": 14 days (336h)
//    - "idea": 90 days (2160h)
//    - "watching": 30 days (720h)
//    - "done": 0 (disabled)
func GetStageThreshold(firma string, stage string) time.Duration {
	stage = strings.ToLower(stage)
	firma = strings.ToLower(firma)

	// Determine conservative defaults
	var def time.Duration
	switch stage {
	case "now":
		def = 3 * 24 * time.Hour // 3 days
		if val := os.Getenv("MANAGER_STAGNATION_THRESHOLD_NOW"); val != "" {
			if d, err := ParseThresholdDuration(val); err == nil {
				def = d
			}
		}
	case "soon":
		def = 14 * 24 * time.Hour // 14 days
		if val := os.Getenv("MANAGER_STAGNATION_THRESHOLD_SOON"); val != "" {
			if d, err := ParseThresholdDuration(val); err == nil {
				def = d
			}
		}
	case "idea":
		def = 90 * 24 * time.Hour // 90 days
		if val := os.Getenv("MANAGER_STALE_THRESHOLD_IDEA"); val != "" {
			if d, err := ParseThresholdDuration(val); err == nil {
				def = d
			}
		}
	case "watching":
		def = 30 * 24 * time.Hour // 30 days
	case "done":
		def = 0 // disabled / infinite
	default:
		def = 0
	}

	// 1. Try PORTFOLIO_THRESHOLD_<STAGE>_<FIRMA>
	envFirma := os.Getenv(fmt.Sprintf("PORTFOLIO_THRESHOLD_%s_%s", strings.ToUpper(stage), strings.ToUpper(firma)))
	if envFirma != "" {
		if d, err := ParseThresholdDuration(envFirma); err == nil {
			return d
		}
	}

	// 2. Try PORTFOLIO_THRESHOLD_<STAGE>
	envStage := os.Getenv(fmt.Sprintf("PORTFOLIO_THRESHOLD_%s", strings.ToUpper(stage)))
	if envStage != "" {
		if d, err := ParseThresholdDuration(envStage); err == nil {
			return d
		}
	}

	return def
}

// ParseThresholdDuration parses duration strings with extended support for days (d), weeks (w), and months (mo).
func ParseThresholdDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}

	// Helper to check suffix
	if strings.HasSuffix(s, "mo") {
		valStr := strings.TrimSuffix(s, "mo")
		var mo float64
		if _, err := fmt.Sscanf(valStr, "%f", &mo); err == nil {
			// Treat 1 month as 30 days
			return time.Duration(mo * 30 * 24) * time.Hour, nil
		}
	}
	if strings.HasSuffix(s, "months") {
		valStr := strings.TrimSuffix(s, "months")
		var mo float64
		if _, err := fmt.Sscanf(valStr, "%f", &mo); err == nil {
			return time.Duration(mo * 30 * 24) * time.Hour, nil
		}
	}
	if strings.HasSuffix(s, "month") {
		valStr := strings.TrimSuffix(s, "month")
		var mo float64
		if _, err := fmt.Sscanf(valStr, "%f", &mo); err == nil {
			return time.Duration(mo * 30 * 24) * time.Hour, nil
		}
	}
	if strings.HasSuffix(s, "w") {
		valStr := strings.TrimSuffix(s, "w")
		var w float64
		if _, err := fmt.Sscanf(valStr, "%f", &w); err == nil {
			return time.Duration(w * 7 * 24) * time.Hour, nil
		}
	}
	if strings.HasSuffix(s, "weeks") {
		valStr := strings.TrimSuffix(s, "weeks")
		var w float64
		if _, err := fmt.Sscanf(valStr, "%f", &w); err == nil {
			return time.Duration(w * 7 * 24) * time.Hour, nil
		}
	}
	if strings.HasSuffix(s, "week") {
		valStr := strings.TrimSuffix(s, "week")
		var w float64
		if _, err := fmt.Sscanf(valStr, "%f", &w); err == nil {
			return time.Duration(w * 7 * 24) * time.Hour, nil
		}
	}
	if strings.HasSuffix(s, "d") {
		valStr := strings.TrimSuffix(s, "d")
		var d float64
		if _, err := fmt.Sscanf(valStr, "%f", &d); err == nil {
			return time.Duration(d * 24) * time.Hour, nil
		}
	}
	if strings.HasSuffix(s, "days") {
		valStr := strings.TrimSuffix(s, "days")
		var d float64
		if _, err := fmt.Sscanf(valStr, "%f", &d); err == nil {
			return time.Duration(d * 24) * time.Hour, nil
		}
	}
	if strings.HasSuffix(s, "day") {
		valStr := strings.TrimSuffix(s, "day")
		var d float64
		if _, err := fmt.Sscanf(valStr, "%f", &d); err == nil {
			return time.Duration(d * 24) * time.Hour, nil
		}
	}

	// Default standard parse
	return time.ParseDuration(s)
}

// GetPromoteTarget returns the target stage when promoting a card from the given stage,
// taking into account capacity constraints for the 'idea' stage as specified in the non-linear Stage-Übergangs-Map (P2.4).
func GetPromoteTarget(stage string, hasCapacity bool, nowCount, nowLimit int) (string, error) {
	stage = strings.ToLower(strings.TrimSpace(stage))
	switch stage {
	case "idea":
		if hasCapacity && nowCount < nowLimit {
			return "now", nil
		}
		return "soon", nil
	case "soon":
		return "now", nil
	case "now":
		return "watching", nil
	case "watching":
		return "done", nil
	case "done":
		return "", fmt.Errorf("terminal stage %q cannot be promoted", stage)
	default:
		return "", fmt.Errorf("unknown stage %q", stage)
	}
}

