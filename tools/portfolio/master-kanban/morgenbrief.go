package main

// morgenbrief.go — Autopilot Stufe 1, W3 (PRD mk-autopilot-stufe1):
// `mk morgenbrief` baut den taeglichen Verwalter-Brief (deterministisch,
// max ~15 Zeilen) und schickt ihn an Marios WhatsApp-Bridge — dieselbe wie
// wp0-frueh-warnung (127.0.0.1:8765). Empfaenger ist AUSSCHLIESSLICH Mario
// (Sende-Freigabe = PRD-Auftrag 2026-08-02). Leerer Brief ⇒ kein Versand.
// Aufruf via systemd mk-morgenbrief.timer, taeglich 06:30 UTC.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

func cmdMorgenbrief() *cobra.Command {
	var dryRun bool
	c := &cobra.Command{
		Use:   "morgenbrief",
		Short: "Taeglicher Board-Brief an Mario (WhatsApp): Deltas, Staus, deine Klicks heute",
		RunE: func(cmd *cobra.Command, args []string) error {
			p := connect()
			brief, leer := buildMorgenbrief(context.Background(), p)
			if leer {
				fmt.Println("Brief waere leer — kein Versand (SC3-Regel).")
				return nil
			}
			fmt.Println(brief)
			if dryRun {
				fmt.Println("\n[dry-run] kein Versand.")
				return nil
			}
			return sendeWhatsApp(brief)
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "Brief nur drucken, nicht senden")
	return c
}

// buildMorgenbrief sammelt die Kennzahlen; leer=true, wenn es NICHTS zu
// sagen gibt (keine Deltas, keine Klicks noetig).
func buildMorgenbrief(ctx context.Context, p *pgxpool.Pool) (string, bool) {
	var fertig24, neu24, autoMoves24, autoReports24 int
	_ = p.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE kind='moved' AND to_stage='done'),
		0, count(*) FILTER (WHERE kind='moved' AND actor='flow-manager'),
		count(*) FILTER (WHERE kind='commented' AND actor='delivery-schreiber')
		FROM portfolio.initiative_event WHERE at > now()-interval '24 hours'`).
		Scan(&fertig24, &neu24, &autoMoves24, &autoReports24)
	_ = p.QueryRow(ctx, `SELECT count(*) FROM portfolio.initiative
		WHERE created_at > now()-interval '24 hours' AND archived_at IS NULL`).Scan(&neu24)

	var lanePending, marioFindings int
	_ = p.QueryRow(ctx, `SELECT count(*) FILTER (WHERE klasse='lane-pending'), count(*)
		FROM portfolio.steward_findings`).Scan(&lanePending, &marioFindings)

	var aeltesteID, aeltesteTitel string
	var aeltesteTage int
	_ = p.QueryRow(ctx, `SELECT i.id, left(i.title,40),
		floor(extract(epoch FROM now()-COALESCE((SELECT max(e.at) FROM portfolio.initiative_event e
			WHERE e.initiative_id=i.id AND e.kind='moved' AND e.to_stage='now'), i.created_at))/86400)::int
		FROM portfolio.initiative i
		WHERE i.archived_at IS NULL AND i.stage='now' AND i.id NOT LIKE 'fabrik-%'
		ORDER BY 3 DESC LIMIT 1`).Scan(&aeltesteID, &aeltesteTitel, &aeltesteTage)

	// Wartende Delivery-Commits (lokal committet, Push steht aus) je Repo.
	var unpushed []string
	for _, repo := range deliveryRepoWhitelist {
		out, err := exec.Command("git", "-C", repo, "log", "--oneline", "@{u}..HEAD", "--grep", "delivery-schreiber:").Output()
		if err == nil && len(strings.TrimSpace(string(out))) > 0 {
			n := len(strings.Split(strings.TrimSpace(string(out)), "\n"))
			unpushed = append(unpushed, fmt.Sprintf("%s (%d)", repo, n))
		}
	}

	if fertig24 == 0 && neu24 == 0 && autoReports24 == 0 && lanePending == 0 && len(unpushed) == 0 && aeltesteTage < 14 {
		return "", true
	}

	var sb strings.Builder
	sb.WriteString("🌅 *Master-Kanban Morgenbrief* " + time.Now().Format("02.01.") + "\n")
	fmt.Fprintf(&sb, "Letzte 24h: %d fertig · %d neu · %d Auto-Moves · %d Auto-Reports\n", fertig24, neu24, autoMoves24, autoReports24)
	sb.WriteString("\n*Deine Klicks heute:*\n")
	if lanePending > 0 {
		fmt.Fprintf(&sb, "⏳ %d Lane-Entscheidungen (Cockpit → Lane-Inbox, Batch-Approve)\n", lanePending)
	}
	if len(unpushed) > 0 {
		fmt.Fprintf(&sb, "📤 Delivery-Commits warten auf Push: %s\n", strings.Join(unpushed, ", "))
	}
	if aeltesteTage >= 14 {
		fmt.Fprintf(&sb, "🐌 Älteste In-Arbeit: %s (%dT) — weiter/parken/kippen?\n", aeltesteTitel, aeltesteTage)
	}
	fmt.Fprintf(&sb, "\n🩺 %d offene Befunde gesamt · Board: master.stayawesome.app", marioFindings)
	return sb.String(), false
}

// sendeWhatsApp schickt an die lokale Bridge (wp0-Konvention). Fehler sind
// laut, nie stumm: Exit != 0 laesst den Timer-Run rot werden (Journal).
func sendeWhatsApp(text string) error {
	url := envOr("WA_URL", "http://127.0.0.1:8765/api/send")
	jid := envOr("WA_JID", "4915153509052@s.whatsapp.net")
	body, _ := json.Marshal(map[string]string{"recipient": jid, "message": text})
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("WA-Bridge nicht erreichbar (%s): %w — Brief steht im Journal, morgen neuer Versuch", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("WA-Bridge HTTP %d — Brief steht im Journal", resp.StatusCode)
	}
	fmt.Fprintln(os.Stderr, "✓ Morgenbrief an Mario gesendet")
	return nil
}
