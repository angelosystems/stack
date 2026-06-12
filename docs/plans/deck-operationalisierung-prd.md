---
title: Deck-Operationalisierung — Triage, Pull-System & Kapazitätsmanagement
slug: deck-operationalisierung
status: review
layer: prd
parent_plan: null
scope: Das Master-Kanban (Deck) wird vom Lese-Fenster zum Steuerpult — Triage-Knöpfe, automatische Stage-Übergänge, WIP-Limits mit Pull-Regel, Kapazitätsanzeige je Lane, Lane-Badges (Hacker/Solartown) und ein Vorschlags-Agent, der den Backlog nach umsetzbar × wichtig bewertet.
created: 2026-06-12
review:
  quick: auto
  deep: none
references:
  - /opt/docs/konventionen/plan-konvention.md
  - /opt/docs/konventionen/public-endpoints.md
  - docs/plans/master-kanban.md
---

# PRD: Deck-Operationalisierung

## Why

Das Deck (master.stayawesome.app) projiziert heute zuverlässig — aber es
steuert nichts. Triage passiert per CLI (`bd label`), Stages werden manuell
gepflegt, der Backlog (976 lane-lose Beads) ist vom Board aus unsichtbar und
unbedienbar, und niemand weiß, ob gerade Kapazität frei ist. Mario muss
Richtung entscheiden können (Priorisierung, Vorschläge abnicken), während die
Maschine das Timing übernimmt (was wann startet, abhängig von echter
Kapazität). Wirkungsweite: Deck-Frontend (cockpit.html), master-kanban serve
(API), ein neuer Triage-Agent; Reactor/Merger werden nur konsumiert (Events
existieren), nicht verändert.

## Goal

Mario arbeitet ausschließlich im Deck: Idee anlegen, triagieren (Karte /
Hacker / Archiv), soon-Spalte ordnen, Vorschläge abnicken. Alles andere —
Stage-Sprünge, Nachziehen bei freier Kapazität, Kapazitätsanzeige — läuft
event-getrieben ohne Handgriff.

## Anforderungen

### R1 — Triage-Knöpfe (Backlog-Tab)
Jeder Backlog-Eintrag bekommt drei Aktionen:
- **→ Karte**: erzeugt neue Initiative (stage=idea, firma aus Repo-Kontext)
  oder hängt den Bead an eine bestehende Karte; Bead erhält `lane:plan`.
- **→ Hacker**: Bead erhält `lane:hacker`; Reactor dispatcht via vk. Keine
  Karte — Kleinkram läuft unterm Board durch, bleibt aber im Backlog-Tab
  sichtbar bis closed.
- **✗ Archiv**: Bead wird geschlossen (`close_reason=triage-archiv`).
Zusätzlich: **[+ Neue Idee]**-Knopf im Board-Header (Titel + Firma + optional
Beschreibung → lane-loser Bead oder direkt Karte).

### R2 — Auto-Stage-Verdrahtung
`stage_proposed`-Events aus dem town.events-Stream werden angewandt statt nur
gespeichert:
- erster Bead einer Karte hooked/in_progress → Karte `now`
- alle Beads einer Karte closed (Merge bestätigt) → Karte `watching`
- PRD-Frontmatter `status: approved` → Karte mindestens `soon`
Menschliche Overrides gewinnen immer (Feld `stage_locked_by_human`).

### R3 — WIP-Limits + Pull-Regel
- Konfigurierbare Limits je Firma-Lane: `soon` max 5, `now` max 3 (Default).
- Überlauf färbt die Spalte sichtbar (kein Hard-Block für Menschen).
- **Pull**: Wird eine Karte fertig (Event: alle Beads closed → watching) und
  `now` liegt unter dem Limit, wird die oberste `soon`-Karte automatisch nach
  `now` gezogen und ihre offenen Beads an `gt scheduler` übergeben.
  Edge-getrieben über town.events — kein Timer.

### R4 — Kapazitätsanzeige je Lane
Kopfzeile jeder Firma-Lane zeigt live: `🟢 N Polecats frei · M vk-Slots`
(Quelle: pick_idle-Logik des Reactors als API + vk-Pool-Limit 5/Rig minus
offene hooked-vk-Beads). Read-only, kein eigener Zustand.

### R5 — Lane-Badges
Jede Karte und jeder Backlog-Eintrag zeigt die Ausführungs-Lane:
⚡ Hacker (vk) / 🏭 Solartown (Polecats) / ○ untriagiert. Quelle ist das
lane:*-Label des Beads bzw. die Mehrheit der Karten-Beads.

### R6 — Vorschlags-Agent (Backlog → idea)
Fällt `soon` einer Firma unter 3 Karten, bewertet ein GLM-Agent (Z.ai, wie
prd-reviewer) die lane-losen Beads dieser Firma auf zwei Achsen:
- **umsetzbar**: Repo eindeutig? Akzeptanzkriterien erkennbar? Lane-tauglich?
- **wichtig**: zahlt auf aktive Initiative (now/soon/watching) oder
  dokumentiertes Firmenziel ein?
Top-3 erscheinen als Vorschlags-Karten in `idea` mit Begründung und zwei
Knöpfen (Annehmen / Verwerfen). Niemals stilles Auto-Push. Trigger ist das
Stage-Event, das soon leert — kein Cron.

### R7 — Backlog-Detox (einmalig)
Bulk-Triage-Ansicht für den Altbestand (~976 Beads): Mehrfachauswahl +
Archiv-Knopf, Filter nach Alter/Firma/Präfix. Detox ist Voraussetzung, bevor
R6 scharf geschaltet wird.

## Arbeitspakete (Phasen, Gas/Brake/Go, Done-Kriterien)

**Phase 1 — Bedienbarkeit** (kein neuer Agent, reine UI+API)
| Paket | Inhalt | ⚒ | Einstufung | Done-Kriterium (überprüfbar) |
|---|---|---|---|---|
| P1.1 | R1 Triage-Knöpfe + [+ Neue Idee] | 2 | **Go** | Klick auf „→ Hacker" setzt `lane:hacker` im Ledger (psql-Check); „✗ Archiv" schließt mit `close_reason=triage-archiv` |
| P1.2 | R5 Lane-Badges | 1 | **Go** | Jede Karten-/Backlog-Zeile rendert ⚡/🏭/○ gemäß lane-Label (DOM-Check gegen 3 bekannte Beads) |
| P1.3 | R7 Detox-Bulk-Ansicht | 2 | **Go** | 50 markierte Alt-Beads in einem Klick archiviert; Backlog-Zähler sinkt entsprechend |

**Phase 2 — Automatik** (schreibt Stages, braucht Phase 1)
| Paket | Inhalt | ⚒ | Einstufung | Done-Kriterium |
|---|---|---|---|---|
| P2.1 | R2 Auto-Stage | 3 | **Brake** (schreibt portfolio.stage) | testrig-Smoke: Bead hooked → Karte steht binnen Event-Latenz auf now; alle closed → watching; Hand-Override bleibt bestehen |
| P2.2 | R4 Kapazitätsanzeige | 2 | **Go** (read-only) | Lane-Kopf zeigt Zahl == `gt scheduler status`-Polecats + vk-Slots; Hook eines Polecats senkt die Zahl sichtbar |
| P2.3 | R3 WIP + Pull | 3 | **Brake** (löst Dispatch aus) | Smoke: now-Karte fertig → oberste soon-Karte wandert nach now, ihre Beads erscheinen in `gt scheduler list`; bei 0 freier Kapazität wandert nichts |

**Phase 3 — Intelligenz** (LLM, braucht P1.3 als Gate)
| Paket | Inhalt | ⚒ | Einstufung | Done-Kriterium |
|---|---|---|---|---|
| P3.1 | R6 Vorschlags-Agent | 3 | **Brake** (LLM erzeugt Karten, nur als Vorschlag) | Nach Leeren von soon ≤3 Vorschlags-Karten mit Begründung; Verwerfen löscht spurlos, Annehmen erzeugt echte Karte; ohne Detox-Abschluss bleibt der Agent aus |

Kein Paket ist **Gas** im Sinne von „ungeprüft durchwinken": P2/P3 schreiben in
geteilten Zustand und bekommen je einen testrig-Smoke vor Scharfschaltung.

## Architektur-Entscheidungen (Alternativen + Verwerfung)

1. **Pull-Trigger event-getrieben statt Timer.** Alternative: Cron-Sweep
   alle N Minuten. Verworfen: Zero-Timer-Fallbacks-Konvention; der
   town.events-Stream liefert die Fertig-Kante bereits (POLECAT_RELEASED /
   bead closed), ein Timer würde nur Latenz und Doppel-Trigger-Risiko
   addieren. Catch-up beim Service-Start deckt verpasste Events ab.
2. **Kapazitätsquelle = Reactor-pick_idle als API statt eigener Zähler.**
   Alternative: Deck zählt selbst Sessions/Prozesse. Verworfen: zweite
   Wahrheit über Polecat-Zustand driftet zwangsläufig (genau das Stale-
   Identity-Problem von heute); der Reactor besitzt die Auswahl-Logik
   bereits, das Deck liest nur.
3. **GLM für den Vorschlags-Agenten statt Fable/Gemini.** Alternative
   Fable: zu teuer für einen Bewertungs-Loop über viele Beads; Alternative
   Gemini: ist die Schreib-Lane, Bewertung und Ausführung sollen getrennt
   bleiben (gleiche Trennung wie Drafter/Polecat/Reviewer der Plan-Pipeline).
   GLM macht bereits R1-R5-Reviews deterministisch gepromptet — gleiche
   Infrastruktur, gleicher Z.ai-Key.
4. **Vorschlag statt Auto-Push.** Alternative: Agent schiebt Beads direkt
   nach idea/soon. Verworfen: bei ~976 Alt-Beads wäre das eine Müll-Lawine;
   Mensch bleibt Richtungs-Gate (Annehmen/Verwerfen), Maschine bleibt
   Timing-Gate. Nach bewährtem Betrieb lockerbar (Konfig-Flag).

## Non-Goals
- Kein neues Backend-Datenmodell: alles über bestehende beads-Labels,
  portfolio.initiative.stage und town.events.
- Keine Änderung an Reactor-Dispatch oder Merger — sie liefern nur Events.
- Kein Mobile-App, keine Benachrichtigungen (separates Vorhaben).

## Akzeptanz
1. Ein Backlog-Bead lässt sich per Klick zu Karte / Hacker / Archiv
   triagieren; das Label stimmt im Ledger.
2. Karte springt ohne Handgriff idea→…→watching, wenn ihre Beads den Weg
   hooked→closed gehen (Smoke über die testrig-Kette).
3. Bei Fertigstellung einer now-Karte rückt die oberste soon-Karte nach und
   ihre Beads erscheinen im `gt scheduler list`.
4. Lane-Kopf zeigt Kapazität; Zahl ändert sich, wenn ein Polecat gehookt wird.
5. Vorschlags-Agent erzeugt nach Leeren von soon max. 3 begründete
   Vorschlags-Karten; Annehmen macht eine echte Karte daraus.
6. Jede Karte/Backlog-Zeile trägt ein Lane-Badge.

## Risiken
| Risiko | Gegenmaßnahme |
|---|---|
| Vorschlags-Agent schlägt Müll vor (Altbestand) | R7 Detox als Gate vor R6; Agent sieht nur Beads jünger als Detox-Datum oder explizit behaltene |
| Auto-Stage kämpft mit Hand-Moves | stage_locked_by_human gewinnt; Events setzen nur vorwärts, nie zurück |
| Pull zieht Karte ohne arbeitsfähiges Rig | Pull prüft Kapazität des Ziel-Rigs (R4-Quelle) vor dem Zug; ohne freie Slots bleibt die Karte in soon |
| API-Schreibpfade öffnen das Deck für Schreibzugriff | bestehende X-Api-Key-Pflicht + SSO davor (public-endpoints.md) |

## Reviewer-Verdict — quick (glm-5.1) — 2026-06-12

**Verdict:** `needs-changes`

Problem ist klar belegt und als Steuerungsengpass formuliert, Scope-Abgrenzung vorhanden. Es fehlen aber überprüfbare Done-Kriterien pro Anforderung, und die Phasen-/Arbeitspaket-Struktur mit Gas-Brake-Go-Einstufung fehlt vollständig.

**Findings:**
- [major] **Keine überprüfbaren Done-Kriterien pro Arbeitspaket** — Die Anforderungen R1–R7 sind als Feature-Beschreibungen formuliert, aber es gibt keine Phaseneinteilung und kein Gas/Brake/Go je Paket. Die Akzeptanz-Sektion enthält Integrationstest-Szenarien, aber kein Paket hat ein explizites, überprüfbares Done-Kriterium.
- [minor] **Architekturentscheidungen ohne Alternativen-Diskussion** — Zentrale Entscheidungen (z.B. Pull-Logik event-getrieben statt Timer, GLM-Agent für Vorschläge, pick_idle-Logik des Reactors als Kapazitäts-API) werden ohne Begründung alternativer Optionen und deren Verwerfung dargestellt.

**Asks:**
- [ ] Zerlege R1–R7 in Arbeitspakete mit Gas/Brake/Go-Einstufung und gib jedem Paket ein konkretes, überprüfbares Done-Kriterium.
- [ ] Ergänze bei den wichtigsten Architekturentscheidungen (Pull-Trigger, Kapazitäts-API-Quelle, Agent-Wahl) eine kurze Begründung, welche Alternative verworfen wurde und warum.

## Reviewer-Verdict — quick (glm-5.1) — 2026-06-12

**Verdict:** `approved-with-notes`

Der PRD-Draft ist strukturell hervorragend und löst ein klar belegtes Problem. Die Kritik des vorherigen Reviewers bezüglich fehlender Done-Kriterien und Architekturentscheidungen wurde vollständig adressiert. Es existieren keinerlei Konventions-Verstöße (insbesondere keine verbotenen Zeitschätzungen).

**Findings:**
- [minor] **Altlasten im Dokument** — Die Sektion 'Reviewer-Verdict — quick (glm-5.1)' am Ende des Dokuments ist ein altes Artefakt, das die Kritik 'es fehlen überprüfbare Done-Kriterien' enthält. Diese Kritik wurde im aktuellen Entwurf bereits durch die detaillierte Tabelle mit Done-Kriterien behoben. Der Text ist daher verwirrend und sollte bereinigt werden.

**Asks:**
- [ ] Entferne oder aktualisiere den alten 'Reviewer-Verdict'-Block am Ende des Dokuments, da er auf einen überholten Stand des PRDs verweist und die eigentlichen Lösungen (Done-Kriterien, Architektur-Alternativen) bereits im Text integriert wurden.
