package main

import (
	"testing"
)

func TestFirmaPrefix(t *testing.T) {
	expected := map[string]string{
		"stayawesome": "sa",
		"quantbot":    "qb",
		"solartown":   "st",
		"mariobrain":  "mb",
		"angeloos":    "ag",
		"stack":       "sk",
	}

	for k, v := range expected {
		if val := firmaPrefix[k]; val != v {
			t.Errorf("Expected %q to be %q, got %q", k, v, val)
		}
	}
}
