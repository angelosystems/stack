package main

import (
	"os"
	"testing"
)

func TestGetWIPLimits(t *testing.T) {
	// 1. Check default values
	nowLim, soonLim := getWIPLimits("stayawesome")
	if nowLim != 3 {
		t.Errorf("expected default now limit to be 3, got %d", nowLim)
	}
	if soonLim != 5 {
		t.Errorf("expected default soon limit to be 5, got %d", soonLim)
	}

	// 2. Set environment variables
	os.Setenv("PORTFOLIO_WIP_NOW_STAYAWESOME", "4")
	os.Setenv("PORTFOLIO_WIP_SOON_STAYAWESOME", "6")
	defer func() {
		os.Unsetenv("PORTFOLIO_WIP_NOW_STAYAWESOME")
		os.Unsetenv("PORTFOLIO_WIP_SOON_STAYAWESOME")
	}()

	nowLim, soonLim = getWIPLimits("stayawesome")
	if nowLim != 4 {
		t.Errorf("expected overridden now limit to be 4, got %d", nowLim)
	}
	if soonLim != 6 {
		t.Errorf("expected overridden soon limit to be 6, got %d", soonLim)
	}
}
