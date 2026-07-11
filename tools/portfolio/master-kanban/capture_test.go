package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeFirma(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"stayawesome", "stayawesome"},
		{"sa", "stayawesome"},
		// firma-Alias solartown/stack -> code-factory (Rename bdd9196)
		{"Solartown", "code-factory"},
		{"st", "code-factory"},
		{"stack", "code-factory"},
		{"sk", "code-factory"},
		{"unknown", "unknown"},
	}

	for _, tc := range tests {
		actual := normalizeFirma(tc.input)
		if actual != tc.expected {
			t.Errorf("normalizeFirma(%q) = %q; expected %q", tc.input, actual, tc.expected)
		}
	}
}

func TestGuessFirmaFromCWD(t *testing.T) {
	// Fixture-Baum unter t.TempDir(): firmaFromPath wird mit den erzeugten
	// Pfaden gefüttert — cwd-unabhängig, kein Zugriff mehr aufs echte
	// Arbeitsverzeichnis.
	root := t.TempDir()
	cases := []struct {
		sub  string
		want string
	}{
		{"opt/stack/polecats/flint", "stack"},
		{"work/master-kanban/tools", "code-factory"},
		{"repos/stayawesome/app", "stayawesome"},
		{"repos/quantbot/desk", "quantbot"},
		{"repos/mariobrain/vault", "mariobrain"},
		{"repos/angeloos/scripts", "angeloos"},
		{"neutral/plain/dir", ""},
	}
	for _, tc := range cases {
		dir := filepath.Join(root, tc.sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := firmaFromPath(dir); got != tc.want {
			t.Errorf("firmaFromPath(%q) = %q; want %q", tc.sub, got, tc.want)
		}
	}
}
