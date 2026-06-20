---
title: Capture-Completeness — nichts fällt durch
slug: capture-completeness
status: approved-with-notes
layer: prd
parent_plan: /opt/stack/docs/plans/master-kanban.md
scope: Jede Arbeit ist zu einer Initiative rückverfolgbar — und alles ohne Zuhause wird SICHTBAR gemacht statt still gedroppt. Erfassung durch Sichtbarkeit erzwingen, nicht hoffen. Damit hält das Board sein Versprechen "alles, was wir tun, ist erfasst".
created: 2026-06-15
review:
  quick: auto
  deep: spec-panel
  panel-mode: critique
  panel-focus: [requirements, architecture]
references:
  - /opt/stack/docs/plans/master-kanban.md
  - /opt/stack/docs/plans/master-kanban-bead-linkage-prd.md
  - /opt/stack/docs/plans/master-kanban-dispatch-prd.md
  - /opt/stack/docs/plans/master-kanban-mcp-copilot-prd.md
---

# Capture-Completeness — nichts fällt durch

## Problem

Das Board erfasst PRD-große Arbeit gut, darunter leckt es — gemessen 2026-06-15:

- **PRD-Ebene**: alle 4 Session-PRDs sind als Karten da (planfile-adapter). ✓
- **Bead-Ebene**: nur ~40 von 160 stack-Beads tragen `plan:<slug>` → nur die
  linken an eine Karte; der Rest ist **unsichtbar** auf dem Board.
- **Inline-Ebene**: direkte Commits / manuelle Änderungen (z.B. der
  cockpit-Reconcile `08b1119`) werden **nie** zu Bead oder Karte — sie
  hinterlassen nirgends eine Spur.

Drei Leckklassen: **(1) Orphan-Beads** (kein Join-Key), **(2) Inline-/Session-Arbeit**
(kein Bead), **(3) Cross-Rig-Reste** (Registry-Macken). Das Versprechen „alles,
was wir tun, ist im Board erfasst" ist damit still verletzt — und *still* ist das
Schlimmste: man merkt nicht, was fehlt.

## Ziel

**Erfassung auf der richtigen Ebene, erzwungen durch Sichtbarkeit.** Jede
Arbeit ist zu *einer* Initiative rückverfolgbar (als verlinkter Bead oder als
Event); alles ohne Zuhause erscheint sichtbar in einer „braucht Zuhause"-Lane,
statt lautlos zu verschwinden.

```
Initiative (Karte)  = grober Strang  → jeder Strang hat eine Karte
  └─ Bead / Event   = Arbeit darunter → rollt unter die Karte hoch
Leak-Detektor        → was kein Zuhause hat → sichtbare "Unlinked"-Lane (kein Silent-Drop)
```

## Nicht-Ziele

- **Keine Karte pro Mini-Aufgabe.** Initiativen bleiben grob; kleine Arbeit
  rollt als Bead/Event hoch, nicht als eigene Karte (sonst Board-Müll).
- **Kein neuer Issue-Tracker.** bd/beads bleibt SoT für Work-Items; das Board
  aggregiert nur.
- **Kein Zwang, jede Chat-Nachricht zu erfassen** — nur echte Arbeit/Entscheidungen.
- **Keine Erfassung von Arbeit ganz außerhalb des Systems** (manuelle Änderung
  ohne Commit/Bead/Workspace ist tool-seitig nicht fangbar — nur Disziplin).

## Lösung

### L1 — Leak-Detektor + „Unlinked"-Lane (der Kern)
Ein Reconciler findet Arbeit-ohne-Zuhause: (a) Beads ohne `plan:<slug>` bzw.
ohne passende Initiative, (b) vk-Workspaces ohne Initiative-Link, (c) Commits
auf getrackten Repos ohne Bead-Referenz. Ergebnis erscheint als sichtbare
Board-Lane/View **„⚠ Unlinked — braucht Zuhause"**, gruppiert nach Firma.
Der bestehende `--link`-Lauf zählt die Roamer schon — diese Zahl bekommt ein
Gesicht. **Alternative verworfen:** Orphans nur loggen (journalctl) — verworfen,
weil unsichtbar = wird nie zugeordnet; das Leck muss aufs Board.

### L2 — Always-Label-on-Create + A1-Backfill
Bead-Erzeugung setzt `plan:<slug>` wo ableitbar (der Dispatch-`lane=plan`-Pfad
tut das schon; auf den Reactor + manuelle `bd create` ausweiten). Plus ein
einmaliger Backfill der ~120 Alt-Orphans (Match über spec-id/Description-Pfad →
`plan:<slug>`). Damit schrumpft die Unlinked-Lane auf echten Rest.

### L3 — Catch-all-Initiative pro Firma + Capture-Helper
Eine stehende Karte pro Firma („Ad-hoc / Sonstiges"), an die unscoped/inline
Arbeit als Event andockt. Ein `mk capture "<text>" [--firma X]`-Helper (CLI +
MCP-Tool) hängt eine Inline-Aktion mit einem Befehl als Event an die passende
oder die Catch-all-Initiative — damit auch ein schneller Fix wie `08b1119` eine
Spur hinterlässt. Unlinked-Lane-Items sind per Klick einer echten Initiative
**oder** der Catch-all zuweisbar. **Alternative verworfen:** statt Catch-all eine
reine Auto-Matching-Heuristik (Inline-Arbeit per Schlüsselwort einer Initiative
zuordnen) — verworfen, weil Inline-Arbeit oft *keine* ableitbare Initiative hat
(genau das macht sie unscoped); eine Heuristik würde dann fehl-zuordnen oder
still droppen. Die Catch-all garantiert ein Zuhause + Mensch-Re-Zuordnung aus der
Unlinked-Lane — Sichtbarkeit vor Cleverness.

### L4 — Capture-Completeness-Metrik
Eine sichtbare Kennzahl: Anteil der Work-Items (Beads/Workspaces) mit
Initiative-Zuhause. Baseline jetzt messen, Trend sichtbar. Macht „nichts fällt
durch" überprüfbar statt gefühlt.

## Success-Criteria

- SC1: Ein Bead ohne Initiative-Zuhause erscheint binnen eines Adapter-Zyklus
  in der „Unlinked"-Lane (nicht still gedroppt) — verifiziert mit einem frisch
  erzeugten label-losen Test-Bead.
- SC2: Der A1-Backfill ordnet die existierenden Orphan-Beads zu; die
  Unlinked-Bead-Zahl sinkt messbar (Baseline ~120 stack → Rest dokumentiert,
  warum nicht zuordenbar).
- SC3: Eine Inline-Aktion lässt sich mit *einem* `mk capture`-Befehl als Event
  an eine Initiative hängen; das Event erscheint in deren Drawer-Historie.
- SC4: Eine Capture-Completeness-Metrik (% Work-Items mit Zuhause) ist auf dem
  Board sichtbar und reproduzierbar berechnet; Baseline ist festgehalten.
- SC5: Kein Work-Item-Typ ist still unsichtbar — Orphan-Beads UND unverlinkte
  vk-Workspaces tauchen beide in der Unlinked-Lane auf (je ein Test-Fall).

## Risiken / offene Fragen

- R-A: Unlinked-Lane flutet (viel Alt-Rauschen). Mitigation: nach Firma + Alter
  gruppieren, geschlossene/ephemere Beads ausklammern, nur Aktionable zeigen.
- R-B: Catch-all wird Müllhalde (alles wandert nach „Sonstiges" statt in echte
  Initiativen). Mitigation: Metrik trackt Catch-all-Anteil; periodischer Review
  der Catch-all-Events auf Re-Zuordnung.
- R-C: „Was zählt als Arbeit?" — Scope auf Beads, vk-Workspaces und
  Commits-auf-getrackten-Repos; NICHT Chat. Vor L1 festnageln.
- R-D: Commit-ohne-Bead-Erkennung (L1c) braucht eine Commit↔Bead-Konvention
  (QuantBot hat `[QB-NN]` schon) — für Stack-Repos Konvention definieren oder
  L1c auf Phase 2 schieben.

## Phasen (Granularität, keine Zeit)

1. **Leak-Detektor + Unlinked-Lane (Beads + vk-Workspaces)** (Gran. 3) —
   Done wenn SC1 + SC5 (beide Typen sichtbar, kein Silent-Drop). L1c (Commits)
   erst nach R-D-Konvention.
2. **Always-Label + A1-Backfill** (Gran. 2) — Done wenn SC2 (Orphan-Zahl sinkt,
   Rest dokumentiert).
3. **Catch-all-Initiative + `mk capture`-Helper** (Gran. 2) — Done wenn SC3
   (Inline-Aktion mit einem Befehl als Event verankert).
4. **Capture-Completeness-Metrik** (Gran. 2) — Done wenn SC4 (Kennzahl sichtbar
   + Baseline festgehalten).

---

> Architektur-Hebel über mehrere Adapter + Reconciler → Plan-Pipeline.
> Deep-Tech empfohlen (Reconciler-Semantik, „was zählt als Arbeit", Silent-Drop-
> Vermeidung). Kein Bead vor Quick-Verdict.

## Reviewer-Verdict — quick (glm-5.1) — 2026-06-18

**Verdict:** `approved`

Klarer, strukturierter PRD-Draft mit messbarem Problembeleg, expliziter Nicht-Ziele-Sektion und durchdachten Done-Kriterien je Phase. Keine Zeitschätzungen, keine Konventionsverstöße, Risiken und offene Fragen sind ehrlich adressiert.

**Findings:**
- [minor] **Architektur-Alternative L3 nicht explizit verworfen** — Bei L3 (Catch-all-Initiative) wird keine Alternative benannt und verworfen, anders als bei L1 (loggen vs. Board). Optionen wie 'automatisches Matching-Heuristik statt Catch-all-Karte' wären denkbar und ihre Verwerfung würde die Entscheidung untermauern.

**Asks:**
- [ ] Erwäge bei L3 kurz eine Alternative zu nennen (z.B. Auto-Matching-Heuristik statt Catch-all-Karte) und warum sie verworfen wurde — analog zu L1, damit alle Architekturentscheidungen dieselbe Begründungstiefe haben.

## Reviewer-Verdict — deep-tech (spec-panel critique, focus: requirements/architecture) — 2026-06-18

- **Verdict:** `approved-with-notes` (Deep-Tech-Vertiefung des Quick-`approved`)
- **Methode:** /sc:spec-panel critique inline. Panel: Wiegers/Adzic/Cockburn (requirements), Fowler/Newman/Hohpe/Nygard (architecture).
- **Gesamt:** Starkes PRD mit dem richtigen Prinzip (Sichtbarkeit vor Hoffnung). Das Panel fand eine Ironie, die vor Phase 1 zu adressieren ist: das Tool gegen stilles Lecken kann selbst still lecken.

**MUST-FIX vor Phase-1-Beads (2):**

1. **[Nygard/Hohpe — MAJOR] Der Detektor darf seine eigene Vollständigkeit nicht still vortäuschen.** Zwei Teile:
   (a) **Liveness** — wenn der Reconciler crasht/nicht läuft, zeigt die Unlinked-Lane `0` → sieht aus wie „alles erfasst". Braucht „Detektor lief zuletzt @T"-Heartbeat; veralteter Lauf = sichtbarer Alarm, nicht stille Null.
   (b) **Nenner-Ehrlichkeit** — die „Was-ist-Arbeit"-Gesamtmenge zieht der Detektor aus derselben Rig-Registry, die mariobrain/angeloos skippt; deren Orphans wären unsichtbar. Das Leck-Tool **erbt die Blindstellen des Linkers**. Skipped/unerreichbare Rigs müssen als „nicht erfasst — Quelle unerreichbar" sichtbar sein, nicht aus dem Nenner fallen. Die L4-Metrik muss ihren Nenner ausweisen, sonst liest sie falsch-hoch (95% bei einem komplett blinden Rig).

2. **[Wiegers/Cockburn — MAJOR] „Was zählt als Arbeit" gehört IN die PRD, nicht in R-C vertagt.** Die ganze Vollständigkeits-Behauptung misst gegen diesen Nenner — ist er undefiniert, ist „100% erfasst" bedeutungslos. Die drei Quellen (Beads, vk-Workspaces, Commits) **plus** die Ausschlüsse (ephemere/closed Beads? Merge-Commits? Bot-Commits?) explizit festnageln, bevor Phase 1 startet.

**NOTES:**
- **[Cockburn] Steady-State-Triage-Last** — braucht jeder Orphan Mensch-Zuordnung? Dann ist die Hand-Erfassung nur verschoben, nicht entfernt (Ziel war Reduktion). Catch-all als *permanentes* Zuhause (niedrige Last) vs. Pflicht-Re-Assign klären.
- **[Fowler] Detektor = Erweiterung des bestehenden `--link`-Reconcilers** (eine Roamer-Quelle), kein paralleler zweiter — sonst zwei Wahrheiten über „was ist roamer". Explizit machen.
- **[Newman] `mk capture`-Idempotenz** (Doppel-Lauf = Doppel-Event) + Verortung der Metrik-Berechnung (per-rig vs. global, wann).
- **[Wiegers] SC2 ohne Schwelle** — „sinkt messbar" braucht einen Floor oder die Akzeptanz „jeder verbleibende Orphan hat einen dokumentierten Grund" schärfer als Pflicht formulieren.
