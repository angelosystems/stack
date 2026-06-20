---
title: vk-Sage — Workspace-Steward (Hänger heilen statt verwaisen)
slug: vk-sage-workspace-steward
status: approved-with-notes
layer: prd
parent_plan: /opt/stack/docs/plans/master-kanban.md
scope: Ein Sage-Analog für vk-Workspaces — diagnostiziert ruhende/pausierte Workspaces und heilt sie (re-dispatch, close-as-done, nudge-merge, eskalieren), statt sie ungenutzt liegen zu lassen. Jeder Workspace wurde mit einem Grund (Bead) angelegt; ein ruhender Workspace ist ein unerledigtes Ziel.
created: 2026-06-19
review:
  quick: auto
  deep: spec-panel
  panel-mode: critique
  panel-focus: [architecture, requirements]
references:
  - /opt/stack/docs/plans/master-kanban.md
  - /opt/stack/docs/plans/capture-completeness-prd.md
  - /opt/stack/docs/plans/master-kanban-dispatch-prd.md
---

# vk-Sage — Workspace-Steward

## Problem

vk archiviert oder heilt ruhende Workspaces nicht. Belegt 2026-06-19:

- **Rituale-Workspace**: Worktree ist *kein gültiges git-Repo*, 0 PR / 0 Merge,
  Execution `killed`, hängt seit ~7 Tagen `archived=0`.
- **3 pausierte Deck-Beads** (`st-yozd`/`st-1bpf`/`st-ib5e`): `vk-paused:no-commits-exit1`,
  **4-5× über Tage re-dispatcht und jedes Mal wieder gescheitert** — nie
  erfolgreich, nie aufgeräumt.
- Unter den „aktiven" Workspaces: 31 completed + **14 failed + 2 killed**
  Executions, real laufend nur 2.

Folge: Ruhende Workspaces hängen als „aktiv", verzerren die Agenten-Auslastung,
binden Retry-Compute. **Jeder Workspace wurde mit einem Grund (Bead)
angelegt** — ein ruhender Workspace ist ein *unerledigtes Ziel*, nicht nur Müll.
Aber: **naives „bei Fehler neu starten" loopt ewig** (die 3 pausierten Beads beweisen es).

## Ziel

Ein **Sage-Analog für vk** (wie `reference_sage_advisor` in Solartown):
detektiert tote/hängende Workspaces, **diagnostiziert die Fehlerklasse**
(GLM-5.1) und **entscheidet** — heilen, als-erledigt-schließen, Merge-anstoßen
oder eskalieren — mit Retry-Budget, edge-getriggert, jede Aktion sichtbar.

## Nicht-Ziele

- **Kein blinder Retry-Bot.** Diagnose VOR der Entscheidung ist der ganze Wert.
- **Kein neuer Standalone-Service.** Angelo-Capability / Erweiterung des
  bestehenden **Witness**-Musters (überwacht Polecat-Health schon).
- **Kein Ersatz für Refinery (Merge) oder das Dispatch-Gate** — ergänzt sie.
- **Keine Heilung von Workspaces ohne Bead** (manuelle/Test-Workspaces): die
  werden nur archiviert, nicht re-dispatcht (kein „Grund" zum Wiederbeleben).

## Lösung

### L1 — Detektion (edge + sweep)
Subscribe auf das Execution-End-Event (`completed/failed/killed` — der
`vibekanban`-adapter sieht es ohnehin) + ein periodischer Sweep nur für
`running`-aber-stuck (kein Update seit > T). Kein Dauer-Poll.

### L2 — Diagnose (das Sage-Hirn)
Für einen ruhenden Workspace mit Bead: GLM-5.1 liest **Bead-Ziel + Outcome**
(exit-code, `no-commits`-Flag, Worktree-Zustand/git-Validität, letzter
Agent-Output) → Fehlerklasse. Wie Sage's Hook-Block→GLM-5.1-Rat, nur auf
Workspace-Lifecycle.

### L3 — Entscheidung/Aktion je Klasse
```
no-commits-exit1 + Ziel schon erledigt   →  Bead „already done" schließen + archivieren   [stoppt den Loop]
no-commits-exit1 + Arbeit echt offen      →  re-dispatch mit diagnose-geschärftem/re-scopetem Prompt
broken worktree / Setup-Fail (rituale)    →  archivieren + frisch re-dispatchen (oder schließen falls obsolet)
echter Blocker (Dep / Entscheidung)       →  eskalieren (advisor-mail + Board-Event), NICHT retry
PR da, aber unmerged                       →  Merge anstoßen (Refinery-Nudge)
```

### L4 — Retry-Budget + Eskalation
Pro Bead ein Heil-Zähler. Nach **N** erfolglosen Heilungen (Default 2): **STOP +
eskalieren** (Board-Event + advisor-Signal), keine weitere Auto-Heilung. Der
Zähler liegt am Bead (überlebt Restarts). **Alternative verworfen:** unbegrenzt
retryen — verworfen, weil genau das die 3 pausierte Deck-Beads erzeugt hat.

### L5 — Sichtbarkeit + Sage-Liveness
Jede Sage-Aktion (heal/close/escalate/archive/merge-nudge) = ein Board-Event
(`kind=sage_action`) auf der Initiative des Beads. Eskalationen erscheinen in
einer „🧓 Sage-Eskalation"-Sicht. **Und** (Lehre aus Capture-Completeness): der
Sage hat selbst einen Liveness-Heartbeat — fällt er aus, ist das sichtbar, nicht
still.

## Success-Criteria

- SC1: Ein `no-commits-exit1`-Workspace, dessen Ziel schon existiert, wird vom
  Sage binnen eines Zyklus „already done" geschlossen + archiviert — verifiziert
  an einem der aktuellen pausierte Deck-Beads.
- SC2: Ein echt-unfertiger gescheiterter Workspace wird mit einem Prompt
  re-dispatcht, der die **Diagnose enthält** — der neue Versuch unterscheidet
  sich nachweisbar vom identischen Retry.
- SC3: Nach N (Default 2) erfolglosen Heilungen am selben Bead **stoppt** der
  Sage und eskaliert (Board-Event + Signal); kein weiterer Auto-Retry —
  verifiziert durch 3 simulierte Fehlschläge in Folge.
- SC4: Jede Sage-Aktion erscheint als Board-Event auf der Bead-Initiative
  (keine stille Aktion).
- SC5: Ein broken-worktree-Workspace (kein `.git`, wie rituale) wird archiviert
  + frisch re-dispatcht (oder geschlossen falls obsolet), nicht hängen gelassen.
- SC6: Die Zahl dangling Workspaces (archived=0, Execution terminal, kein offener
  PR, Alter > T) sinkt über Zeit gegen ~0 (das Leck schließt).

## Risiken / offene Fragen

- R-A: **Fehldiagnose** — GLM-5.1 schließt „erledigt", obwohl nicht (false-close)
  oder umgekehrt. Mitigation: die „already-done"-Entscheidung braucht einen
  **verifizierbaren Check** (Ziel-Artefakt existiert / Tests grün), nicht nur
  LLM-Meinung; bei niedriger Confidence → eskalieren statt schließen.
- R-B: **Re-dispatch-Storm** — der Sage dispatcht selbst Workspaces; mit
  Sessions+Reactor zusammen = die Überlauf-Sorge. Mitigation: Sage-Dispatches
  laufen durch dasselbe Admission-/WIP-Gate (sobald es existiert); bis dahin
  harte Concurrency-Cap + das Retry-Budget.
- R-C: **Eskalations-Flut** — jeder ruhende Workspace eskaliert → Lärm. Mitigation:
  einmal pro Bead eskalieren, dedupen, gruppieren.
- R-D: **Workspaces ohne Bead** (tr-*/Test) — kein „Grund" zum Heilen; nur
  archivieren, nie re-dispatchen.
- R-E: **Sage scheitert selbst still** (Watcher-of-the-Watcher) — eigener
  Liveness-Heartbeat Pflicht (L5).

## Phasen (Granularität, keine Zeit)

1. **Detektion + Diagnose + Report (read-only)** (Gran. 3) — ruhende Workspaces
   klassifizieren + als Board-Events sichtbar machen, **keine Mutation**.
   Done wenn SC4 (Aktionen geloggt) für den Klassifizier-Schritt + Dry-Run-Report
   über die aktuellen 4 pausierten Workspaces.
2. **Aktion: close-as-done + archivieren** (Gran. 2) — die sichere Aktion, stoppt
   die Retry-Loops zuerst. Done wenn SC1 + SC5.
3. **Aktion: diagnose-informiertes re-dispatch + Retry-Budget + Stop-&-Eskalieren**
   (Gran. 3) — inkl. der SC3-Eskalation (Stop nach N). Done wenn SC2 + SC3.
4. **Sage-Liveness + dangling-Metrik** (Gran. 2) — Done wenn (a) der
   Sage-Liveness-Heartbeat sichtbar ist und ein veralteter Lauf einen Alarm
   auslöst (R-E), und (b) die Zahl dangling Workspaces (SC6-Definition) als
   konkrete Kennzahl auf dem Board steht (nicht nur „messbar", sondern als
   angezeigter Wert mit Baseline).

---

> Architektur-Hebel (Agent-Daemon + Diagnose-LLM + Dispatch/Refinery-Kopplung)
> → Plan-Pipeline. Deep-Tech verpflichtend (Heil-Entscheidungen mutieren echten
> Compute + können loopen). Kein Bead vor Quick-Verdict.

## Reviewer-Verdict — quick (glm-5.1) — 2026-06-20

**Verdict:** `approved-with-notes`

Ein klar strukturierten Plan mit starkem Problembeleg, bewusst plausibler Scope-Abgrenzung, überprüfbaren Success-Criteria pro Phase und ehrlich ausgewiesenen Risiken. Es existieren keine Konventionsverstöße (keine Zeitschätzungen). Architekturentscheidungen sind begründet, eine Alternative explizit verworfen.

**Findings:**
- [minor] **Done-Kriterien der Phase 4 unscharf formuliert** — Die Done-Kriterien für Phase 4 referenzieren auf SC3-Eskalation (welches laut Phasen-Logik eigentlich in Phase 3 verortet ist) und fordern, dass der SC6-Trend 'messbar' ist. 'Messbar' ist kein eindeutiger Abschluss-Trigger, hier fehlt ein konkretes Check-Kriterium.

**Asks:**
- [ ] Schärfen Sie das Done-Kriterium für Phase 4: Definieren Sie genau, was 'SC6-Trend messbar' bedeutet (z.B. durch ein Metrik-Dashboard oder einen reportierbaren Status-Wechsel) und stellen Sie sicher, dass die SC3-Eskalation klar zugeordnet ist.

## Reviewer-Verdict — deep-tech (spec-panel critique, focus: architecture/requirements) — 2026-06-20

- **Verdict:** `approved-with-notes`
- **Methode:** /sc:spec-panel critique inline. Panel: Fowler/Newman/Hohpe/Nygard (architecture), Wiegers/Adzic/Cockburn + Crispin (requirements/testing).
- **Gesamt:** Starkes PRD (verbaut viele Lehren dieser Session). Drei Must-Fixes betreffen die Autonomie-Sicherheit eines Agenten, der echten State mutiert.

**MUST-FIX vor Phase-1-Beads (3):**

1. **[Nygard — KRITISCH] Der Sage muss die *alleinige* Re-Dispatch-Autorität bei Fehlern sein.** Heute re-dispatchen **zwei** Dinge gescheiterte Beads: der bestehende Auto-Retry (der die 3 pausierten Beads 4-5× geloopt hat) **und** der Reactor. Einen smarten Healer danebenzustellen, *ohne den dummen Retry zu entfernen/unterzuordnen*, macht das Retry-Budget wirkungslos — die pausierten Beads bleiben. Das PRD muss spezifizieren: alten Auto-Retry abschalten, **alle** Failure-Re-Dispatches durch den Sage routen.
2. **[Fowler — MAJOR] Der „close-as-done"-Sicherheitsmechanismus ist hand-waved.** Es ist die *gefährlichste* Aktion (markiert Arbeit still als erledigt). R-A nennt „verifizierbarer Check", aber **wie** der Sage das Done-Artefakt eines beliebigen Beads mit Prosa-Akzeptanz kennt, ist unspezifiziert. Entweder Beads tragen einen **maschinen-prüfbaren Done-Probe**, oder close-as-done braucht **Mensch-Bestätigung**. Sonst droppt ein false-close Arbeit lautlos — das Gegenteil der Capture-Completeness-Maxime.
3. **[Hohpe — MAJOR] Atomarer Claim/Lock vor jeder Aktion.** Ohne per-Bead-Lease/Compare-and-Set handeln zwei Sage-Zyklen (oder Sage+Reactor) doppelt am selben ruhenden Workspace. Heal-Counter-Inkrement **und** Aktion müssen atomar sein.

**NOTES:**
- **[Crispin/Wiegers] Kalibrierungs-Gate vor Autonomie.** Phase 1 ist read-only — ergänze ein Kriterium: die Sage-Klassifikationen müssen mit dem Mensch-Urteil über die aktuellen 4 pausierten Workspaces übereinstimmen (≥ Schwelle), **bevor** Phase 2 ihn handeln lässt. Keinen autonomen Mutator mit unbewiesenem Urteil scharfschalten. **Bestanden & Dokumentiert am 20.06.2026:** Siehe [vk-Sage Kalibrierung](vk-sage-calibration.md) (100% Übereinstimmung erreicht).
- **[Cockburn] Live-Geld-Ausnahme.** quantbot/Trading-Path-Beads → Sage **nur eskalieren**, kein autonomes close/re-dispatch (Live-Geld-Konvention „keine Änderungen ohne Permission").
- **[Hohpe] Subscribable Execution-End-Event verifizieren** (wie Dispatch-PRD A1) — emittiert der vibekanban-adapter wirklich ein abonnierbares Event, oder degradiert „edge-triggered" still zu Poll?
- **[Newman] Heal-Counter-Reset-Semantik** — setzt partieller Fortschritt (ein paar Commits, dann Fehler) den Zähler zurück oder nicht? Definieren, sonst verhungern harte Tasks oder pausierten Beads bleiben.
- **[Crispin] Den Sage testen braucht eine Sandbox** (fake failed Bead/Workspace) — an die Staging-vk-Instanz koppeln, den autonomen Mutator nicht gegen prod-Beads testen.
