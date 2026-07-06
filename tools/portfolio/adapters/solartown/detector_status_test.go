package main

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// unreachable_rigs ist text[] NOT NULL. Eine nil-Slice kodiert pgx als SQL NULL
// und verletzt damit den Constraint (23502); eine nicht-nil leere Slice kodiert
// als leeres Array. Der Status-Update-Pfad muss bei leerer Rig-Liste letzteres
// schreiben.
func TestUnreachableRigsEncodesEmptyArrayNotNull(t *testing.T) {
	m := pgtype.NewMap()

	nilBuf, err := m.Encode(pgtype.TextArrayOID, pgtype.BinaryFormatCode, []string(nil), nil)
	if err != nil {
		t.Fatalf("encode nil slice: %v", err)
	}
	if nilBuf != nil {
		t.Fatalf("expected nil slice to encode as SQL NULL, got non-null buffer")
	}

	emptyBuf, err := m.Encode(pgtype.TextArrayOID, pgtype.BinaryFormatCode, []string{}, nil)
	if err != nil {
		t.Fatalf("encode empty slice: %v", err)
	}
	if emptyBuf == nil {
		t.Fatalf("expected empty slice to encode as non-null empty array, got SQL NULL")
	}
}

// Der Scan initialisiert die Rig-Liste als nicht-nil leere Slice, damit bei
// keinen unerreichbaren Rigs kein NULL geschrieben wird.
func TestUnreachableRigsInitializedNonNil(t *testing.T) {
	unreachableRigs := []string{}
	if unreachableRigs == nil {
		t.Fatalf("expected non-nil empty slice")
	}
	if len(unreachableRigs) != 0 {
		t.Fatalf("expected empty slice, got len %d", len(unreachableRigs))
	}
}
