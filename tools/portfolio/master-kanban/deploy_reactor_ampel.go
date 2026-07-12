package main

// mk-pipeline-ampel WP1: Die Ledger-Zeile bekommt ihre Karten-ID, damit die
// Board-Ampel dem Deploy folgt. Aufloesung NUR wenn eindeutig — mehrdeutige
// Treffer bleiben leer (Regel 2: nie raten). Best-effort: ein Fehler hier
// haelt keinen Deploy auf.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// resolveInitiativeID loest die Karten-ID einer Deploy-Zeile auf:
// primaer ueber die Bead-Links (bead_ids[] → initiative_link kind=bead),
// Fallback ueber service == software-Tag einer unarchivierten Karte.
// Rueckgabe ("", grund) wenn nichts Eindeutiges gefunden wurde.
func resolveInitiativeID(ctx context.Context, p *pgxpool.Pool, beadIDs []string, service string) (string, string) {
	if len(beadIDs) > 0 {
		rows, err := p.Query(ctx, `SELECT DISTINCT initiative_id
		     FROM portfolio.initiative_link
		     WHERE kind='bead' AND ref = ANY($1)`, beadIDs)
		if err == nil {
			var ids []string
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					ids = append(ids, id)
				}
			}
			rows.Close()
			if len(ids) == 1 {
				return ids[0], "bead-link"
			}
			if len(ids) > 1 {
				return "", "mehrdeutig: beads gehoeren zu mehreren karten"
			}
		}
	}
	// Fallback: service entspricht genau EINEM software-Tag einer aktiven Karte.
	rows, err := p.Query(ctx, `SELECT t.initiative_id
	     FROM portfolio.initiative_tag t
	     JOIN portfolio.initiative i ON i.id = t.initiative_id AND i.archived_at IS NULL
	     WHERE t.kind='software' AND t.value=$1`, service)
	if err != nil {
		return "", "software-tag-query miss"
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 1 {
		return ids[0], "software-tag"
	}
	if len(ids) > 1 {
		return "", "mehrdeutig: software-tag auf mehreren karten"
	}
	return "", "kein treffer"
}

// attachInitiative persistiert die aufgeloeste Karten-ID an der Outbox-Zeile —
// nur wenn dort noch keine steht (Producer/ledger-record.sh gewinnt immer).
func (r *reactor) attachInitiative(ctx context.Context, o *outboxRow) {
	if o.InitiativeID != "" {
		return
	}
	id, quelle := resolveInitiativeID(ctx, r.pool, o.BeadIDs, o.Service)
	if id == "" {
		r.logf("· %s@%s ohne Karten-ID (%s)", o.Service, o.Environment, quelle)
		return
	}
	if _, err := r.pool.Exec(ctx, `UPDATE portfolio.deployments
	     SET initiative_id=$1 WHERE id=$2 AND initiative_id IS NULL`, id, o.ID); err != nil {
		r.logf("· %s@%s Karten-ID %s nicht persistiert: %v", o.Service, o.Environment, id, err)
		return
	}
	o.InitiativeID = id
	r.logf("· %s@%s → Karte %s (%s)", o.Service, o.Environment, id, quelle)
}
