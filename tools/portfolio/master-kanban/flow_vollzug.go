package main

// flow_vollzug.go — die gemeinsame Vollzugs-Schicht des Flow-Managers
// (mk-verwalter-vollzug-PRD, WP1). Hier liegen die reinen Entscheidungs-Kerne
// (ohne DB, damit testbar) und die DB-tragenden Wrapper, die sowohl der
// serve-Listener (main.go, /api/events) als auch der flow_manager-Sweep
// benutzen. Ein Regel-Satz, eine Quelle — kein doppelter stageRank mehr.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// stageOrder ordnet die Board-Stages linear. Bewusst identisch zur früheren
// Inline-Map im /api/events-Listener (idea<soon<now<watching<done), damit das
// Refactoring das Verhalten nicht verschiebt.
var stageOrder = map[string]int{"idea": 0, "soon": 1, "now": 2, "watching": 3, "done": 4}

// stageRank liefert den Rang einer Stage (unbekannt = 0, wie die alte Map).
func stageRank(stage string) int { return stageOrder[stage] }

// stageProposalDecision entscheidet OHNE DB, ob ein Stage-Vorschlag vollzogen
// wird. Reihenfolge der Guards = Regeln 1/2/5 des PRD:
//   - halt  ⇒ Not-Aus, nur Vorschlag (Regel 5)
//   - locked ⇒ menschlich gepinnt, nur Vorschlag (Regel 1)
//   - Ziel nicht echt vorwärts ⇒ kein Auto-Move (Regel 2)
//
// Rückgabe: move + maschinenlesbarer reason (halted/locked/not-forward/moved).
func stageProposalDecision(halt, locked bool, currentStage, targetStage string) (bool, string) {
	if halt {
		return false, "halted"
	}
	if locked {
		return false, "locked"
	}
	if stageRank(targetStage) <= stageRank(currentStage) {
		return false, "not-forward"
	}
	return true, "moved"
}

// applyStageProposal ist die gemeinsame Vollzugs-Funktion für evidenzbasierte
// Stage-Übergänge. Seit WP2 (mk-flow-manager-haertung) gilt: erst entscheiden
// (und ggf. vollziehen), DANN das stage_proposed-Event mit dem ERGEBNIS
// (outcome: moved/locked/halted/not-forward/propose-only) schreiben — die
// alte Reihenfolge protokollierte nur "wollte", nie "durfte nicht".
// Identische Vorschlags-Stände (ziel+outcome+evidenz) werden NICHT wiederholt
// (Proposal-Delta-Gate): eine gepinnte Karte sammelte vorher ~326 identische
// Events/Tag. Ändert sich irgendetwas (Lock gelöst, neue Evidenz), schreibt
// der nächste Sweep wieder.
//
// proposeOnly=true (Regel 3, quantbot: Live-Geld-Nähe) macht den Vorschlag
// sichtbar (Event mit propose_only:true + reason='propose-only (quantbot)'),
// führt aber NIE das UPDATE aus — der Listener (main.go) ignoriert diese Events
// spiegelbildlich (listenerShouldMove), sonst würde er den bewusst nicht
// vollzogenen Vorschlag doch bewegen.
//
// Nur für serverseitige Aufrufer (Sweep) gedacht: der /api/events-Listener
// teilt sich stageProposalDecision, schreibt das Event aber selbst über die
// generische Ingest-Route (kein Doppel-Event).
func applyStageProposal(ctx context.Context, p *pgxpool.Pool, initiativeID, targetStage string, evidence map[string]any, actor string, proposeOnly bool) (bool, string, error) {
	// 1. Entscheidung + ggf. Vollzug — das Ergebnis gehoert ins Event.
	moved := false
	outcome := "propose-only"
	if !proposeOnly {
		halt := os.Getenv("PORTFOLIO_STEWARD_HALT") == "1"
		var currentStage string
		var locked bool
		if err := p.QueryRow(ctx,
			`SELECT stage, COALESCE(stage_locked_by_human, false) FROM portfolio.initiative WHERE id=$1`,
			initiativeID).Scan(&currentStage, &locked); err != nil {
			return false, "", fmt.Errorf("Karte %s lesen: %w", initiativeID, err)
		}
		var move bool
		move, outcome = stageProposalDecision(halt, locked, currentStage, targetStage)
		if move {
			// Das moved-Event schreibt der DB-Trigger (notify_stage_change) beim
			// Stage-UPDATE — ein direkter INSERT hier waere ein Doppel-Event. Was
			// dem Trigger fehlte, war die Attribution (actor=current_user, also
			// immer der DB-User): der transaktionslokale GUC portfolio.actor
			// (portfolio-030) stempelt den echten Verursacher in die Historie.
			tx, err := p.Begin(ctx)
			if err != nil {
				return false, "", fmt.Errorf("Stage-Update %s: Tx beginnen: %w", initiativeID, err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, `SELECT set_config('portfolio.actor', $1, true)`, actor); err != nil {
				return false, "", fmt.Errorf("Stage-Update %s: actor setzen: %w", initiativeID, err)
			}
			if _, err := tx.Exec(ctx,
				`UPDATE portfolio.initiative SET stage=$2 WHERE id=$1`, initiativeID, targetStage); err != nil {
				return false, "", fmt.Errorf("Stage-Update %s → %s: %w", initiativeID, targetStage, err)
			}
			if err := tx.Commit(ctx); err != nil {
				return false, "", fmt.Errorf("Stage-Update %s: Commit: %w", initiativeID, err)
			}
			moved = true
		}
	}

	// 2. stage_proposed mit Outcome schreiben — delta-gated ueber den
	//    proposal_hash (Legacy-Events ohne Hash zaehlen als Aenderung).
	hash := proposalHash(targetStage, outcome, evidence, proposeOnly)
	if lastProposalHash(ctx, p, initiativeID) != hash {
		payload := map[string]any{
			"stage": targetStage, "evidence": evidence, "actor": actor,
			"outcome": outcome, "proposal_hash": hash,
		}
		if proposeOnly {
			payload["propose_only"] = true
			payload["reason"] = "propose-only (quantbot)"
		}
		payloadBytes, _ := json.Marshal(payload)
		if _, err := p.Exec(ctx, `
			INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, payload, actor, at)
			VALUES ($1, 'stage_proposed', 'flow_manager', $2, $3, now())
		`, initiativeID, string(payloadBytes), actor); err != nil {
			if moved {
				// Der Move IST vollzogen (Trigger-Event existiert) — nur loggen.
				fmt.Fprintf(os.Stderr, "  ⚠ stage_proposed-Event für %s nicht geschrieben: %v\n", initiativeID, err)
			} else {
				return false, "", fmt.Errorf("stage_proposed-Event für %s schreiben: %w", initiativeID, err)
			}
		}
	}
	return moved, outcome, nil
}

// proposalHash bildet den Delta-Gate-Hash eines Vorschlags-Stands: gleiche
// (ziel, outcome, evidenz, propose_only) ⇒ gleicher Hash ⇒ kein neues Event.
// json.Marshal sortiert Map-Keys — die Evidenz-Kodierung ist deterministisch.
func proposalHash(targetStage, outcome string, evidence map[string]any, proposeOnly bool) string {
	evBytes, _ := json.Marshal(evidence)
	sum := sha256.Sum256([]byte(targetStage + "\x00" + outcome + "\x00" + string(evBytes) + "\x00" + fmt.Sprintf("%v", proposeOnly)))
	return hex.EncodeToString(sum[:])
}

// lastProposalHash liest den proposal_hash des juengsten stage_proposed-Events
// einer Karte (leer bei Legacy-Events ohne Hash oder ohne Vorgaenger).
func lastProposalHash(ctx context.Context, p *pgxpool.Pool, initiativeID string) string {
	var h string
	_ = p.QueryRow(ctx, `
		SELECT COALESCE(payload->>'proposal_hash','')
		  FROM portfolio.initiative_event
		 WHERE initiative_id=$1 AND kind='stage_proposed'
		 ORDER BY at DESC LIMIT 1
	`, initiativeID).Scan(&h)
	return h
}

// listenerShouldMove entscheidet OHNE DB, ob der /api/events-Listener (main.go)
// einen eingehenden stage_proposed-Vorschlag vollzieht. propose_only-Vorschläge
// (Regel 3, quantbot) werden NIE vollzogen — sonst führte der Listener den
// bewusst nicht-vollzogenen Vorschlag doch aus. Sonst gilt dieselbe
// Vorwärts/Lock-Regel wie im Sweep (halt=false: der Not-Aus greift nur im
// Sweep-Vollzug; der externe Ingest-Weg bleibt unverändert).
func listenerShouldMove(proposeOnly, locked bool, currentStage, targetStage string) bool {
	if proposeOnly {
		return false
	}
	move, _ := stageProposalDecision(false, locked, currentStage, targetStage)
	return move
}

// doneEvidence bündelt die Belege für den watching→done-Vollzug.
type doneEvidence struct {
	HasDelivery      bool   // plan_item delivered/done ODER *-delivery.md verlinkt
	DeployStatus     string // "" = kein eigener deployments-Eintrag; sonst status des jüngsten
	SoftwareInLedger bool   // software-Tag-Value kommt als deployments.service vor
}

// watchingDoneDecision entscheidet OHNE DB, ob eine watching-Karte nach done
// darf. (a) Delivery-Beleg ist Pflicht. (b) Deploy-Beleg je nach Ledger-Lage:
//   - eigener deployments-Eintrag ⇒ jüngster muss 'live' sein
//   - kein eigener Eintrag, aber Software kommt im Ledger vor ⇒ Urteilsfall
//     (watching-ohne-deploy) — nur flaggen, kein Auto-Move
//   - Software gar nicht im Ledger ⇒ Delivery allein reicht (Docs/Konzept)
func watchingDoneDecision(ev doneEvidence) (bool, string) {
	if !ev.HasDelivery {
		return false, "keine-delivery-evidenz"
	}
	if ev.DeployStatus != "" {
		if ev.DeployStatus == "live" {
			return true, "delivery+deploy-live"
		}
		return false, "deploy-nicht-live"
	}
	if ev.SoftwareInLedger {
		return false, "watching-ohne-deploy"
	}
	return true, "delivery-ohne-ledger-software"
}

// gatherDoneEvidence sammelt die drei Belege aus der DB (plan_item/plan_file,
// eigener deployments-Eintrag, software-Tag↔Ledger). Fehler bei einer
// Teilabfrage lassen den jeweiligen Beleg konservativ leer (kein Auto-Move).
func gatherDoneEvidence(ctx context.Context, p *pgxpool.Pool, initiativeID string) (doneEvidence, error) {
	var ev doneEvidence

	// (a) Delivery: plan_item delivered/done ODER *-delivery.md als plan_file.
	if err := p.QueryRow(ctx, `
		SELECT
		  EXISTS(SELECT 1 FROM portfolio.plan_item
		           WHERE initiative_id=$1 AND status IN ('delivered','done'))
		  OR
		  EXISTS(SELECT 1 FROM portfolio.initiative_link
		           WHERE initiative_id=$1 AND kind='plan_file' AND ref LIKE '%-delivery.md')
	`, initiativeID).Scan(&ev.HasDelivery); err != nil {
		return ev, fmt.Errorf("delivery-evidenz für %s: %w", initiativeID, err)
	}

	// (b) Deploy-Beleg: jüngster eigener deployments-Eintrag der Karte.
	var status string
	err := p.QueryRow(ctx, `
		SELECT status FROM portfolio.deployments
		 WHERE initiative_id=$1
		 ORDER BY deployed_at DESC NULLS LAST
		 LIMIT 1
	`, initiativeID).Scan(&status)
	if err == nil {
		ev.DeployStatus = status
	}
	// kein Eintrag ⇒ DeployStatus bleibt "" (pgx: ErrNoRows), das ist gewollt.

	// (c) Software-Tag-Value kommt (case-insensitiv) als deployments.service vor?
	if err := p.QueryRow(ctx, `
		SELECT EXISTS(
		  SELECT 1 FROM portfolio.initiative_tag t
		    JOIN portfolio.deployments d ON lower(d.service) = lower(t.value)
		   WHERE t.initiative_id=$1 AND t.kind='software'
		)
	`, initiativeID).Scan(&ev.SoftwareInLedger); err != nil {
		return ev, fmt.Errorf("software-ledger-abgleich für %s: %w", initiativeID, err)
	}

	return ev, nil
}

// flagsHash bildet einen stabilen Hash über den (sortierten) Flag-Satz einer
// Karte — Grundlage des Event-Delta-Gates: flow_action wird nur geschrieben,
// wenn sich dieser Hash gegenüber dem jüngsten flow_action-Event ändert.
func flagsHash(reasons []string) string {
	sorted := append([]string(nil), reasons...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return hex.EncodeToString(sum[:])
}

// flowActionChanged sagt, ob sich der Flag-Satz gegenüber lastHash geändert hat.
// lastHash == "" (Legacy-Event ohne Hash / kein Vorgänger) gilt als Änderung.
func flowActionChanged(lastHash string, reasons []string) bool {
	return flagsHash(reasons) != lastHash
}

// lastFlowActionHash liest den flags_hash des jüngsten flow_action-Events einer
// Karte (leer, wenn keiner existiert oder das Legacy-Event ihn nicht trug).
func lastFlowActionHash(ctx context.Context, p *pgxpool.Pool, initiativeID string) string {
	var h string
	_ = p.QueryRow(ctx, `
		SELECT COALESCE(payload->>'flags_hash','')
		  FROM portfolio.initiative_event
		 WHERE initiative_id=$1 AND kind='flow_action'
		 ORDER BY at DESC LIMIT 1
	`, initiativeID).Scan(&h)
	return h
}
