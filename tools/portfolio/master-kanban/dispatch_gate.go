package main

// mk-dispatch-gate WP2: Der Cockpit-Dispatch vergibt das lane-Tag — die
// Zuendung fuer das Decomposer-Gate (WP1). lane-source=mario ist unantastbar
// (Auto-Stufe LANE_AUTO darf spaeter nur lane-source=auto setzen).

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// laneTagFor uebersetzt die Dispatch-Lane in den Tag-Wert.
var laneTagFor = map[string]string{
	"plan": "solartown", "plan-deep": "solartown",
	"hack": "vibe-kanban", "human": "human", "session": "session",
}

// setLaneTag setzt lane=<wert> + lane-source=mario an der Karte und raeumt
// triage:lane-pending ab. Ein bestehendes lane-Tag wird ERSETZT (Mario
// entscheidet um), lane-source=auto weicht mario.
func setLaneTag(ctx context.Context, p *pgxpool.Pool, initiativeID, lane string) error {
	tx, err := p.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`DELETE FROM portfolio.initiative_tag WHERE initiative_id=$1 AND kind IN ('lane','lane-source')
		 OR (initiative_id=$1 AND kind='triage' AND value='lane-pending')`, initiativeID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO portfolio.initiative_tag (initiative_id, kind, value)
		 VALUES ($1,'lane',$2), ($1,'lane-source','mario') ON CONFLICT DO NOTHING`,
		initiativeID, lane); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// approvedPlanItem liefert (slug, path, layer, status) des juengsten
// approved(-with-notes) plan_items der Karte — leer, wenn keins existiert.
func approvedPlanItem(ctx context.Context, p *pgxpool.Pool, initiativeID string) (slug, path, layer, status string) {
	_ = p.QueryRow(ctx,
		`SELECT slug, COALESCE(path,''), COALESCE(layer,'prd'), status
		 FROM portfolio.plan_item
		 WHERE initiative_id=$1 AND status IN ('approved','approved-with-notes')
		 ORDER BY updated_at DESC LIMIT 1`, initiativeID).
		Scan(&slug, &path, &layer, &status)
	return
}

// reEmitPlanApproved wiederholt das plan.status-changed-Event in town.events
// (:5433), damit der laufende Decomposer die frisch dispatchte Karte sofort
// zerlegt (idempotent: Claims + Kinder-Check im Decomposer). Best-effort.
func reEmitPlanApproved(ctx context.Context, slug, path, layer, status string) error {
	sp, err := solartownPool()
	if err != nil {
		return fmt.Errorf("solartown-Pool: %w", err)
	}
	payload, _ := json.Marshal(map[string]string{
		"slug": slug, "path": path, "layer": layer,
		"old": status, "new": status,
	})
	_, err = sp.Exec(ctx, `SELECT town.emit('plan.status-changed', $1::jsonb, 'master-kanban')`, string(payload))
	return err
}
