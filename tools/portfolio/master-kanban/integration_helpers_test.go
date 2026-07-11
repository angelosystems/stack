//go:build integration

package main

import (
	"os"
	"testing"
)

// mkIntegrationDSN liefert die Board-DSN der Integrations-Schicht aus
// MK_INTEGRATION_DSN. Kein Fallback auf eine Prod-DSN — wer mit
// -tags integration baut, will Integration und muss eine ephemere Test-DB
// stellen (nightly-Timer). Niemals gegen das Live-Board 127.0.0.1:5434 laufen.
func mkIntegrationDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("MK_INTEGRATION_DSN")
	if dsn == "" {
		t.Fatal("MK_INTEGRATION_DSN fehlt — Integrations-Schicht braucht eine ephemere Test-DB; niemals gegen das Live-Board 127.0.0.1:5434 laufen lassen")
	}
	return dsn
}

// mkSecondaryDSN liefert eine Neben-DB-DSN (beads/solartown) aus dem Env.
// Fehlt sie, wird der Test übersprungen — die Neben-DB ist nicht überall da.
func mkSecondaryDSN(t *testing.T, env string) string {
	t.Helper()
	dsn := os.Getenv(env)
	if dsn == "" {
		t.Skipf("%s nicht gesetzt — Neben-DB (beads/solartown) fehlt, überspringe", env)
	}
	return dsn
}
