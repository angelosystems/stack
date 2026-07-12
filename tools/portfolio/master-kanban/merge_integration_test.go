//go:build integration

package main

import (
	"context"
	"testing"
	"time"
)

// TestMergeInitiatives_Integration fährt den Merge gegen die DB: Links/Tags/
// plan_items umhängen (mit Dedup gegen Ziel-Bestand), Events auf beide Karten,
// Dublette archiviert. --dry-run persistiert nichts. Erneuter Merge auf die
// archivierte Dublette wird verweigert.
func TestMergeInitiatives_Integration(t *testing.T) {
	p := mkVollzugPool(t)
	defer p.Close()
	ctx := context.Background()

	dup, ziel := "st-merge-dup", "st-merge-ziel"
	cleanup := func() {
		for _, id := range []string{dup, ziel} {
			_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id=$1", id)
			_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id=$1", id)
			_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_tag WHERE initiative_id=$1", id)
			_, _ = p.Exec(ctx, "DELETE FROM portfolio.plan_item WHERE initiative_id=$1", id)
			_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id=$1", id)
		}
	}
	cleanup()
	defer cleanup()

	mk := func(id string) {
		if _, err := p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend)
			VALUES ($1,'solartown','now',$1,'plan_file')`, id); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	mk(dup)
	mk(ziel)
	// dup: 2 Links (bead b-neu = neu fürs Ziel; plan_file /shared.md = Ziel hat
	// ihn schon → drop), 1 Tag, 1 plan_item.
	_, _ = p.Exec(ctx, `INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		VALUES ($1,'bead','b-neu'),($1,'plan_file','/shared.md')`, dup)
	_, _ = p.Exec(ctx, `INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		VALUES ($1,'plan_file','/shared.md')`, ziel) // Kollision
	_, _ = p.Exec(ctx, `INSERT INTO portfolio.initiative_tag (initiative_id, kind, value)
		VALUES ($1,'software','x-svc')`, dup)
	_, _ = p.Exec(ctx, `INSERT INTO portfolio.plan_item (id, initiative_id, slug, layer, status)
		VALUES ($1,$2,'s','implementation','draft')`, dup+"-pi", dup)

	archivedAt := func(id string) *time.Time {
		var a *time.Time
		_ = p.QueryRow(ctx, "SELECT archived_at FROM portfolio.initiative WHERE id=$1", id).Scan(&a)
		return a
	}
	countLinks := func(id string) int {
		var n int
		_ = p.QueryRow(ctx, "SELECT count(*) FROM portfolio.initiative_link WHERE initiative_id=$1", id).Scan(&n)
		return n
	}

	// dry-run: Report korrekt, aber nichts persistiert.
	rep, err := mergeInitiatives(ctx, p, dup, ziel, "tester", true, false)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if rep.LinksMoved != 1 || rep.LinksDropped != 1 || rep.TagsMoved != 1 || rep.PlanItemsMoved != 1 || rep.Archived {
		t.Errorf("dry-run report unerwartet: %+v (want moved 1/drop 1/tags 1/pi 1/archived false)", rep)
	}
	if archivedAt(dup) != nil {
		t.Errorf("dry-run darf die Dublette NICHT archivieren")
	}
	if countLinks(dup) != 2 {
		t.Errorf("dry-run: dup-Links müssen erhalten sein (2), got %d", countLinks(dup))
	}

	// echter Merge.
	rep, err = mergeInitiatives(ctx, p, dup, ziel, "tester", false, false)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !rep.Archived {
		t.Errorf("merge muss archivieren")
	}
	if countLinks(ziel) != 2 { // bead b-neu + plan_file /shared.md (dedupliziert)
		t.Errorf("Ziel-Links erwartet 2 (bead + plan_file dedup), got %d", countLinks(ziel))
	}
	if countLinks(dup) != 0 {
		t.Errorf("dup-Links müssen umgehängt/entfernt sein, got %d", countLinks(dup))
	}
	var pi int
	_ = p.QueryRow(ctx, "SELECT count(*) FROM portfolio.plan_item WHERE initiative_id=$1", ziel).Scan(&pi)
	if pi != 1 {
		t.Errorf("plan_item muss am Ziel hängen, got %d", pi)
	}
	var mergedInto, commented int
	_ = p.QueryRow(ctx, "SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id=$1 AND kind='merged_into'", dup).Scan(&mergedInto)
	_ = p.QueryRow(ctx, "SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id=$1 AND kind='commented'", ziel).Scan(&commented)
	if mergedInto != 1 || commented != 1 {
		t.Errorf("Events: merged_into(dup)=%d commented(ziel)=%d, want 1/1", mergedInto, commented)
	}
	if archivedAt(dup) == nil {
		t.Errorf("dup muss archiviert sein")
	}

	// Poka-yoke: erneuter Merge auf die (jetzt archivierte) Dublette verweigert.
	if _, err := mergeInitiatives(ctx, p, dup, ziel, "tester", false, false); err == nil {
		t.Errorf("Merge auf archivierte Dublette muss verweigert werden")
	}
}

// TestMergeInitiatives_QuantbotGuard_Integration: quantbot-Karte nur mit
// --force-propose-ack (Regel 3, Kalibrier-Pfad).
func TestMergeInitiatives_QuantbotGuard_Integration(t *testing.T) {
	p := mkVollzugPool(t)
	defer p.Close()
	ctx := context.Background()

	dup, ziel := "qb-merge-dup", "qb-merge-ziel"
	cleanup := func() {
		for _, id := range []string{dup, ziel} {
			_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id=$1", id)
			_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id=$1", id)
		}
	}
	cleanup()
	defer cleanup()
	for _, id := range []string{dup, ziel} {
		if _, err := p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend)
			VALUES ($1,'quantbot','now',$1,'plan_file')`, id); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	if _, err := mergeInitiatives(ctx, p, dup, ziel, "tester", false, false); err == nil {
		t.Fatalf("quantbot-Merge OHNE force-propose-ack muss verweigert werden")
	}
	rep, err := mergeInitiatives(ctx, p, dup, ziel, "tester", false, true)
	if err != nil {
		t.Fatalf("quantbot-Merge MIT force-propose-ack: %v", err)
	}
	if !rep.Archived {
		t.Errorf("quantbot-Merge mit ack muss archivieren")
	}
}
