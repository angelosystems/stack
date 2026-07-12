package main

// findings.go — Findings-Surface fuer den mk-verwalter (mk-verwalter-vollzug-PRD,
// WP3). Duenner Lese-Layer ueber die View portfolio.steward_findings: eine
// gemeinsame Query (queryStewardFindings) treibt sowohl die HTTP-Route
// GET /api/steward/findings (Reflex cf-reflex-board-pflege) als auch die CLI
// `master-kanban steward-findings` (Menschen/Debug). Ein Regel-Satz lebt in der
// View (SQL), hier nur Projektion concise/detailed + Fehler-Uebersetzung.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

// StewardFinding ist eine Zeile der View. firma/stage/detail sind omitempty —
// die concise-Projektion laesst sie weg (nur klasse+initiative_id+titel+hash).
type StewardFinding struct {
	Klasse       string          `json:"klasse"`
	InitiativeID string          `json:"initiative_id"`
	Titel        string          `json:"titel"`
	Firma        string          `json:"firma,omitempty"`
	Stage        string          `json:"stage,omitempty"`
	Detail       json.RawMessage `json:"detail,omitempty"`
	FindingHash  string          `json:"finding_hash"`
}

// errFindingsViewMissing signalisiert, dass die View noch nicht angelegt ist
// (Migration portfolio-020 nicht appliziert) — die Route macht daraus 503.
var errFindingsViewMissing = errors.New("View portfolio.steward_findings fehlt — Migration schema/portfolio-020-steward-findings.sql ausfuehren (Deploy)")

// queryStewardFindings liest die View, optional nach Klasse gefiltert. detailed
// steuert die Projektion (concise = ohne firma/stage/detail). Fehlt die View,
// kommt errFindingsViewMissing zurueck (handlungsleitend).
func queryStewardFindings(ctx context.Context, p *pgxpool.Pool, klasse string, detailed bool) ([]StewardFinding, error) {
	q := `SELECT klasse, initiative_id, titel, firma, stage, detail, finding_hash
	        FROM portfolio.steward_findings`
	var args []any
	if klasse != "" {
		q += ` WHERE klasse = $1`
		args = append(args, klasse)
	}
	q += ` ORDER BY klasse, initiative_id`

	rows, err := p.Query(ctx, q, args...)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" { // undefined_table
			return nil, errFindingsViewMissing
		}
		return nil, err
	}
	defer rows.Close()

	out := []StewardFinding{}
	for rows.Next() {
		var f StewardFinding
		var detail []byte
		if err := rows.Scan(&f.Klasse, &f.InitiativeID, &f.Titel, &f.Firma, &f.Stage, &detail, &f.FindingHash); err != nil {
			return nil, err
		}
		if detailed {
			f.Detail = json.RawMessage(detail)
		} else {
			f.Firma, f.Stage = "", "" // concise: weglassen (omitempty)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func cmdStewardFindings() *cobra.Command {
	var klasse, format string
	c := &cobra.Command{
		Use:   "steward-findings",
		Short: "Findings-Surface fuer das Urteil (View steward_findings) — Menschen/Debug",
		Long: `Liest portfolio.steward_findings (dieselbe Query wie GET /api/steward/findings).
Klassen: dup-kandidat, parent-check, tier-los, now-ohne-evidenz, watching-ohne-deploy.

Beispiele:
  master-kanban steward-findings
  master-kanban steward-findings --klasse dup-kandidat --format detailed`,
		RunE: func(cmd *cobra.Command, args []string) error {
			detailed := format == "detailed"
			findings, err := queryStewardFindings(context.Background(), connect(), klasse, detailed)
			if err != nil {
				return err
			}
			if len(findings) == 0 {
				fmt.Println("keine Findings.")
				return nil
			}
			for _, f := range findings {
				fmt.Printf("%-22s %-34s %s\n", f.Klasse, f.InitiativeID, truncate(f.Titel, 48))
				if detailed && len(f.Detail) > 0 {
					fmt.Printf("    firma=%s stage=%s detail=%s\n", f.Firma, f.Stage, string(f.Detail))
				}
				fmt.Printf("    hash=%s\n", f.FindingHash)
			}
			fmt.Printf("\n%d Finding(s).\n", len(findings))
			return nil
		},
	}
	c.Flags().StringVar(&klasse, "klasse", "", "nur eine Klasse (dup-kandidat|parent-check|tier-los|now-ohne-evidenz|watching-ohne-deploy)")
	c.Flags().StringVar(&format, "format", "concise", "concise|detailed")
	return c
}

// truncate kuerzt s auf max Runen (fuer die CLI-Tabelle).
func truncate(s string, max int) string {
	if len([]rune(s)) <= max {
		return s
	}
	return string([]rune(s)[:max-1]) + "…"
}

// findingsFormat normalisiert den format-Query-Parameter: alles ausser
// "detailed" ist concise (sicherer Default).
func findingsFormat(q string) bool {
	return strings.EqualFold(q, "detailed")
}
