package main

// approval.go — Approval-Store für das Promotion-Gate (sa-deploy-stufen W3).
//
// ENTSCHEID (ehrlich dokumentiert): die bestehende WA-Approval-Schiene bildet
// Paperclip-BOARD-Approvals ab und ist an EINE Paperclip-Company (QuantBot-
// Territorium, tabu) gebunden — SA-Deploy-Freigaben dort einzuhängen wäre eine
// Mandanten-Vermischung. Darum W3 = der im PRD vorgesehene FALLBACK: ein
// root-only Datei-Store. Das promote-Kommando LIEST hier nur; es sendet nie
// selbst nach WhatsApp. Die WA-Anbindung (Remind-Schiene diese pending Files
// nachsenden lassen ODER eine eigene SA-Paperclip-Company) ist ein präziser
// Folge-Punkt, s. PRD-Delivery.
//
// Ein Approval ist eine JSON-Datei <dir>/<app>-<sha>.json (root-only, 0600):
//   { "app": "...", "sha": "...", "approved_by": "...",
//     "approved_at": "RFC3339", "ttl_seconds": 86400, "note": "..." }
// Gültig, wenn: Datei existiert, app+sha stimmen exakt, und now < approved_at
// + ttl_seconds. Frisch-Kriterium (TTL) verhindert, dass eine alte Freigabe
// eine spätere, nicht abgesegnete SHA durchwinkt — die SHA ist Teil des
// Datei-Namens, deckt das strukturell zusätzlich ab.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type approval struct {
	App        string    `json:"app"`
	Sha        string    `json:"sha"`
	ApprovedBy string    `json:"approved_by"`
	ApprovedAt time.Time `json:"approved_at"`
	TTLSeconds int64     `json:"ttl_seconds"`
	Note       string    `json:"note,omitempty"`
}

func (a approval) expiry() time.Time {
	return a.ApprovedAt.Add(time.Duration(a.TTLSeconds) * time.Second)
}

// approvalPath — poka-yoke: EIN kanonischer Pfad, keine relative Variante.
func approvalPath(dir, app, sha string) string {
	return filepath.Join(dir, app+"-"+sha+".json")
}

// checkApproval liest+validiert die Freigabe für (app, sha) zu 'now'. Fehler =
// keine gültige Freigabe (handlungsleitend formuliert); der Aufrufer verweigert
// dann hart (exit 66) OHNE zu deployen.
func checkApproval(dir, app, sha string, now time.Time) (approval, error) {
	p := approvalPath(dir, app, sha)
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return approval{}, fmt.Errorf("kein Approval für %s@%s (Datei fehlt)", app, short(sha))
		}
		return approval{}, fmt.Errorf("Approval %s nicht lesbar: %w", p, err)
	}
	var a approval
	if err := json.Unmarshal(b, &a); err != nil {
		return approval{}, fmt.Errorf("Approval %s nicht parsebar: %w", p, err)
	}
	if a.App != app || a.Sha != sha {
		return approval{}, fmt.Errorf("Approval %s trägt app=%q sha=%q, erwartet %q/%q — verworfen", p, a.App, short(a.Sha), app, short(sha))
	}
	if a.ApprovedAt.IsZero() || a.TTLSeconds <= 0 {
		return approval{}, fmt.Errorf("Approval %s ohne gültiges approved_at/ttl_seconds", p)
	}
	if now.After(a.expiry()) {
		return approval{}, fmt.Errorf("Approval für %s@%s ist abgelaufen (approved_at %s + ttl %ds < now) — neu freigeben",
			app, short(sha), a.ApprovedAt.Format(time.RFC3339), a.TTLSeconds)
	}
	return a, nil
}
