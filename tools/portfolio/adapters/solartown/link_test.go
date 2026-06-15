package main

import (
	"testing"
)

func TestSlugFromSpecID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"docs/plans/backtest-gate-prd.md", "backtest-gate"},
		{"docs/plans/my-plan.md", "my-plan"},
		{"backtest-gate-prd.md", "backtest-gate"},
		{"some-slug", "some-slug"},
		{"", ""},
	}

	for _, tc := range tests {
		actual := slugFromSpecID(tc.input)
		if actual != tc.expected {
			t.Errorf("slugFromSpecID(%q) = %q; expected %q", tc.input, actual, tc.expected)
		}
	}
}

func TestGetJoinKey(t *testing.T) {
	tests := []struct {
		specID   string
		labels   []string
		expected string
	}{
		{"docs/plans/backtest-gate-prd.md", []string{"some-other-label"}, "backtest-gate"},
		{"", []string{"some-label", "plan:my-precious-slug"}, "my-precious-slug"},
		{"docs/plans/overriding-spec-prd.md", []string{"plan:labeled-spec"}, "overriding-spec"},
		{"", []string{"no-plan-prefix"}, ""},
		{"", []string{}, ""},
	}

	for _, tc := range tests {
		actual := getJoinKey(tc.specID, tc.labels)
		if actual != tc.expected {
			t.Errorf("getJoinKey(%q, %v) = %q; expected %q", tc.specID, tc.labels, actual, tc.expected)
		}
	}
}
