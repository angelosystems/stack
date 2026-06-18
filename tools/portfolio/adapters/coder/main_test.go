package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestEventKindFor(t *testing.T) {
	cases := map[string]string{
		"workspace.started": "activity",
		"workspace.stopped": "activity",
		"workspace.deleted": "activity",
		"workspace.created": "activity",
		"workspace.updated": "activity",
		"unrelated.event":   "",
		"":                  "",
	}
	for in, want := range cases {
		if got := eventKindFor(in); got != want {
			t.Errorf("eventKindFor(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCoderAdapterRoleIsScopedToPortfolio: Integration-Test, gated über
// CODER_ADAPTER_TEST_DSN. Verifiziert, dass die Rolle:
//   - in portfolio.initiative_event INSERTen darf,
//   - außerhalb dessen Schreibversuche mit „permission denied" scheitern.
//
// Erwartet ein voll migriertes mario_brain (portfolio-001 .. -005). Login
// als coder_adapter (CREATE ROLE coder_adapter LOGIN PASSWORD ...). Beispiel:
//
//	CODER_ADAPTER_TEST_DSN='postgres://coder_adapter:pw@127.0.0.1:5434/mario_brain?sslmode=disable' \
//	  go test ./adapters/coder/...
func TestCoderAdapterRoleIsScopedToPortfolio(t *testing.T) {
	tdsn := os.Getenv("CODER_ADAPTER_TEST_DSN")
	if tdsn == "" {
		t.Skip("CODER_ADAPTER_TEST_DSN nicht gesetzt — Integration-Test übersprungen")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, tdsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	// 0) Sanity: wir sind als coder_adapter verbunden.
	var who string
	if err := conn.QueryRow(ctx, "SELECT current_user").Scan(&who); err != nil {
		t.Fatalf("current_user: %v", err)
	}
	if who != "coder_adapter" {
		t.Fatalf("current_user = %q, want coder_adapter", who)
	}

	// 1) Erlaubt: INSERT in portfolio.initiative_event (gegen vorhandene Initiative).
	var initiativeID string
	if err := conn.QueryRow(ctx,
		`SELECT id FROM portfolio.initiative LIMIT 1`).Scan(&initiativeID); err != nil {
		t.Fatalf("kein initiative-row zum Testen: %v", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) // wir wollen den Test-Insert nicht behalten
	if _, err := tx.Exec(ctx,
		`INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
		 VALUES ($1, 'activity', 'coder', '{"test":true}'::jsonb, 'test')`,
		initiativeID); err != nil {
		t.Fatalf("INSERT initiative_event sollte erlaubt sein: %v", err)
	}

	// 2) Verboten: UPDATE auf portfolio.initiative.
	_, err = tx.Exec(ctx, `UPDATE portfolio.initiative SET title=title WHERE id=$1`, initiativeID)
	if !isPermissionDenied(err) {
		t.Fatalf("UPDATE portfolio.initiative sollte denied sein, got: %v", err)
	}

	// 3) Verboten: INSERT in portfolio.initiative_link.
	_, err = tx.Exec(ctx,
		`INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		 VALUES ($1, 'coder_workspace', 'should-not-insert')`, initiativeID)
	if !isPermissionDenied(err) {
		t.Fatalf("INSERT portfolio.initiative_link sollte denied sein, got: %v", err)
	}

	// 4) Verboten: irgendetwas außerhalb des portfolio-Schemas. pg_catalog ist
	// system-readable, ein Schreibversuch dort scheitert für jeden Nicht-Superuser
	// — also nehmen wir einen sicheren Probe-Versuch in information_schema, der
	// für coder_adapter nicht erlaubt sein darf.
	_, err = tx.Exec(ctx, `CREATE TABLE public.coder_adapter_probe (x int)`)
	if !isPermissionDenied(err) {
		t.Fatalf("CREATE TABLE public.* sollte denied sein, got: %v", err)
	}
}

func isPermissionDenied(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "permission denied") || strings.Contains(msg, "must be owner")
}
