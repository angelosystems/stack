package main

// mk-session-lane WP1: Sessions claimen ihre inline gebauten Karten —
// lane=session + session=<kennung>. Mario-Override gewinnt immer.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

func sessionClaim(ctx context.Context, p *pgxpool.Pool, id, kennung string) error {
	var marioLane string
	_ = p.QueryRow(ctx, `SELECT l.value FROM portfolio.initiative_tag l
	     JOIN portfolio.initiative_tag src ON src.initiative_id = l.initiative_id
	          AND src.kind='lane-source' AND src.value='mario'
	     WHERE l.initiative_id=$1 AND l.kind='lane'`, id).Scan(&marioLane)
	if marioLane != "" && marioLane != "session" {
		return fmt.Errorf("karte %s hat Mario-Lane %q — session-claim ueberschreibt Mario nie (Lane im Cockpit umstellen, dann erneut)", id, marioLane)
	}
	tx, err := p.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	ct, err := tx.Exec(ctx, `DELETE FROM portfolio.initiative_tag
	     WHERE initiative_id=$1 AND kind IN ('lane','lane-source','session')
	        OR (initiative_id=$1 AND kind='triage' AND value='lane-pending')`, id)
	_ = ct
	if err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM portfolio.initiative WHERE id=$1)`, id).Scan(&exists); err != nil || !exists {
		return fmt.Errorf("karte %s nicht gefunden", id)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO portfolio.initiative_tag (initiative_id, kind, value)
	     VALUES ($1,'lane','session'), ($1,'lane-source','session'), ($1,'session',$2)
	     ON CONFLICT DO NOTHING`, id, kennung); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"text": "session-claim: " + kennung + " arbeitet inline an dieser Karte (lane=session)"})
	if _, err := tx.Exec(ctx, `INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
	     VALUES ($1,'commented','master',$2::jsonb,$3)`, id, string(payload), "session:"+kennung); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func cmdSessionClaim() *cobra.Command {
	return &cobra.Command{
		Use:   "session-claim <karte> <session-kennung>",
		Short: "Session claimt eine inline gebaute Karte (lane=session + session-Tag)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := sessionClaim(cmd.Context(), connect(), args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("✓ %s → lane=session (🤖 %s)\n", args[0], args[1])
			return nil
		},
	}
}
