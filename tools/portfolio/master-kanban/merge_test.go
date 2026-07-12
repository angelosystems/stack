package main

import (
	"strings"
	"testing"
)

// TestValidateMerge deckt die reinen (DB-freien) Verweigerungs-Pfade der
// Merge-Validierung ab — Regeln 3/4 + Poka-yoke. Die DB-tragenden Schritte
// prüft merge_integration_test.go.
func TestValidateMerge(t *testing.T) {
	cases := []struct {
		name            string
		dupID, zielID   string
		dupFirma        string
		dupArchived     bool
		zielFirma       string
		zielArchived    bool
		forceProposeAck bool
		wantErr         string // Teilstring; "" = Merge erlaubt
	}{
		{"erlaubt: zwei code-factory-Karten", "cf-dup", "cf-ziel", "code-factory", false, "code-factory", false, false, ""},
		{"dup==ziel verweigert", "cf-x", "cf-x", "code-factory", false, "code-factory", false, false, "identisch"},
		{"Ziel archiviert verweigert", "cf-dup", "cf-ziel", "code-factory", false, "code-factory", true, false, "Ziel"},
		{"Dublette archiviert verweigert", "cf-dup", "cf-ziel", "code-factory", true, "code-factory", false, false, "bereits archiviert"},
		{"quantbot als Dublette blockt", "qb-dup", "cf-ziel", "quantbot", false, "code-factory", false, false, "quantbot"},
		{"quantbot als Ziel blockt", "cf-dup", "qb-ziel", "code-factory", false, "quantbot", false, false, "quantbot"},
		{"quantbot mit force-propose-ack erlaubt", "qb-dup", "cf-ziel", "quantbot", false, "code-factory", false, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMerge(tc.dupID, tc.zielID, tc.dupFirma, tc.dupArchived, tc.zielFirma, tc.zielArchived, tc.forceProposeAck)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("erwartete Erlaubnis, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("erwartete Fehler mit %q, got %v", tc.wantErr, err)
			}
		})
	}
}
