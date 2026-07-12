package main

// merge.go — Merge-Primitive für das Master-Kanban (mk-verwalter-vollzug-PRD,
// WP2a). Führt eine Dublette in eine Ziel-Karte zusammen: hängt Links, Tags und
// plan_items um, schreibt ein merged_into-Event auf die Dublette + ein
// commented-Event auf das Ziel und archiviert die Dublette. NICHTS wird gelöscht
// (Regel 4, reversibel) — die Dublette bleibt als archivierte Karte mit
// vollständiger Event-Spur erhalten. Alles in EINER Transaktion; --dry-run
// simuliert den Merge (voller Ablauf) und rollt danach zurück.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

// MergeReport hält das Ergebnis eines Merge — was tatsächlich (bzw. bei
// --dry-run: was würde) umgehängt wurde. High-signal (ACI): nur die Zähler, die
// für die Nachvollziehbarkeit zählen.
type MergeReport struct {
	DupID          string `json:"dup_id"`
	ZielID         string `json:"ziel_id"`
	LinksMoved     int64  `json:"links_moved"`   // in die Ziel-Karte eingefügte Dubletten-Links
	LinksDropped   int64  `json:"links_dropped"` // Dubletten-Links, die das Ziel schon hatte
	TagsMoved      int64  `json:"tags_moved"`
	TagsDropped    int64  `json:"tags_dropped"`
	PlanItemsMoved int64  `json:"plan_items_moved"`
	Archived       bool   `json:"archived"` // Dublette archiviert (false bei dry-run)
	DryRun         bool   `json:"dry_run"`
}

// validateMerge ist die reine (DB-freie) Merge-Validierung — Regeln 3/4 +
// Poka-yoke. Rückgabe nil = Merge erlaubt; sonst eine handlungsleitende
// Fehlermeldung (was schiefging + wie fixen). Als reine Funktion table-testbar;
// die DB-tragenden Schritte liegen in mergeInitiatives.
func validateMerge(dupID, zielID, dupFirma string, dupArchived bool, zielFirma string, zielArchived, forceProposeAck bool) error {
	if dupID == zielID {
		return fmt.Errorf("merge verweigert: Dublette und Ziel sind identisch (%q) — dup und ziel müssen verschieden sein", dupID)
	}
	if zielArchived {
		return fmt.Errorf("merge verweigert: Ziel %q ist archiviert — man kann nicht in eine archivierte Karte mergen; ein unarchiviertes Ziel wählen", zielID)
	}
	if dupArchived {
		return fmt.Errorf("merge verweigert: Dublette %q ist bereits archiviert — nichts zu mergen", dupID)
	}
	// Regel 3: quantbot-Nähe = Live-Geld. Nur mit ausdrücklichem Ack.
	if (dupFirma == "quantbot" || zielFirma == "quantbot") && !forceProposeAck {
		return fmt.Errorf("merge verweigert: quantbot-Karte betroffen (dup-firma=%s, ziel-firma=%s) — Regel 3 (Live-Geld-Nähe). Nur mit --force-propose-ack (CLI) bzw. force_propose_ack:true (API) nach bewusster Prüfung", dupFirma, zielFirma)
	}
	return nil
}

// lockInitiative liest firma + archiviert-Status einer Karte und sperrt die
// Zeile (FOR UPDATE) gegen Nebenläufigkeit während des Merge.
func lockInitiative(ctx context.Context, tx pgx.Tx, id string) (firma string, archived bool, err error) {
	var archivedAt *time.Time
	err = tx.QueryRow(ctx,
		`SELECT firma, archived_at FROM portfolio.initiative WHERE id=$1 FOR UPDATE`, id).
		Scan(&firma, &archivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, fmt.Errorf("existiert nicht")
	}
	if err != nil {
		return "", false, err
	}
	return firma, archivedAt != nil, nil
}

// mergeInitiatives führt die Dublette dupID in die Ziel-Karte zielID zusammen.
// forceProposeAck hebt die quantbot-Sperre (Regel 3) auf. dryRun fährt den
// vollen Ablauf und rollt danach zurück (Simulation mit echten Zählern).
func mergeInitiatives(ctx context.Context, pool *pgxpool.Pool, dupID, zielID, actor string, dryRun, forceProposeAck bool) (MergeReport, error) {
	rep := MergeReport{DupID: dupID, ZielID: zielID, DryRun: dryRun}
	if actor == "" {
		actor = "merge" // Poka-yoke: die Event-Spur braucht immer einen Akteur.
	}
	// Billige Vorab-Prüfung ohne DB (auch von validateMerge abgedeckt).
	if dupID == zielID {
		return rep, validateMerge(dupID, zielID, "", false, "", false, forceProposeAck)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return rep, fmt.Errorf("Transaktion öffnen: %w", err)
	}
	defer tx.Rollback(ctx)

	dupFirma, dupArchived, err := lockInitiative(ctx, tx, dupID)
	if err != nil {
		return rep, fmt.Errorf("Dublette %q: %w — ID per 'master-kanban list' prüfen", dupID, err)
	}
	zielFirma, zielArchived, err := lockInitiative(ctx, tx, zielID)
	if err != nil {
		return rep, fmt.Errorf("Ziel %q: %w — ID per 'master-kanban list' prüfen", zielID, err)
	}
	if err := validateMerge(dupID, zielID, dupFirma, dupArchived, zielFirma, zielArchived, forceProposeAck); err != nil {
		return rep, err
	}

	// (b) Links umhängen: nur die, die das Ziel noch nicht hat (kein
	//     Unique-Constraint auf initiative_link → NOT EXISTS statt ON CONFLICT),
	//     danach die Dubletten-Links löschen.
	tag, err := tx.Exec(ctx, `
		INSERT INTO portfolio.initiative_link (initiative_id, kind, ref)
		SELECT $2, l.kind, l.ref FROM portfolio.initiative_link l
		 WHERE l.initiative_id=$1
		   AND NOT EXISTS (SELECT 1 FROM portfolio.initiative_link z
		                    WHERE z.initiative_id=$2 AND z.kind=l.kind AND z.ref=l.ref)
	`, dupID, zielID)
	if err != nil {
		return rep, fmt.Errorf("Links umhängen: %w", err)
	}
	rep.LinksMoved = tag.RowsAffected()
	tag, err = tx.Exec(ctx, `DELETE FROM portfolio.initiative_link WHERE initiative_id=$1`, dupID)
	if err != nil {
		return rep, fmt.Errorf("Dubletten-Links entfernen: %w", err)
	}
	rep.LinksDropped = tag.RowsAffected() - rep.LinksMoved

	// (c) Tags umhängen — analog (initiative_tag hat zwar einen PK, aber NOT
	//     EXISTS hält die Logik identisch zu den Links und liefert den drop-Zähler).
	tag, err = tx.Exec(ctx, `
		INSERT INTO portfolio.initiative_tag (initiative_id, kind, value)
		SELECT $2, t.kind, t.value FROM portfolio.initiative_tag t
		 WHERE t.initiative_id=$1
		   AND NOT EXISTS (SELECT 1 FROM portfolio.initiative_tag z
		                    WHERE z.initiative_id=$2 AND z.kind=t.kind AND z.value=t.value)
	`, dupID, zielID)
	if err != nil {
		return rep, fmt.Errorf("Tags umhängen: %w", err)
	}
	rep.TagsMoved = tag.RowsAffected()
	tag, err = tx.Exec(ctx, `DELETE FROM portfolio.initiative_tag WHERE initiative_id=$1`, dupID)
	if err != nil {
		return rep, fmt.Errorf("Dubletten-Tags entfernen: %w", err)
	}
	rep.TagsDropped = tag.RowsAffected() - rep.TagsMoved

	// (d) plan_items umhängen.
	tag, err = tx.Exec(ctx, `UPDATE portfolio.plan_item SET initiative_id=$2 WHERE initiative_id=$1`, dupID, zielID)
	if err != nil {
		return rep, fmt.Errorf("plan_items umhängen: %w", err)
	}
	rep.PlanItemsMoved = tag.RowsAffected()

	// (e) merged_into-Event auf die Dublette, commented-Event auf das Ziel.
	dupPayload, _ := json.Marshal(map[string]any{
		"into": zielID, "links_moved": rep.LinksMoved, "tags_moved": rep.TagsMoved,
		"plan_items_moved": rep.PlanItemsMoved, "actor": actor,
	})
	if _, err := tx.Exec(ctx, `
		INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor, at)
		VALUES ($1, 'merged_into', 'master', $2, $3, now())`,
		dupID, string(dupPayload), actor); err != nil {
		return rep, fmt.Errorf("merged_into-Event (Dublette): %w", err)
	}
	zielPayload, _ := json.Marshal(map[string]any{
		"merged_from": dupID, "links_moved": rep.LinksMoved, "tags_moved": rep.TagsMoved,
		"plan_items_moved": rep.PlanItemsMoved, "actor": actor,
	})
	if _, err := tx.Exec(ctx, `
		INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor, at)
		VALUES ($1, 'commented', 'master', $2, $3, now())`,
		zielID, string(zielPayload), actor); err != nil {
		return rep, fmt.Errorf("commented-Event (Ziel): %w", err)
	}

	// (f) Dublette archivieren — nichts wird gelöscht (Regel 4, reversibel).
	if _, err := tx.Exec(ctx,
		`UPDATE portfolio.initiative SET archived_at=now() WHERE id=$1`, dupID); err != nil {
		return rep, fmt.Errorf("Dublette archivieren: %w", err)
	}

	if dryRun {
		_ = tx.Rollback(ctx) // Simulation: zurückrollen, Zähler behalten.
		return rep, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return rep, fmt.Errorf("Merge committen: %w", err)
	}
	rep.Archived = true
	return rep, nil
}

// printMergeReport gibt den Report menschenlesbar aus (CLI).
func printMergeReport(rep MergeReport) {
	prefix := "✓ Merge"
	if rep.DryRun {
		prefix = "[dry-run] Merge (nichts geschrieben)"
	}
	fmt.Printf("%s: %s → %s\n", prefix, rep.DupID, rep.ZielID)
	fmt.Printf("  Links:      %d umgehängt, %d verworfen (Ziel hatte sie schon)\n", rep.LinksMoved, rep.LinksDropped)
	fmt.Printf("  Tags:       %d umgehängt, %d verworfen\n", rep.TagsMoved, rep.TagsDropped)
	fmt.Printf("  plan_items: %d umgehängt\n", rep.PlanItemsMoved)
	if rep.Archived {
		fmt.Printf("  Dublette %s archiviert (reversibel — Events erhalten).\n", rep.DupID)
	} else if rep.DryRun {
		fmt.Printf("  Dublette %s würde archiviert (Events auf beide Karten).\n", rep.DupID)
	}
}

func cmdMerge() *cobra.Command {
	var dryRun, forceProposeAck bool
	c := &cobra.Command{
		Use:   "merge <dup-id> <ziel-id>",
		Short: "Dublette in Ziel-Karte zusammenführen (Links/Tags/plan_items umhängen, Dublette archivieren)",
		Long: `Führt die Dublette (erstes Argument) in die Ziel-Karte (zweites Argument)
zusammen. Hängt Links, Tags und plan_items um, schreibt merged_into-/commented-
Events auf beide Karten und archiviert die Dublette (nichts wird gelöscht,
reversibel). Verweigert, wenn dup==ziel, das Ziel oder die Dublette archiviert
ist, oder eine quantbot-Karte betroffen ist (dann nur mit --force-propose-ack).

Beispiele:
  master-kanban merge st-alt-karte st-ziel-karte --dry-run
  master-kanban merge cf-dublette cf-kanon`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := connect()
			rep, err := mergeInitiatives(context.Background(), p, args[0], args[1], "merge-cli", dryRun, forceProposeAck)
			if err != nil {
				return err
			}
			printMergeReport(rep)
			return nil
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "Merge simulieren (voller Ablauf, danach Rollback — nichts wird geschrieben)")
	c.Flags().BoolVar(&forceProposeAck, "force-propose-ack", false, "quantbot-Sperre (Regel 3) bewusst aufheben (Kalibrier-Pfad)")
	return c
}
