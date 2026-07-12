package main

// events_tend.go — beaufsichtigter Aufräum-Job für die Event-Tabelle
// (mk-verwalter-vollzug-PRD, WP1). Löscht alte flow_action/activity-Events
// (413k Bestand aus dem 30-s-Churn) in Batches. BEWUSST nicht automatisch
// verdrahtet — nur manuell via `master-kanban events-tend`.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

func cmdEventsTend() *cobra.Command {
	var olderThan time.Duration
	var kinds string
	var dryRun bool
	c := &cobra.Command{
		Use:   "events-tend",
		Short: "Räumt alte flow_action/activity-Events in Batches auf (Delta-Gate-Kehraus)",
		Long: `Löscht initiative_event-Zeilen der angegebenen kinds, die älter als die
Schwelle sind, in Batches (LIMIT 10000 pro Runde), und gibt den Zählerstand aus.
Reiner Wartungs-Job — NICHT automatisch verdrahtet. --dry-run zählt nur.

Beispiele:
  master-kanban events-tend --dry-run
  master-kanban events-tend --older-than 168h --kinds flow_action,activity`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := connect()
			return runEventsTend(context.Background(), p, olderThan, kinds, dryRun)
		},
	}
	c.Flags().DurationVar(&olderThan, "older-than", 168*time.Hour, "Alter-Schwelle als Dauer (z.B. 168h = 7 Tage)")
	c.Flags().StringVar(&kinds, "kinds", "flow_action,activity", "Komma-Liste der zu löschenden Event-kinds")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "Nur zählen, nichts löschen")
	return c
}

// runEventsTend zählt und löscht die Alt-Events in Batches. Batch-Grenze 10000
// pro Runde hält Locks/WAL klein; Abbruch, sobald ein Batch < 10000 liefert
// (nichts mehr da).
func runEventsTend(ctx context.Context, p *pgxpool.Pool, olderThan time.Duration, kindsCSV string, dryRun bool) error {
	var kinds []string
	for _, k := range strings.Split(kindsCSV, ",") {
		if k = strings.TrimSpace(k); k != "" {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) == 0 {
		return fmt.Errorf("keine kinds angegeben — z.B. --kinds flow_action,activity")
	}
	cutoff := time.Now().Add(-olderThan)

	var candidates int64
	if err := p.QueryRow(ctx, `
		SELECT count(*) FROM portfolio.initiative_event
		 WHERE kind = ANY($1) AND at < $2
	`, kinds, cutoff).Scan(&candidates); err != nil {
		return fmt.Errorf("Kandidaten zählen: %w", err)
	}
	fmt.Printf("events-tend: %d Event(s) kinds=%s älter als %s (vor %s)\n",
		candidates, strings.Join(kinds, ","), olderThan, cutoff.Format(time.RFC3339))

	if dryRun {
		fmt.Println("dry-run: nichts gelöscht.")
		return nil
	}

	var deleted int64
	const batch = 10000
	for {
		tag, err := p.Exec(ctx, `
			DELETE FROM portfolio.initiative_event
			 WHERE ctid IN (
			   SELECT ctid FROM portfolio.initiative_event
			    WHERE kind = ANY($1) AND at < $2
			    LIMIT $3
			 )
		`, kinds, cutoff, batch)
		if err != nil {
			return fmt.Errorf("Batch-Delete: %w (bisher %d gelöscht)", err, deleted)
		}
		n := tag.RowsAffected()
		deleted += n
		fmt.Printf("  … Batch gelöscht: %d (kumuliert %d)\n", n, deleted)
		if n < batch {
			break
		}
	}
	fmt.Printf("events-tend fertig: %d Event(s) gelöscht.\n", deleted)
	return nil
}
