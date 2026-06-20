---
title: vk-Sage Erstfälle — 4 Hänger triagiert (rituale + 3 Deck-Beads)
slug: vk-sage-erstfaelle
status: delivered
layer: delivery
parent_plan: docs/plans/vk-sage-workspace-steward-prd.md
sapling: st-1ixrt
created: 2026-06-20
references:
  - docs/plans/vk-sage-workspace-steward-prd.md
  - docs/plans/vk-sage-calibration.md
  - docs/plans/deck-operationalisierung-prd.md
---

# vk-Sage Erstfälle — manuell ausgeführtes Phase-1-Urteil

## Kontext

Vier pausierte Workspaces (rituale, `sol-st-yozd`, `sol-st-1bpf`, `sol-st-ib5e`)
wurden am 20.06.2026 manuell archiviert, um Slots freizugeben. Das Kalibrierungs-Gate
([vk-sage-calibration.md](vk-sage-calibration.md)) hat ihre Sage-Klassifikation
gegen Mensch-Urteil verifiziert (4/4 Match, 100%). Phase 2 (autonomer Mutator)
ist noch nicht implementiert — dieses Dokument hält die **manuell durchgeführte
Sage-Entscheidung pro Bead** fest, gemäß PRD L3 (heal / close-as-done /
escalate). Damit hängt kein Workspace ungeprüft pausiert herum.

Wichtig (PRD-Maxime): *Ein ruhender Workspace ist ein unerledigtes Ziel.*
Blindes Schließen ist nicht erlaubt. Jeder der vier wurde diagnostiziert und
einer der drei Sage-Aktionen zugewiesen.

## Entscheidungs-Matrix

| Workspace | Bead | Sage-Klasse (aus Kalibrierung) | Entscheidung | Begründung |
|---|---|---|---|---|
| `935D9575` v3s34-rituale | — (kein Bead) | broken worktree / kein Bead | **close (workspace-only)** | Worktree kein gültiges Git-Repo, keine Bead-Zuordnung → PRD-Regel: „Keine Heilung von Workspaces ohne Bead" (nur archivieren, nie re-dispatchen). Archivierung am 20.06.2026 vollzogen. |
| `B8427650` sol-st-ib5e | `st-ib5e` Detox-Bulk-Ansicht | no-commits-exit1 + Ziel schon erledigt | **close-as-done** | Detox-Konzept ist im master-kanban-Backend operationalisiert (`checkFirmaProposals` gated auf `st-ib5e.status='closed'`, siehe `tools/portfolio/master-kanban/main.go:2670`). Eigenständige „Detox-Bulk-Ansicht" als separate UI ist out-of-scope der Deck-Operationalisierung-PRD R1-R6 — das beoperationalisierte Backend-Gate genügt dem deck-internen Zweck (Vorschlagsagent-Sperre bis Detox sauber). Bead → `closed` (close_reason `sage-already-done`). |
| `05021F1F` sol-st-yozd | `st-yozd` R1 Triage-Knöpfe + Neue-Idee | no-commits-exit1 + Arbeit echt offen | **escalate** | Retry-Budget verbraucht: 4-5× re-dispatcht, jedes Mal `no-commits-exit1`. PRD §L4 sagt: nach N=2 erfolglosen Heilungen STOP+Eskalation, kein weiterer Auto-Retry. UI-Lücke verifiziert: Backlog-Tab hat heute **einen** „Triage…"-Knopf (öffnet Dispatch-Drawer), nicht die drei expliziten Buttons (→ Karte / → Hacker / ✗ Archiv) + `[+ Neue Idee]`-Header-Knopf, die R1 fordert (`cockpit/cockpit.html:229`). Eskalation: Mensch-Triage (re-scope nötig — Drawer-vs-Buttons-Designfrage muss vor Re-Dispatch geklärt werden). |
| `64D07879` sol-st-1bpf | `st-1bpf` R5 Lane-Badges | no-commits-exit1 + Arbeit echt offen | **escalate** | Retry-Budget verbraucht (>2 Fehlschläge). UI-Lücke: cockpit hat firma-Stripes (`lane-stripe-solartown` etc.) — aber **nicht** die R5-Exec-Lane-Badges (⚡ Hacker / 🏭 Solartown / ○ untriagiert) je Karte und je Backlog-Eintrag. Grep auf `cockpit/cockpit.html` für `⚡ Hacker`/`🏭 Solartown` → 0 Treffer. Eskalation: Mensch-Triage (Source-of-truth für Lane-Mehrheit pro Karte ist im PRD nicht hart spezifiziert — vor Re-Dispatch festschreiben). |

## Befolgte PRD-Regeln

- **L3-Routing** angewendet (close-as-done / re-dispatch / escalate je Klasse).
- **L4 Retry-Budget**: `st-yozd` und `st-1bpf` haben N>2 erreicht → STOP, kein
  weiterer Auto-Retry, Mensch-Eskalation.
- **Nicht-Ziel**: keine Heilung von Workspaces ohne Bead — `rituale` nur archiviert.
- **R-A Mitigation**: `close-as-done` für `st-ib5e` ist mit verifizierbarem
  Backend-Check belegt (Code-Referenz `main.go:2670`), nicht bloß LLM-Meinung.

## Folge-Aktionen (manuell, ausserhalb dieses Saplings)

1. Bead `st-ib5e` schliessen mit `close_reason=sage-already-done`.
2. Beads `st-yozd` und `st-1bpf` bleiben `BLOCKED` mit Label
   `vk-paused:no-commits-exit1`; werden in nächster Mario-Triage neu gescopet
   (Buttons-vs-Drawer-Frage für R1; Lane-Source-Definition für R5).
3. Workspace `935D9575` (rituale) ist bereits archiviert — keine weitere
   Aktion.

## Phase-1 Done-Check (PRD-SC4 Auszug)

> SC4: Jede Sage-Aktion erscheint als Board-Event auf der Bead-Initiative
> (keine stille Aktion).

Da Phase 1 read-only war und der autonome Sage-Mutator nicht läuft, ersetzt
dieses Delivery-Dokument das Board-Event für die vier Erstfälle. Sobald
Phase 2 scharf ist, emittiert der Sage `kind=sage_action`-Events automatisch.

---

Letztes Update: 2026-06-20 (sapling st-1ixrt).
