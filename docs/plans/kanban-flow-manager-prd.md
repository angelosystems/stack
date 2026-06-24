---
title: Kanban-Flow-Manager — übergeordneter Karten-Steward
slug: kanban-flow-manager
status: approved-with-notes
layer: prd
parent_plan: /opt/stack/docs/plans/master-kanban.md
scope: Ein autonomer Flow-Overseer auf Karten-Ebene — misst Alterung/Stagnation/Veraltung aus vorhandenen Board-Daten, diagnostiziert das Warum, schlägt Flow-Aktionen vor (re-triggern/promoten/review/archivieren) und liefert einen periodischen Board-Review. Der card-altitude Zwilling der vk-Sage.
created: 2026-06-21
review:
  quick: auto
  deep: spec-panel
  panel-mode: critique
  panel-focus: [architecture, requirements]
references:
  - /opt/stack/docs/plans/master-kanban.md
  - /opt/stack/docs/plans/vk-sage-workspace-steward-prd.md
  - /opt/stack/docs/plans/capture-completeness-prd.md
  - /opt/stack/docs/plans/master-kanban-dispatch-prd.md
---

# Kanban-Flow-Manager — übergeordneter Karten-Steward

## Problem

Das Board erfasst Karten, aber niemand überwacht den **Fluss**: Karten landen
drin, doch es fehlt der übergeordnete Blick auf — wie lange liegt eine Karte
schon in einer Stage, warum bewegt sie sich nicht, ist sie inzwischen veraltet,
was muss neu angestoßen werden. Belegt in dieser Session: `qb-backtest-gate`
lag in IDEA, obwohl real 40% gebaut; die pUSD-Karte hing in NOW, obwohl längst
erledigt; Karten promoten nicht von selbst, wenn ihre Beads alle closed sind.

Das ist eine **eigene Schicht**, klar abgegrenzt von dem, was wir schon haben:
- **vk-Sage** heilt hängende *Workspaces* (Ausführungs-Ebene).
- **capture-completeness** sorgt, dass jede Arbeit eine *Karte* hat (Abdeckung).
- **mcp-copilot** ist die KI, mit der man pro Karte *spricht* (interaktiv).
- **Hier fehlt:** der autonome Overseer für den *Karten-Fluss*.

## Ziel

Ein autonomer Flow-Manager — der **card-altitude Zwilling der vk-Sage** (gleiches
Muster: erkennen → diagnostizieren → vorschlagen/eskalieren, eine Ebene höher).
Er rechnet Alterung/Stagnation/Veraltung aus den **schon vorhandenen** Karten-
Daten, diagnostiziert das Warum (GLM-5.1) und liefert einen periodischen
Board-Review mit konkreten Vorschlägen.

## Nicht-Ziele

- **Kein vk-Sage-Duplikat.** Stockt eine Karte, weil ihr *Workspace* gescheitert
  ist, ist das vk-Sages Job — der Manager flaggt nur das Karten-Symptom und
  reicht an vk-Sage weiter (R-D), handelt nicht doppelt.
- **Keine Abdeckung** (das ist capture-completeness) — der Manager setzt voraus,
  dass Karten existieren.
- **Kein neuer Metrik-Store** — rechnet aus `initiative` + `initiative_event` +
  bead/vk/pr-Counts, die schon da sind.
- **Kein blindes Bewegen/Archivieren** — mutierende Aktionen nur propose→confirm.

## Lösung

### L1 — Flow-Signale (read, aus vorhandenen Daten)
```
Zeit-in-Stage:      now − Stage-Eintritt (aus stage-move-Events)
Aktivitäts-Stille:  now − last_activity   (die Activity-Spark nutzt das schon)
Bead-Fortschritt:   closed/total der verlinkten Beads
WIP-vs-Limit:       Karten-in-NOW vs. wip-Limit (col-now-overflow existiert)
```
Kein neuer Datenpfad — nur Auswertung.

### L2 — Erkennungen
```
Stagnation:    in NOW/SOON, Stille > Schwelle, KEIN aktiver Bead/Workspace
Promote-reif:  alle verlinkten Beads closed, aber Stage nicht weiter
Backlog-Fäule: sehr lange in IDEA, nie bewegt → veraltet?
WIP-Überlauf:  mehr Karten in NOW als das wip-Limit
```

### L3 — Diagnose (das Manager-Hirn)
Pro geflaggter Karte liest GLM-5.1 Kontext + Outcome und benennt das **Warum**:
wartet-auf-Mensch / Workspace-gescheitert / fertig-nicht-promotet / verlassen.
Schwellwert-Flaggen allein reicht nicht — die Diagnose macht den Vorschlag
brauchbar (analog Sage). **Alternative verworfen:** rein regelbasierte Diagnose
ohne LLM — verworfen, weil das Warum (wartet-auf-Mensch vs. Workspace-gescheitert
vs. verlassen) ein Kontext-Urteil braucht, das starre Schwellwerte nicht leisten;
Regeln flaggen nur, erklären nicht.

### L4 — Aktion (propose → confirm, über bestehende Endpoints)
```
stockt + keine aktive Arbeit  →  Re-Dispatch vorschlagen (dispatch-Endpoint) ODER eskalieren
alle Beads closed              →  Stage-Promotion vorschlagen (move-Endpoint)
Backlog-Fäule / veraltet       →  „Review: noch relevant?"-Event, ggf. Archiv-Vorschlag
```
Jede Aktion = ein Karten-Event. Mensch bestätigt jede Mutation per Klick.

#### Stage-Übergangs-Map (nicht-lineare Förderung)
Da die Stages nicht rein linear bzw. sequentiell verlaufen (z.B. kann von verschiedenen Vorstufen gearbeitet werden), ist das genaue Promote-Ziel (`to_stage`) abhängig von der aktuellen Stage der Initiative:
- `idea`       → `soon`       (Idee wird zu einer konkret eingeplanten Initiative)
- `soon`       → `now`        (Eingeplante Initiative wird aktiv in Arbeit genommen)
- `now`        → `watching`   (Aktive Arbeit beendet, Ergebnisse werden beobachtet/verifiziert)
- `watching`   → `done`       (Beobachtung erfolgreich abgeschlossen, Initiative ist done)
- `done`       → (kein weiteres Ziel, bereits am Endpunkt)

**Alternative verworfen:** Auto-Promote bei 100% Bead-Close ohne Confirm —
verworfen, weil „alle Beads closed" nicht immer „Ziel erreicht" heißt (Beads
können als no-changes/Duplikat geschlossen sein, wie diese Session mehrfach
zeigte); ein false-Promote würde unfertige Arbeit als erledigt markieren. Darum
strikt propose→confirm.

### L5 — Board-Review-Digest & Aktive Zustellung (Push-Kanal)
Ein periodischer Lauf erzeugt einen aggregierten **Board-Review-Digest**. Um einen proaktiven Manager zu garantieren, wird dieser Digest nicht nur passiv im Cockpit bereitgestellt, sondern über **aktive Push-Zustellkanäle** zugestellt:

1. **Gas Town Mail (Primärkanal):**
   - **Ziel:** Der Digest wird proaktiv als Markdown-Bericht an den konfigurierten Empfänger (Standard: `mariobrain/`, überschreibbar per `PORTFOLIO_DIGEST_RECIPIENT`) zugestellt.
   - **Befehl:** Nutzt das systemeigene `gt mail send <recipient> -s "🩺 Flow-Manager Board-Review Digest" --stdin` Tool.
   - **Inhalt:** Strukturierter Markdown-Bericht mit aggregierten Metriken (Gesamtzahl geflaggter Karten, Verteilung nach Stagnation/Promote-reif/Backlog-Fäule/WIP-Überlauf) sowie detaillierten Diagnoseergebnissen und vorgeschlagenen Aktionen je Karte.

2. **Dashboard / Console (Ausweichkanal & Liveness):**
   - **Ziel:** Ausgabe im Standard-Output (Journal-Logs) für volle Audit-Sicherheit und Systemüberwachung.

*Telegram wird als Zustellkanal aus Sicherheits- und Datenschutzgründen explizit ausgeschlossen.*

### L6 — Selbst-Liveness + Budget
Der Manager hat einen eigenen Heartbeat (ein ausgefallener Lauf ist sichtbar,
nicht still — Lehre aus capture-completeness) und ein Pro-Karte-Budget: dieselbe
Karte wird nicht jeden Zyklus neu geflaggt (Cooldown gegen Flag-Müdigkeit).

### L7 — Stage-Übergangs-Map (P2.4)
Da die Stages nicht linear sind, wird das Promote-Ziel über eine dynamische Entscheidungsmatrix ermittelt, die die Kapazitätsanzeige (P2.2) nutzt:

| Von Stage | Bedingung | Ziel-Stage | Logik / Begründung |
|---|---|---|---|
| **idea** | Freie Kapazität (idle Polecats > 0 ODER vk-Slots > 0) UND `nowCount < nowLimit` | **now** | Überspringt `soon`, da sofortige Ausführung möglich ist (Pull-System). |
| **idea** | Keine Kapazität ODER `nowCount >= nowLimit` | **soon** | Wandert in die Warteschlange. |
| **soon** | Keine zusätzlichen Bedingungen | **now** | Wird in die aktive Umsetzung überführt. |
| **now** | Keine zusätzlichen Bedingungen | **watching** | Aktive Entwicklung abgeschlossen, Beobachtung von Reviews/Tests. |
| **watching** | Keine zusätzlichen Bedingungen | **done** | Erfolgreich abgeschlossen und archiviert. |
| **done** | Bereits am Ende | *(keine)* | Endzustand. Keine weitere Promotion möglich. |

## Success-Criteria

- SC1: Eine Karte in NOW mit Aktivitäts-Stille > Schwelle und ohne aktiven
  Bead/Workspace erscheint binnen eines Review-Zyklus in der Manager-Ansicht,
  geflaggt „Stagnation: <Diagnose>".
- SC2: Eine Karte, deren verlinkte Beads alle closed sind, deren Stage aber nicht
  weiter ist, wird „promote-reif" geflaggt mit Ein-Klick-Promote-Vorschlag;
  Bestätigung → /api/move → Stage rückt vor + Event.
- SC3: Der periodische Review liefert einen Digest (Top-N stockend / promote-reif
  / veraltet), sichtbar auf dem Board.
- SC4: Jede Manager-Aktion (flag/vorschlag/promote/archiv) ist ein Karten-Event
  (keine stille Aktion).
- SC5: Mutationen sind propose→confirm; der Manager bewegt/archiviert nie eine
  Karte ohne Mensch-Klick.
- SC6: Selbst-Liveness — ein ausgefallener Manager-Lauf ist sichtbar (Heartbeat),
  nicht still.
- SC7: Live-Geld-Schutz — quantbot/Trading-Path-Karten werden nur geflaggt +
  eskaliert, kein autonomer Promote-/Dispatch-Vorschlag in Live-Code.

## Risiken / offene Fragen

- R-A: Schwellwert-Tuning — was ist „zu lange in Stage"? Pro Stage + pro Firma
  konfigurierbar; konservativ starten, nachjustieren. Vor L1 Defaults setzen.
  
  **Spezifikation: Per-Stage-Schwellen-Defaultmodell (P1.2)**
  
  Das Default-Schwellenmodell definiert, nach welcher Dauer von Inaktivität (Aktivitäts-Stille) eine Karte in einer bestimmten Stage als stagnierend oder veraltet (Backlog-Fäule) eingestuft wird.
  
  | Stage | Standard-Schwelle | Begründung & Typ |
  |---|---|---|
  | `now` | **3 Tage** (`3d`) | Active Execution. 3 Tage ohne jegliche Aktivität (Bead/Workspace/Commit) deutet auf Stagnation hin. |
  | `soon` | **14 Tage** (`14d`) | Waiting queue. Längere Liegezeiten sind hier normal, aber nach 2 Wochen Inaktivität sollte nachgehakt werden. |
  | `idea` | **90 Tage** (`90d`) | Backlog-Fäule. Im Idea-Backlog sind Monate legitim; nach 3 Monaten ohne Bewegung gilt die Idee als veraltet. |
  | `watching` | **30 Tage** (`30d`) | Passive Beobachtung. Sollte nach 1 Monat ohne Aktivität evaluiert werden. |
  | `done` | **Inaktiviert** (`0s`) | Abgeschlossene Arbeit altert nicht und löst keine Flow-Aktionen aus. |
  
  *Konfiguration & Priorität:*
  Die Schwellenwerte können flexibel über Umgebungsvariablen überschrieben werden:
  1. `PORTFOLIO_THRESHOLD_<STAGE>_<FIRMA>` (z.B. `PORTFOLIO_THRESHOLD_NOW_STAYAWESOME=12h`)
  2. `PORTFOLIO_THRESHOLD_<STAGE>` (z.B. `PORTFOLIO_THRESHOLD_NOW=5d`)
  3. Der oben spezifizierte Standardwert.
  
  Unterstützte Formate beim Parsing (erweiterte durations): Tage (`d`/`days`), Wochen (`w`/`weeks`), Monate (`mo`/`months`) sowie Standard-Go-Dauer-Formate (`h`, `m`).

- R-B: Diagnose-Qualität — GLM liest das Warum evtl. falsch. Mitigation:
  Diagnose ist beratend; jede Aktion braucht Confirm; niedrige Confidence →
  nur flaggen, kein Aktions-Vorschlag.
- R-C: Flag-Müdigkeit — Digest gruppiert + rankt, zeigt nur Aktionables, dedupt
  Re-Flags per Cooldown (L6).
- R-D: **Abgrenzung zu vk-Sage** — stockt eine Karte wegen eines gescheiterten
  Workspaces, ist das vk-Sages Zuständigkeit. Der Manager erkennt das, flaggt das
  Karten-Symptom und **reicht weiter** (kein Doppel-Handeln). Den Übergabe-Pfad
  explizit definieren.
- R-E: Zeit-in-Stage braucht stage-move-Events; sind die lückenhaft, ist die
  Alterung approximativ — Fallback auf `updated_at`.
- R-F: **Promote-Ziel mehrdeutig** — die Stages sind nicht linear (`idea`/`now`/`soon`/`watching`/`done`).
  
  **Spezifikation: Stage-Übergangs-Map (P2.4)**
  
  Das Modell definiert das eindeutige Promote-Ziel für jede Ausgangs-Stage, um Fehl-Promotions oder Unklarheiten bei automatisierten Vorschlägen zu vermeiden.
  
  | Ausgangs-Stage | Promote-Ziel | Bedeutung des Übergangs & Logik |
  |---|---|---|
  | `idea` | `soon` | **In Warteschlange einreihen.** Die Idee ist triagiert und bereit für die Detail-Ausarbeitung oder Einreihung in die nächste Planungsphase. |
  | `soon` | `now` | **In aktive Entwicklung geben.** Karte rückt in die Spalte für aktive Ausführung vor; Beads werden an den Scheduler/Reactor übergeben. |
  | `now` | `watching` | **In Beobachtung/Abnahme verschieben.** Aktive Umsetzung abgeschlossen (z. B. alle verlinkten Beads closed), wartet auf PR-Review, CI-Durchlauf oder menschliche Abnahme. |
  | `watching` | `done` | **Final abschließen.** Abnahme erfolgreich, Karte wird archiviert/abgeschlossen. |
  | `done` | *Terminal* | **Keine Promotion möglich.** Dies ist der Endzustand. Promotion aus `done` wirft einen Fehler. |

## Phasen (Granularität, keine Zeit)

1. **Flow-Signale + Erkennungen + Manager-Ansicht (read-only, nur flaggen)**
   (Gran. 3) — Done wenn SC1 + SC3 (Flags + Digest sichtbar, keine Mutation).
2. **Diagnose (GLM) + Aktions-Vorschläge (promote/re-trigger) propose→confirm**
   (Gran. 3) — Done wenn SC2 + SC4 + SC5.
3. **Selbst-Liveness + Budget + Live-Geld-Schutz + vk-Sage-Übergabe** (Gran. 2) —
   Done wenn SC6 + SC7 + die R-D-Abgrenzung implementiert ist.

---

> Architektur-Hebel (autonomer Overseer + Diagnose-LLM + Endpoint-Kopplung) →
> Plan-Pipeline. Deep-Tech empfohlen (autonome Karten-Mutation + Abgrenzung zu
> vk-Sage). Kein Bead vor Quick-Verdict.

## Reviewer-Verdict — quick (glm-5.1) — 2026-06-23

**Verdict:** `approved-with-notes`

Ein klar fokussierter, sauber abgegrenzter PRD-Draft. Das Problem ist konkret und mit Beispielen belegt, die Abgrenzung zu Bestandssystemen (vk-Sage, capture-completeness) ist hervorragend herausgearbeitet, und alle Phasen haben überprüfbare Done-Kriterien. Es gibt keine Major-Findings, allerdings fehlen bei den Architektur-Entscheidungen die explizit verworfenen Alternativen.

**Findings:**
- [minor] **Alternativen bei Architektur-Entscheidungen nicht explizit verworfen** — Die Architektur baut auf GLM-5.1 zur Diagnose und auf striktem propose→confirm für Mutationen. Es wird jedoch nicht deutlich, welche anderen Architektur- oder Interaktions-Muster (z.B. regelbasierte Diagnose ohne LLM, oder auto-promote bei 100% Bead-Close ohne Confirm) erwogen und bewusst verworfen wurden.

**Asks:**
- [ ] Ergänze bei der Lösungs-Architektur (L3/L4) kurz 1-2 Sätze zu bewusst verworfenen Alternativen (z.B. 'Bewusst kein Auto-Promote, weil...', 'Bewusst LLM statt statischer Regeln, weil...').

## Reviewer-Verdict — deep-tech (spec-panel critique, focus: architecture/requirements) — 2026-06-23

- **Verdict:** `approved-with-notes`
- **Methode:** /sc:spec-panel critique inline. Panel: Fowler/Newman/Hohpe/Nygard (architecture), Wiegers/Adzic/Cockburn + Crispin (requirements).
- **Gesamt:** Starkes, sauber abgegrenztes PRD. Die Must-Fixes betreffen das emergente Risiko, einen **dritten** autonomen Akteur aufs Board zu setzen.

**MUST-FIX vor Phase-1-Beads (3):**

1. **[Nygard/Newman — MAJOR] Autoritäts-Hierarchie der drei Akteure.** Reactor (dispatcht ready Beads), vk-Sage (heilt Execution) **und** dieser Manager (re-triggert stockende Karten) können alle auf dieselbe Karte wirken. Ohne Vorrang handeln sie doppelt oder gegeneinander. Definieren: **Reactor → vk-Sage → Manager** als Eskalations-Leiter — der Manager ist der oberste Overseer und greift NUR, wenn die unteren Schichten nicht (mehr) engagiert sind (kein aktiver/wartender Workspace, kein offener Reactor-Versuch, nicht in vk-Sages Queue).
2. **[Hohpe — MAJOR] „wartet auf Kapazität" ≠ „stockt".** Eine Karte, deren Bead offen-und-in-der-Schlange ist (wartet auf einen freien Polecat — wie die 78 in dieser Session), **stockt nicht**, sie wartet. Sie als „stagniert → re-dispatch" zu flaggen löst einen Re-Dispatch-Sturm gegen das Kapazitätslimit aus. Der Stagnations-Detektor muss Karten mit gesundem ausstehendem Dispatch ausschließen.
3. **[Fowler/Wiegers — MAJOR] „Promote-reif = alle Beads closed" hängt an vollständiger Bead-Linkage** (capture-completeness), die selbst lückenhaft ist (~40/160 gelabelt). Bis die Abdeckung hoch ist, ist promote-reif verrauscht (eine Karte kann unverlinkte offene Beads haben). Promote-reif an eine Linkage-Confidence koppeln oder die Abhängigkeit explizit machen; propose→confirm mildert, aber das Signal braucht den Vorbehalt.

**NOTES:**
- **[Wiegers] Per-Stage-Schwellen sind das Korrektheits-Fundament** (R-A) — vor L1 das stage-differenzierte Default-Modell festnageln (NOW: Tage; IDEA: Monate legitim), nicht „später tunen".
- [x] **[Adzic] Promote-Ziel mehrdeutig** — die Stages sind nicht linear (idea/now/soon/watching/done). Die Stage-Übergangs-Karte definieren: was heißt „promoten" aus jeder Stage? (Spezifiziert in R-F / Spezifikation: Stage-Übergangs-Map (P2.4))
- **[Cockburn] Digest-Zustellung: push statt pull.** Eine Ansicht, die Mario aktiv aufrufen muss, untergräbt einen *proaktiven* Manager — Zustellkanal spezifizieren (Dashboard/Mail/Fabric, kein Telegram).
- **[Crispin] Den Manager gegen die Staging-Board-Instanz testen** (geseedete Karten in stockend/promote-reif-Zuständen), nicht gegen prod-Karten.
