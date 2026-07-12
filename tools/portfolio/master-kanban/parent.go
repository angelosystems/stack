package main

// mk-karten-hierarchie WP2: parent_id-Schreibwege — API + CLI + Zyklen-Wache.
// parent-source-Tag dokumentiert die Quelle (mario|adapter|verwalter);
// mario wird nie ueberschrieben (Wache liegt beim Adapter, nicht hier —
// API-Aufrufe SIND Mario/Verwalter-Entscheidungen).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

// parentCycleCheck laeuft die Ahnenkette des designierten Dachs — landet sie
// wieder bei id, waere die Hierarchie zyklisch. Flach gedacht (2 Ebenen
// Praxis), Kappe 10 als Notbremse gegen entartete Ketten.
func parentCycleCheck(ctx context.Context, p *pgxpool.Pool, id, parentID string) error {
	cur := parentID
	for depth := 0; cur != "" && depth < 10; depth++ {
		if cur == id {
			return fmt.Errorf("zyklus: %s ist (indirekt) kind von %s — parent nicht gesetzt", parentID, id)
		}
		var next *string
		if err := p.QueryRow(ctx, `SELECT parent_id FROM portfolio.initiative WHERE id=$1`, cur).Scan(&next); err != nil {
			return fmt.Errorf("dach-karte %s nicht gefunden", cur)
		}
		if next == nil {
			return nil
		}
		cur = *next
	}
	return nil
}

// setParent setzt/loescht parent_id, stempelt parent-source, raeumt
// triage:parent-check ab und hinterlaesst ein linked-Event.
func setParent(ctx context.Context, p *pgxpool.Pool, id, parentID, source string) error {
	if parentID != "" {
		if err := parentCycleCheck(ctx, p, id, parentID); err != nil {
			return err
		}
	}
	tx, err := p.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var tag *string
	if parentID != "" {
		tag = &parentID
	}
	ct, err := tx.Exec(ctx, `UPDATE portfolio.initiative SET parent_id=$2 WHERE id=$1`, id, tag)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("karte %s nicht gefunden", id)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM portfolio.initiative_tag
	     WHERE initiative_id=$1 AND (kind='parent-source' OR (kind='triage' AND value='parent-check'))`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO portfolio.initiative_tag (initiative_id, kind, value)
	     VALUES ($1,'parent-source',$2) ON CONFLICT DO NOTHING`, id, source); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"parent": parentID, "source": source})
	if _, err := tx.Exec(ctx, `INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor)
	     VALUES ($1,'linked','master',$2::jsonb,'master-kanban')`, id, string(payload)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func handleParent(p *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,X-Api-Key")
		if r.Method == "OPTIONS" {
			return
		}
		if r.Method != "POST" {
			http.Error(w, "POST only", 405)
			return
		}
		if !checkAuth(r) {
			http.Error(w, "auth erforderlich (SSO-Header oder X-Api-Key)", 401)
			return
		}
		var body struct {
			Id       string `json:"id"`
			ParentId string `json:"parent_id"`
			Source   string `json:"source"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		body.Id = strings.TrimSpace(body.Id)
		body.ParentId = strings.TrimSpace(body.ParentId)
		if body.Source == "" {
			body.Source = "mario"
		}
		if body.Id == "" {
			http.Error(w, "id erforderlich; parent_id leer = dach loesen", 400)
			return
		}
		if err := setParent(r.Context(), p, body.Id, body.ParentId, body.Source); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": body.Id, "parent": body.ParentId})
	}
}

func cmdParent() *cobra.Command {
	return &cobra.Command{
		Use:   "parent <id> <dach-id|->",
		Short: "Dach-Karte setzen ('-' = loesen); raeumt triage:parent-check ab",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			parentID := args[1]
			if parentID == "-" {
				parentID = ""
			}
			if err := setParent(cmd.Context(), connect(), args[0], parentID, "mario"); err != nil {
				return err
			}
			if parentID == "" {
				fmt.Printf("✓ %s ist jetzt top-level (dach geloest)\n", args[0])
			} else {
				fmt.Printf("✓ %s → dach %s\n", args[0], parentID)
			}
			return nil
		},
	}
}
