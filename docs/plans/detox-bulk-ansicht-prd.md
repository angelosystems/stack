---
title: Detox-Bulk-Ansicht — Triage-UI für 976 lane-lose Beads
slug: detox-bulk-ansicht
status: in-progress
layer: prd
parent_plan: deck-operationalisierung-prd.md
scope: Einmalige Bulk-Triage-UI im Cockpit; markierte Beads in einem Klick archiviert. Voraussetzung bevor Vorschlags-Agent (P3.1) scharfgeschaltet wird.
created: 2026-06-23
review:
  quick: pending
  deep: none
references:
  - /opt/stack/.beads/issues.jsonl  # st-ib5e
---

# Detox-Bulk-Ansicht

## Kontext

Wieder eröffnet am 2026-06-23 nach Triage-Befund: bd-bead st-ib5e war fälschlich auf `closed` gesetzt ohne dass Code in main landete. Vorheriger Workspace `vk/5015-sol-st-ib5e` enthielt andere Features (Karten-Drawer, Lane-Capacity), nicht die geforderte Bulk-UI.

## Problem

~976 lane-lose Beads aus dem Altbestand machen das Cockpit unbenutzbar. Eine pro-Karten-Triage skaliert nicht. Backlog-Zähler bleibt rot. Vorschlags-Agent (P3.1) kann nicht scharf, weil er auf einem riesigen Rauschen aufsetzen müsste.

## Ziel

Cockpit-View für Mehrfachauswahl + Archiv-Button + Filter (Alter, Firma, Präfix). 50 Alt-Beads in einem Klick weg → Backlog-Zähler sinkt entsprechend.

## Akzeptanz

- Filter `Alter > 30d AND Firma = X AND Präfix = Y` liefert N Treffer
- "Alle markieren" → Bulk-Archive-API-Call schickt N IDs
- Backend `POST /api/beads/bulk-archive { ids: [...] }` archiviert + emittiert `bead.archived`-Events
- Backlog-Zähler aktualisiert sich edge-triggered via SSE/WS

## Out of Scope

- Auto-Triage (das ist P3.1)
- Un-Archive-UI (irreversibel via Archive im ersten Wurf)
- Cross-Rig-Bulk (nur stack-rig in v1)

## Architektur-Skizze (LTR)

```
Cockpit-View ──▶ Filter ──▶ Multi-Select ──▶ POST /api/beads/bulk-archive ──▶ bd close (loop) ──▶ SSE event ──▶ Cockpit-Refresh
```

**Warum sequenzieller bd-close-Loop, nicht Batch/Queue?** bd-CLI bietet keine native Batch-API (`bd close` ist single-id-only). Eine asynchrone Queue (z.B. Redis-Job) wäre überdimensioniert für eine einmalige Detox-Aktion. Ein Single-Transaction-DELETE auf dolt/postgres würde bd-CLI-Hooks (audit, export-state) umgehen — verworfen.

## Offene Fragen

- **Idempotenz bei Teilfehler**: wenn `bd close` für 3 von 50 Beads fehlschlägt — zeigt Frontend die 3 als "failed" und der User kann retry? Oder werden alle 50 wieder neu markiert? Vorschlag: failed-Liste im Response, Frontend bietet "nochmal versuchen" nur für die 3.
- **SSE-Verbindungsabbruch während Loop**: Backend macht weiter, Frontend kriegt's nicht mit → State-Drift. Mitigation: Backend persistiert Run-ID, Frontend kann via GET `/api/beads/bulk-archive/<run-id>` final-Status abrufen.

## Reversibilität

`bd reopen <id>` existiert (siehe st-ib5e heute manuell reopened) — der Close ist prinzipiell reversibel. **Bewusst kein Undo in der UI** weil: (a) der Bestätigungs-Dialog mit Liste-der-ersten-5 ist die Sicherheits-Schicht, (b) Mass-Reopen wäre die nächste Detox-Operation (gleiches UI-Pattern, wenn nötig später), (c) Audit-Trail über `bd reopen` ist sauberer als ein opaker UI-Undo.

## Risiken

- **Falsch-Archivierung**: einmal weg ist weg (UI-mäßig). Mitigation: Bestätigungs-Dialog "X Beads archivieren?" mit Liste der ersten 5; Recovery via `bd reopen` möglich aber nicht UI-exposed.
- **Performance bei 1000+ Beads**: Bulk-Archive sequenziell könnte 30s+ dauern. Mitigation: progress-bar + Backend-Streaming

## Beads-Generierung (post-approval)

- **E1: Backend bulk-archive Endpoint** — Done wenn: `POST /api/beads/bulk-archive {ids:[...]}` archiviert N Beads via `bd close` loop, gibt `{archived: N, failed: [...]}` zurück, emittiert `bead.archived` SSE-Event pro Bead.
- **E2: Cockpit Multi-Select-UI** — Done wenn: in Card-Liste Checkbox pro Bead, Shift-Click für Range-Select, "Alle markieren"-Knopf, Bulk-Action-Toolbar erscheint bei N>0 Selected.
- **E3: Filter-Knöpfe (Alter/Firma/Präfix)** — Done wenn: 3 Filter-Dropdowns (Alter ">30d/>90d/>180d", Firma-multi-select, Präfix-Text-Filter) reduzieren Card-Liste reaktiv, Filter-State im URL-Query gespeichert.
- **E4: Bestätigungs-Dialog + Progress-UI** — Done wenn: Klick auf "Archivieren" zeigt Modal "X Beads archivieren? (erste 5: ...)", Progress-Bar während Backend-Loop, Fehler-Liste am Ende falls failed>0.
- **E5: E2E-Test mit 50 markierten Beads** — Done wenn: Playwright-Test in `tests/e2e/detox-bulk.spec.ts` läuft grün und deckt ab: Filter "Alter>30d" → "Alle markieren" → "Archivieren" → Backlog-Zähler sinkt um exakte Treffer-Zahl. (Manueller Smoke ist Voraussetzung für E5-Start, aber nicht das Done-Kriterium.)

## Reviewer-Verdict — quick (glm-5.1) — 2026-06-23

**Verdict:** `approved`

## Reviewer-Verdict — quick (glm-5.1) — 2026-06-23

**Verdict:** `approved-with-notes`

Solider PRD-Draft mit klarer Problem-Begründung (976 lane-lose Beads, belegte Blockade des Vorschlags-Agents), plausibler Out-of-Scope-Abgrenzung und durchgängig überprüfbaren Done-Kriterien pro Arbeitspaket. Architektur-Skizze ist vorhanden, Alternativen werden aber nicht explizit verworfen; offene Fragen sind ebenfalls nicht separat gelistet.

**Findings:**
- [minor] **Keine explizit verworfenen Architektur-Alternativen** — Die Architektur-Skizze zeigt den gewählten Pfad (sequenzielles bd close + SSE), aber Bulk-Alternativen (z.B. Batch-Endpoint auf bd-Ebene, asynchrone Queue, Single-Transaction-DELETE) werden nicht einmal erwähnt, geschweige denn begründet verworfen. Bei einer irreversiblen Massenoperation wäre das wünschenswert.
- [minor] **Keine Sektion für offene Fragen** — Der Draft hat keine explizite Liste offener Fragen. Mindestens die Frage nach Undo/Rate-Limiting/Idempotenz bei SSE-Ausfällen während der Bulk-Loop wäre erwartbar.
- [minor] **Reversibilität nur implizit adressiert** — Bei Out-of-Scope steht 'irreversibel via Archive im ersten Wurf' und bei Risiken wird ein Bestätigungs-Dialog beschrieben. Eine kurze Einschätzung, ob bd close prinzipiell reversibel ist (reopen) und warum bewusst darauf verzichtet wird, fehlt.
- [minor] **E5 Done-Kriterium mischelt manuellen Test** — E5 formuliert 'Playwright/manueller Test' als Done-Bedingung. Damit ist das Kriterium nicht eindeutig verifizierbar — es sollte sich auf einen der beiden Pfade festlegen.

**Asks:**
- [ ] Füge bei der Architektur-Skizze 1–2 Sätze hinzu, warum der sequenzielle bd-close-Loop gewählt wurde und welche Alternative (z.B. asynchrone Queue oder native Batch-API) bewusst verworfen wurde.
- [ ] Nimm eine kurze 'Offene Fragen'-Sektion auf, mindestens zur Idempotenz/Teilfehler-Handhabung und zum geplanten Umgang mit SSE-Verbindungsabbrüchen während der Loop.
- [ ] Entscheide dich in E5 auf entweder Playwright-Test oder manuellen Test als Done-Kriterium, nicht eine Entweder-Oder-Formulierung.
- [ ] Ergänze bei Out-of-Scope oder Risiken einen Satz zur prinzipiellen Reversibilität von bd close (reopen möglich?) und warum bewusst kein Undo angeboten wird.

## Reviewer-Verdict — quick (glm-5.1) — 2026-06-23

**Verdict:** `approved-with-notes`

Solider PRD-Draft mit klarer Problem-Begründung und vollständiger Konformerfüllung: alle Phasen haben überprüfbare Done-Kriterien, Architektur-Alternativen werden begründet verworfen, offene Fragen und Risiken sind ehrlich gelistet. Es bleiben nur kosmetische Anmerkungen zu inkonsistenter Bezeichnung und Doppelung in der Offene-Fragen-Sektion.

**Findings:**
- [minor] **Inkonsistente Terminologie: Archive vs. Close** — PRD spricht durchgehend von 'Bulk-Archive' und 'Archivieren' in UI/API, beschreibt aber als Mechanismus 'bd close'. Das ist entweder ein Naming-Mismatch oder die Begriffe meinen unterschiedliche Dinge — sollte geklärt werden.
- [minor] **Idempotenz-Frage erscheint doppelt** — Die Idempotenz-/Teilfehler-Problematik taucht sowohl unter 'Offene Fragen' als auch indirekt im E1-Done-Kriterium (failed-Liste im Response) auf. E1 präjudiert damit eine der offenen Fragen schon als gelöst — sollte konsistent zusammengeführt werden.
- [minor] **Performance-Risiko nennt Schwellenwert ohne Begründung** — Im Risiko 'Performance bei 1000+ Beads' wird '30s+' als Schätze genannt. Diese Zahl sollte entweder als erfahrungsbasiert markiert oder entfernt werden, da sie ohne Kontext willkürlich wirkt.

**Asks:**
- [ ] Kläre das Verhältnis von 'Archive' (UI/API-Naming) zu 'bd close' (Mechanismus) — entweder explizit gleichsetzen oder begründen, warum die Begriffe verschieden sind.
- [ ] Führe die Idempotenz-/Teilfehler-Handhabung aus 'Offene Fragen' und dem E1-Done-Kriterium an einer Stelle zusammen, damit keine widersprüchlichen Erwartungen entstehen.
- [ ] Markiere oder entferne die '30s+'-Schätzung im Performance-Risiko, damit klar ist, ob es sich um eine Messung oder eine Vermutung handelt.
