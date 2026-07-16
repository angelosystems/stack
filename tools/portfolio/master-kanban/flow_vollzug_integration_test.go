//go:build integration

package main

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// mkVollzugPool öffnet die Integrations-DB (nie das Live-Board 5434) oder
// überspringt, wenn sie nicht erreichbar ist.
func mkVollzugPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := mkIntegrationDSN(t)
	ctx := context.Background()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skip("skipping integration test; db not reachable:", err)
	}
	if err := p.Ping(ctx); err != nil {
		p.Close()
		t.Skip("skipping integration test; db ping failed:", err)
	}
	return p
}

// TestApplyStageProposal_Integration fährt die vier Regel-Pfade der gemeinsamen
// Vollzugs-Funktion gegen die DB: vorwärts bewegt, rückwärts blockt, locked
// blockt, Not-Aus blockt (Event trotzdem geschrieben).
func TestApplyStageProposal_Integration(t *testing.T) {
	p := mkVollzugPool(t)
	defer p.Close()
	ctx := context.Background()

	id := "st-vollzug-apply-test"
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id=$1", id)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id=$1", id)
	_, err := p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, stage_locked_by_human, primary_backend)
		VALUES ($1,'code-factory','now','Vollzug Apply Test',false,'plan_file')`, id)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	defer func() {
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id=$1", id)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id=$1", id)
	}()

	stageOf := func() string {
		var s string
		_ = p.QueryRow(ctx, "SELECT stage FROM portfolio.initiative WHERE id=$1", id).Scan(&s)
		return s
	}
	eventCount := func() int {
		var n int
		_ = p.QueryRow(ctx, "SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id=$1 AND kind='stage_proposed'", id).Scan(&n)
		return n
	}

	// 1. vorwärts bewegt: now→watching.
	moved, reason, err := applyStageProposal(ctx, p, id, "watching", map[string]any{"reason": "test"}, "flow-manager", false)
	if err != nil {
		t.Fatalf("apply forward: %v", err)
	}
	if !moved || reason != "moved" || stageOf() != "watching" {
		t.Errorf("forward: moved=%v reason=%q stage=%q, want moved=true/moved/watching", moved, reason, stageOf())
	}
	if eventCount() != 1 {
		t.Errorf("forward: erwartet 1 stage_proposed-Event, got %d", eventCount())
	}
	// Vollzug hinterlässt ein moved-Event mit from/to (WP1 Haertung):
	// Drawer-Historie + last_activity zählen den Auto-Move.
	var movedFrom, movedTo string
	if err := p.QueryRow(ctx, `SELECT COALESCE(from_stage,''), COALESCE(to_stage,'')
		FROM portfolio.initiative_event
		WHERE initiative_id=$1 AND kind='moved' AND actor='flow-manager'
		ORDER BY at DESC LIMIT 1`, id).Scan(&movedFrom, &movedTo); err != nil {
		t.Errorf("forward: moved-Event fehlt: %v", err)
	} else if movedFrom != "now" || movedTo != "watching" {
		t.Errorf("forward: moved-Event %s→%s, want now→watching", movedFrom, movedTo)
	}

	// 2. rückwärts blockt: watching→soon (Event geschrieben, kein Move).
	moved, reason, err = applyStageProposal(ctx, p, id, "soon", nil, "flow-manager", false)
	if err != nil {
		t.Fatalf("apply backward: %v", err)
	}
	if moved || reason != "not-forward" || stageOf() != "watching" {
		t.Errorf("backward: moved=%v reason=%q stage=%q, want false/not-forward/watching", moved, reason, stageOf())
	}
	if eventCount() != 2 {
		t.Errorf("backward: Event muss trotzdem geschrieben sein (erwartet 2), got %d", eventCount())
	}

	// 3. locked blockt: done-Vorschlag auf gepinnter Karte.
	_, _ = p.Exec(ctx, "UPDATE portfolio.initiative SET stage_locked_by_human=true WHERE id=$1", id)
	moved, reason, err = applyStageProposal(ctx, p, id, "done", nil, "flow-manager", false)
	if err != nil {
		t.Fatalf("apply locked: %v", err)
	}
	if moved || reason != "locked" || stageOf() != "watching" {
		t.Errorf("locked: moved=%v reason=%q stage=%q, want false/locked/watching", moved, reason, stageOf())
	}
	_, _ = p.Exec(ctx, "UPDATE portfolio.initiative SET stage_locked_by_human=false WHERE id=$1", id)

	// 4. Not-Aus blockt: PORTFOLIO_STEWARD_HALT=1 (Event ja, Move nein).
	os.Setenv("PORTFOLIO_STEWARD_HALT", "1")
	defer os.Unsetenv("PORTFOLIO_STEWARD_HALT")
	before := eventCount()
	moved, reason, err = applyStageProposal(ctx, p, id, "done", nil, "flow-manager", false)
	if err != nil {
		t.Fatalf("apply halt: %v", err)
	}
	if moved || reason != "halted" || stageOf() != "watching" {
		t.Errorf("halt: moved=%v reason=%q stage=%q, want false/halted/watching", moved, reason, stageOf())
	}
	if eventCount() != before+1 {
		t.Errorf("halt: Event muss trotzdem geschrieben sein (erwartet %d), got %d", before+1, eventCount())
	}

	// Genau EIN moved-Event über alle vier Pfade: nur der echte Vollzug (1.)
	// hinterlässt eins — geblockte Vorschläge (rückwärts/locked/halt) nie.
	var movedEvents int
	_ = p.QueryRow(ctx, "SELECT count(*) FROM portfolio.initiative_event WHERE initiative_id=$1 AND kind='moved'", id).Scan(&movedEvents)
	if movedEvents != 1 {
		t.Errorf("moved-Events: got %d, want 1 (nur der vollzogene Move)", movedEvents)
	}

	// 5. WP2: Event trägt das ERGEBNIS (outcome) — und identische
	// Vorschlags-Stände werden nicht wiederholt (Proposal-Delta-Gate):
	// derselbe halt-Vorschlag nochmal ⇒ KEIN neues Event.
	var lastOutcome string
	if err := p.QueryRow(ctx, `SELECT COALESCE(payload->>'outcome','')
		FROM portfolio.initiative_event
		WHERE initiative_id=$1 AND kind='stage_proposed'
		ORDER BY at DESC LIMIT 1`, id).Scan(&lastOutcome); err != nil {
		t.Fatalf("outcome lesen: %v", err)
	}
	if lastOutcome != "halted" {
		t.Errorf("outcome im Event: got %q, want halted", lastOutcome)
	}
	beforeRepeat := eventCount()
	moved, reason, err = applyStageProposal(ctx, p, id, "done", nil, "flow-manager", false)
	if err != nil {
		t.Fatalf("apply halt repeat: %v", err)
	}
	if moved || reason != "halted" {
		t.Errorf("halt repeat: moved=%v reason=%q, want false/halted", moved, reason)
	}
	if eventCount() != beforeRepeat {
		t.Errorf("Proposal-Delta-Gate: Wiederholung schrieb ein Event (got %d, want %d)", eventCount(), beforeRepeat)
	}
}

// TestApplyStageProposal_ProposeOnly_Integration beweist den WP1-Nachtrag:
// proposeOnly=true schreibt das stage_proposed-Event mit propose_only:true +
// reason='propose-only (quantbot)', bewegt aber NIE (Regel 3, quantbot).
func TestApplyStageProposal_ProposeOnly_Integration(t *testing.T) {
	p := mkVollzugPool(t)
	defer p.Close()
	ctx := context.Background()

	id := "qb-vollzug-propose-only"
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id=$1", id)
	_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id=$1", id)
	if _, err := p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend)
		VALUES ($1,'quantbot','now',$1,'plan_file')`, id); err != nil {
		t.Fatalf("insert: %v", err)
	}
	defer func() {
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_event WHERE initiative_id=$1", id)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id=$1", id)
	}()

	moved, reason, err := applyStageProposal(ctx, p, id, "watching", map[string]any{"reason": "promote-reif"}, "flow-manager", true)
	if err != nil {
		t.Fatalf("propose-only: %v", err)
	}
	if moved || reason != "propose-only" {
		t.Errorf("propose-only: moved=%v reason=%q, want false/propose-only", moved, reason)
	}
	// Stage unverändert (kein Vollzug).
	var stage string
	_ = p.QueryRow(ctx, "SELECT stage FROM portfolio.initiative WHERE id=$1", id).Scan(&stage)
	if stage != "now" {
		t.Errorf("propose-only darf nicht bewegen — stage=%q, want now", stage)
	}
	// Event trägt propose_only:true + reason.
	var proposeOnly bool
	var evReason string
	if err := p.QueryRow(ctx, `SELECT (payload->>'propose_only')::bool, payload->>'reason'
		FROM portfolio.initiative_event
		WHERE initiative_id=$1 AND kind='stage_proposed' ORDER BY at DESC LIMIT 1`, id).
		Scan(&proposeOnly, &evReason); err != nil {
		t.Fatalf("Event lesen: %v", err)
	}
	if !proposeOnly || evReason != "propose-only (quantbot)" {
		t.Errorf("stage_proposed-Event: propose_only=%v reason=%q, want true/'propose-only (quantbot)'", proposeOnly, evReason)
	}
}

// TestGatherDoneEvidence_Integration prüft die DB-Evidenzsammlung für die drei
// Ledger-Lagen (mit Live-Deploy / ohne Ledger-Vorkommen / Urteilsfall).
func TestGatherDoneEvidence_Integration(t *testing.T) {
	p := mkVollzugPool(t)
	defer p.Close()
	ctx := context.Background()

	cleanup := func(id string) {
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.plan_item WHERE initiative_id=$1", id)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_link WHERE initiative_id=$1", id)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative_tag WHERE initiative_id=$1", id)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.deployments WHERE initiative_id=$1", id)
		_, _ = p.Exec(ctx, "DELETE FROM portfolio.initiative WHERE id=$1", id)
	}
	mkInit := func(id string) {
		cleanup(id)
		_, err := p.Exec(ctx, `INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend)
			VALUES ($1,'code-factory','watching',$1,'plan_file')`, id)
		if err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	// Eindeutiger Service-Name, der garantiert im Ledger vorkommt.
	const svc = "vollzug-evidence-svc"

	// Fall A: Delivery (plan_item) + eigener Live-Deploy.
	idA := "st-vollzug-ev-a"
	mkInit(idA)
	defer cleanup(idA)
	_, _ = p.Exec(ctx, `INSERT INTO portfolio.plan_item (id, initiative_id, slug, path, layer, status)
		VALUES ($1,$2,'a-slug','/x/a-slug-prd.md','implementation','delivered')`, idA+"-pi", idA)
	_, _ = p.Exec(ctx, `INSERT INTO portfolio.deployments (service, initiative_id, status, deployed_at, git_sha, deployed_by)
		VALUES ($1,$2,'live', now(), 'testsha00', 'itest')`, svc, idA)
	evA, err := gatherDoneEvidence(ctx, p, idA)
	if err != nil {
		t.Fatalf("gather A: %v", err)
	}
	if !evA.HasDelivery || evA.DeployStatus != "live" {
		t.Errorf("A: %+v, want HasDelivery=true DeployStatus=live", evA)
	}
	if move, _ := watchingDoneDecision(evA); !move {
		t.Errorf("A: erwartet Move nach done")
	}

	// Fall B: Delivery via *-delivery.md, Software NICHT im Ledger → Delivery reicht.
	idB := "st-vollzug-ev-b"
	mkInit(idB)
	defer cleanup(idB)
	_, _ = p.Exec(ctx, `INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		VALUES ($1,'plan_file','/x/foo-delivery.md')`, idB)
	_, _ = p.Exec(ctx, `INSERT INTO portfolio.initiative_tag (initiative_id, kind, value)
		VALUES ($1,'software','ganz-und-gar-nicht-im-ledger')`, idB)
	evB, err := gatherDoneEvidence(ctx, p, idB)
	if err != nil {
		t.Fatalf("gather B: %v", err)
	}
	if !evB.HasDelivery || evB.DeployStatus != "" || evB.SoftwareInLedger {
		t.Errorf("B: %+v, want HasDelivery=true DeployStatus='' SoftwareInLedger=false", evB)
	}
	if move, reason := watchingDoneDecision(evB); !move || reason != "delivery-ohne-ledger-software" {
		t.Errorf("B: (%v,%q), want move/delivery-ohne-ledger-software", move, reason)
	}

	// Fall C: Urteilsfall — Delivery da, Software im Ledger (fremder Eintrag),
	// aber KEIN eigener Deploy-Beleg der Karte.
	idC := "st-vollzug-ev-c"
	idOther := "st-vollzug-ev-c-other"
	mkInit(idC)
	mkInit(idOther)
	defer cleanup(idC)
	defer cleanup(idOther)
	_, _ = p.Exec(ctx, `INSERT INTO portfolio.plan_item (id, initiative_id, slug, path, layer, status)
		VALUES ($1,$2,'c-slug','/x/c-slug-prd.md','implementation','done')`, idC+"-pi", idC)
	_, _ = p.Exec(ctx, `INSERT INTO portfolio.initiative_tag (initiative_id, kind, value)
		VALUES ($1,'software',$2)`, idC, svc)
	// Ledger-Vorkommen des Service, aber auf einer ANDEREN Karte.
	_, _ = p.Exec(ctx, `INSERT INTO portfolio.deployments (service, initiative_id, status, deployed_at, git_sha, deployed_by)
		VALUES ($1,$2,'live', now(), 'testsha00', 'itest')`, svc, idOther)
	evC, err := gatherDoneEvidence(ctx, p, idC)
	if err != nil {
		t.Fatalf("gather C: %v", err)
	}
	if !evC.HasDelivery || evC.DeployStatus != "" || !evC.SoftwareInLedger {
		t.Errorf("C: %+v, want HasDelivery=true DeployStatus='' SoftwareInLedger=true", evC)
	}
	if move, reason := watchingDoneDecision(evC); move || reason != "watching-ohne-deploy" {
		t.Errorf("C: (%v,%q), want false/watching-ohne-deploy", move, reason)
	}
}
