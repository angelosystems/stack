---
title: Master-Kanban — Dispatch aus der Karte
slug: master-kanban-dispatch
status: approved-with-notes
layer: prd
parent_plan: /opt/stack/docs/plans/master-kanban.md
scope: Aus einer Karten-Detailansicht heraus Arbeit in die richtige Lane auslösen, statt Arbeit manuell aus Sessions zu starten — das Board wird Schaltzentrale statt Statusspiegel.
created: 2026-06-14
review:
  quick: auto
  deep: spec-panel
  panel-mode: critique
  panel-focus: [requirements, architecture]
references:
  - /opt/stack/docs/plans/master-kanban.md
  - /opt/stack/docs/plans/portfolio-inventur.md
  - /root/.claude/CLAUDE.md  # Größenordnungs-Routing-Tabelle
---

# Master-Kanban — Dispatch aus der Karte

## Problem

Das Master-Kanban ist heute eine **Einbahnstraße**. Drei edge-getriggerte
Adapter schieben Status *ins* Board (`planfile`, `solartown`-Bead-Status,
`vibekanban`-Task-Status). Der Rückweg fehlt: Arbeit wird weiterhin **manuell
aus einer Session** gestartet. Mario liest auf dem Board, was ansteht, wechselt
dann aber in eine separate Session/CLI, um es tatsächlich loszutreten. Der
Medienbruch ist der Reibungspunkt.

## Ziel

Aus der **Karten-Detailansicht** (Side-Peek-Drawer) heraus Arbeit auslösen.
Ein Dispatch-Knopf routet — nach der bestehenden Größenordnungs-Tabelle aus
`~/.claude/CLAUDE.md` — in genau eine Lane. Status fließt über die schon
existierenden Adapter automatisch zurück auf die Karte (vk_count / bead_count /
pr_count aktualisieren sich), womit sich die Schleife ohne Zusatzarbeit
schließt.

```
Board ─► Karte öffnen ─► [Dispatch] ─► Lane-Routing ─► Adapter ─► Karte aktualisiert
                                          │
   1-3 Files ─────────────────────────────┼─► vk-delegate --auto (Hacker-Lane, executor-classify)
   Multi-File / Architektur ──────────────┼─► Plan-Pipeline: PRD-Scaffold ─► Quick-Reviewer ─► Beads
   Live-Geld / Cross-Rig ─────────────────┴─► Plan-Pipeline + Deep-Reviewer-Marker
```

## Nicht-Ziele

- Keine neue Board-View, kein Redesign des bestehenden Drawers.
- Kein eigener Scheduler/Queue — Dispatch ist synchron-feuern-und-vergessen;
  der Status-Rückfluss kommt aus den bestehenden Adaptern.
- Keine Modell-Auswahl-UI — `executor-classify` entscheidet das Modell
  (bestehende Hybrid-Logik), nicht der Mensch.
- Keine automatische Bead-Generierung in dieser PRD — die bleibt an den
  Quick-Verdict gekoppelt (Plan-Konvention).

## Annahmen (vor Implementation zu verifizieren)

- A1: `vk-delegate --auto` akzeptiert Repo + Titel + Kontext-Text und spawnt
  einen Workspace mit Auto-Modellwahl (Memory: `reference_vk_delegate`,
  `reference_executor_classify`). → Live prüfen, nicht aus Memory annehmen.
  **Wenn die Live-Verifikation scheitert:** Blocker **nur für Phase 1**
  (Hacker-Lane-Pfad), nicht für die ganze PRD — Phase 2 (Plan-Pipeline-Pfad)
  hängt nicht an `vk-delegate` und kann unabhängig landen. Phase 1 wartet dann
  auf einen Fix/Ersatz für den Spawn-Pfad.
- A2: Die Karte kennt ihr Ziel-Repo. `primary_backend` + `initiative_link`
  (`kind=plan_file` → Pfad) lassen das Repo ableiten; für reine vk/bead-Karten
  über die firma→repo-Map (`rig-town-map`).
- A3: Der `serve`-Prozess (`:7780`, Go) darf `vk-delegate` als Subprozess
  starten (gleicher Host werkstatt, gleicher User).

## Lösung

### L1 — Lane-Vorschlag (Heuristik, Mensch bestätigt)

Beim Öffnen des Dispatch-Dialogs schlägt die Karte eine Lane vor:

- `plan_count == 0` **und** Karte trägt kein `lane:plan`-Signal → **Hacker-Lane**.
- `plan_count > 0` **oder** firma ∈ {quantbot (Trading-Path), cross-rig} →
  **Plan-Pipeline** (+ Deep-Marker bei quantbot/cross-rig).

Vorschlag ist vorbelegt, Mario kann übersteuern (Dropdown: hack | plan |
plan+deep-tech | plan+deep-business). **Kein** Auto-Feuern ohne Klick.

### L2 — Dispatch-Endpoint

`POST /api/dispatch` im bestehenden Go-`serve` (`main.go`):

```
{ "id": "<initiative-id>", "lane": "hack|plan|plan-deep", "note": "<freitext>" }
```

- `lane=hack`: leitet Repo + Titel + `note` an `vk-delegate --auto` weiter,
  schreibt ein `initiative_event` (`kind=dispatched`, `lane=hack`,
  `ref=<vk-workspace-id>`). Der `vibekanban`-Adapter zieht den Workspace-Status
  danach automatisch nach.
- `lane=plan` / `plan-deep`: legt ein PRD-Scaffold unter
  `<repo>/docs/plans/<slug>-prd.md` an (Frontmatter aus Karten-Metadaten
  vorbefüllt, `status: draft`, `review.deep` je nach Marker), verlinkt es als
  `initiative_link(kind=plan_file)`, schreibt `initiative_event(kind=dispatched,
  lane=plan)`. Triggert **nicht** automatisch Beads (Plan-Konvention: erst nach
  Quick-Verdict).

### L3 — Frontend (Drawer-Erweiterung)

Knopf „⚡ Dispatch" in der Drawer-Kopfleiste neben Archivieren. Öffnet ein
kleines Inline-Panel: vorgeschlagene Lane (vorbelegt) + Freitext-Notiz +
„Auslösen". Nach Erfolg: Toast + Event erscheint in der Karten-Historie. Bei
`lane=hack` Deep-Link zum vk-Workspace.

## Success-Criteria (messbar)

- SC1: Klick auf „Dispatch" mit `lane=hack` erzeugt einen realen vk-Workspace;
  `vk_count` der Karte erhöht sich nach Adapter-Lauf um 1 (verifiziert per
  `/api/initiative?id=…`).
- SC2: Klick mit `lane=plan` erzeugt ein PRD-File auf Disk mit gültigem
  Frontmatter (alle Pflichtfelder) und einen `initiative_link(kind=plan_file)`.
- SC3: Jeder Dispatch schreibt genau ein `initiative_event(kind=dispatched)` mit
  Lane + Ref; sichtbar in der Drawer-Historie.
- SC4: Kein Dispatch ohne expliziten Klick; Lane-Vorschlag ist nur vorbelegt,
  nie auto-gefeuert.
- SC5: Bei `vk-delegate`-Fehler bleibt die Karte unverändert, Fehler erscheint
  als Event/Toast (kein Silent-Fail).

## Risiken / offene Fragen (für Reviewer)

- R-A: Subprozess-Spawn aus dem `serve`-Daemon — Blocking vs. detached?
  **Entscheidung: detached.** Blocking wird verworfen, weil `vk-delegate` einen
  Worktree anlegt + Workspace-Spawn anstößt (mehrere Sekunden bis zig Sekunden);
  ein blockierender HTTP-Handler riskiert nginx-/Browser-Timeout und macht den
  Dispatch-Klick gefühlt „hängend". Detached: Subprozess starten, sofort die
  vergebene Workspace-ID zurückgeben, finaler Status kommt über den
  `vibekanban`-Adapter zurück auf die Karte (edge-getriggert, kein Polling).
- R-B: Repo-Ableitung für Karten ohne plan_file-Link (reine Ideen-Karten) —
  woher kommt das Ziel-Repo? Fallback firma→repo-Map ausreichend?
- R-C: PRD-Slug-Kollision bei wiederholtem Dispatch derselben Karte —
  idempotent halten (existierendes PRD wiederverwenden statt neu anlegen).
- R-D: Auth — `/api/dispatch` ist mutierend und löst echte Compute aus; muss
  hinter SSO + ggf. API-Key, nicht nur GET-Niveau.

## Phasen (Granularität, keine Zeit)

1. **Endpoint + Heuristik** (Granularität 3) — `/api/dispatch`, Lane-Vorschlag,
   Event-Schreibung; Hacker-Lane-Pfad zuerst.
   → **Done wenn SC1 + SC3 + SC5 erfüllt** (vk-Workspace entsteht, Event
   geschrieben, Fehler ist kein Silent-Fail).
2. **Plan-Pipeline-Pfad** (Granularität 3) — PRD-Scaffold + Link + Quick-Trigger.
   → **Done wenn SC2 erfüllt** (PRD-File mit gültigem Frontmatter + Link).
3. **Drawer-UI** (Granularität 2) — Knopf, Panel, Toast, Deep-Link.
   → **Done wenn SC4 erfüllt** (Dispatch nur auf expliziten Klick, Lane nur
   vorbelegt) und beide Lane-Pfade aus der UI auslösbar sind.

---

> Kein Bead bevor `status: approved` / `approved-with-notes`. Nächster Schritt:
> Quick-Reviewer (R1-R5). Deep-Tech (spec-panel critique) empfohlen, weil
> Subprozess-Spawn aus dem Daemon + Auth ein Architektur-Hebel ist.

## Reviewer-Verdict — quick (glm-5.1) — 2026-06-14

**Verdict:** `approved-with-notes`

Solider PRD-Draft mit klarer Problemstellung, plausibler Nicht-Ziele-Sektion und durchdachter Risiko-Darstellung. Das Problem ist als Medienbruch sauber beschrieben und nicht als Lösungs-Pitch getarnt. Es fehlen jedoch explizite Done-Kriterien auf Phase-Ebene und der Architektur-Entscheidung zur Subprozess-Spawn-Strategie fehlt eine erkennbare Alternativen-Diskussion.

**Findings:**
- [minor] **Phasen ohne überprüfbare Done-Kriterien** — Die drei Phasen (Endpoint+Heuristik, Plan-Pipeline-Pfad, Drawer-UI) haben nur Granularitäts-Angaben, aber keine Phase hat ein eigenes Done-Kriterium. Success-Criteria SC1-SC5 existieren, sind aber nicht den Phasen zugeordnet.
- [minor] **Architektur-Alternative zum Blocking-Problem nicht erkennbar verworfen** — R-A nennt Blocking vs. detached als Frage, aber es wird keine Alternative explizit verworfen oder begründet. Der Vorschlag (detached) steht da, aber die Abwägung fehlt.

**Asks:**
- [ ]  Weise jeder der drei Phasen explizit ein Done-Kriterium zu (z.B. Phase 1 done wenn SC1+SC3 erfüllt, Phase 2 done wenn SC2 erfüllt, Phase 3 done wenn SC4+SC5 erfüllt).
- [ ]  Ergänze bei R-A eine kurze Begründung, warum detached dem Blocking-Ansatz überlegen ist (z.B. HTTP-Timeout-Risiko bei langen vk-delegate-Läufen), sodass die Entscheidung nachvollziehbar wird.
- [ ]  Kläre in A1 explizit, was passiert wenn die Live-Verifikation von vk-delegate --auto scheitert — ist das ein Blocker für die ganze PRD oder nur für Phase 1?
