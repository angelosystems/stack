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

func TestMaskDSN(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"postgres://remote:remote@127.0.0.1:5433/solartown_clean", "postgres://***@127.0.0.1:5433/solartown_clean"},
		{"postgres://mario:secret@db:5432/mario_brain", "postgres://***@db:5432/mario_brain"},
		{"no-credentials", "no-credentials"},
		{"", ""},
	}
	for _, tc := range tests {
		actual := maskDSN(tc.input)
		if actual != tc.expected {
			t.Errorf("maskDSN(%q) = %q; expected %q", tc.input, actual, tc.expected)
		}
	}
}

func TestHexToUUID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"AA108CBA4A144387A75A9D9684BD9E78", "aa108cba-4a14-4387-a75a-9d9684bd9e78"},
		{"05021F1F765846E299B6A36B39DC39F8", "05021f1f-7658-46e2-99b6-a36b39dc39f8"},
		{"short", "short"},
	}
	for _, tc := range tests {
		actual := hexToUUID(tc.input)
		if actual != tc.expected {
			t.Errorf("hexToUUID(%q) = %q; expected %q", tc.input, actual, tc.expected)
		}
	}
}

func TestGetFirmaForRig(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"sa", "stayawesome"},
		{"st", "code-factory"},
		{"qu", "quantbot"},
		{"sk", "code-factory"},
		{"invalid", "code-factory"},
	}
	for _, tc := range tests {
		actual := getFirmaForRig(tc.input)
		if actual != tc.expected {
			t.Errorf("getFirmaForRig(%q) = %q; expected %q", tc.input, actual, tc.expected)
		}
	}
}

func TestParseWorkspaceMetadata(t *testing.T) {
	tests := []struct {
		name       string
		branch     string
		expectedRP string
		expectedF  string
	}{
		{"sol-st-yozd", "vk/0502-sol-st-yozd", "st", "code-factory"},
		{"sol-tr-vksmoke", "vk/aa10-sol-tr", "tr", "code-factory"},
		{"[tr-8et5z] some task", "bd/tr-8et5z", "tr", "code-factory"},
		{"sol-so-pgus", "vk/74e1-sol-so-pgus", "so", "stayawesome"},
		{"unknown", "unknown", "st", "code-factory"},
	}
	for _, tc := range tests {
		rp, f := parseWorkspaceMetadata(tc.name, tc.branch)
		if rp != tc.expectedRP || f != tc.expectedF {
			t.Errorf("parseWorkspaceMetadata(%q, %q) = (%q, %q); expected (%q, %q)",
				tc.name, tc.branch, rp, f, tc.expectedRP, tc.expectedF)
		}
	}
}
