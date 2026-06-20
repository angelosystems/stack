---
title: Master-Kanban — Automatische Multi-Rig Bead-Linkage
slug: master-kanban-bead-linkage
status: approved-with-notes
layer: prd
parent_plan: /opt/stack/docs/plans/master-kanban.md
scope: Beads automatisch an Initiative-Karten verlinken (über alle Rigs hinweg), damit das Board echten Umsetzungs-Fortschritt zeigt statt bead_count=0 — die Grundvoraussetzung dafür, aus dem Board heraus zu arbeiten.
created: 2026-06-14
review:
  quick: auto
  deep: spec-panel
  panel-mode: critique
  panel-focus: [requirements, architecture]
references:
  - /opt/stack/docs/plans/master-kanban.md
  - /opt/stack/docs/plans/master-kanban-dispatch-prd.md
  - /opt/stack/tools/portfolio/adapters/solartown/main.go
---

# Master-Kanban — Automatische Multi-Rig Bead-Linkage

## Problem

Das Board zeigt für QuantBot (und fast alle Firmen) `bead_count=0`, obwohl
Arbeit aktiv läuft — Beispiel Backtest-Gate: ~18 Beads, 7 closed, aktive
Umsetzung, Karte steht trotzdem in IDEA mit `🐝0`. Zwei verkettete Ursachen,
verifiziert am Code + DB (2026-06-14):

1. **`bead_count` = `COUNT(initiative_link WHERE kind='bead')`** (View
   `portfolio.initiative_summary`). Systemweit hat **genau eine** Initiative
   überhaupt Bead-Links (`st-quantbot-paperclip-rollout`, 1 Stück). Es gibt
   **keinen Mechanismus, der `initiative_link kind='bead'` befüllt** — der
   `solartown`-Adapter *liest* nur vorhandene Links, erzeugt aber keine.
2. **Der Adapter ist single-rig.** Er ruft `bd show <ref>` fix in `BD_RIG`
   (`/opt/solartown`) auf und listent nur auf solartowns Bead-DB (:5433).
   QuantBot-Beads (`qu-*`) liegen in der project-lokalen `.beads`-DB unter
   `/opt/quantbot` — der Adapter kann sie weder lesen noch ihren Status pushen.

Folge: Das Board ist ein Plan-File-Spiegel, kein Fortschritts-Spiegel. „Aus dem
Board heraus arbeiten" scheitert daran, dass das Board den wahren Stand nicht
kennt. (Verwandt: die Dispatch-PRD setzt voraus, dass der Adapter-Rückfluss
funktioniert — siehe [[master-kanban-dispatch]].)

## Ziel

Beads erscheinen automatisch an der richtigen Karte, über alle Rigs hinweg,
mit korrektem Status — ohne dass jemand Links von Hand pflegt.

## Nicht-Ziele

- Keine neue Bead-Semantik, kein Eingriff in `bd`/Beads selbst.
- Kein Schreibzugriff des Boards auf die Bead-DBs (read-only Richtung Beads).
- Keine Karten-Auto-Erzeugung aus Beads — Beads linken an *existierende*
  Initiativen; verwaiste Beads bleiben unverlinkt (sichtbar als Lücke).

## Join-Key (die Kern-Entscheidung)

Bead ↔ Initiative über den **PRD-Pfad als gemeinsamen Schlüssel**:

- Die Initiative trägt einen `initiative_link kind='plan_file'` mit der
  PRD-Pfad-`ref` (z.B. `/opt/quantbot/docs/plans/backtest-gate-prd.md`).
- Der Bead trägt entweder `spec_id` = PRD-Pfad **oder** ein Label = PRD-Slug
  (z.B. `backtest-gate`). Aus dem PRD-Pfad lässt sich der Slug deterministisch
  ableiten (`<slug>-prd.md`).
- Linker matcht Bead→Initiative, wenn Slug(bead.spec_id|label) ==
  Slug(initiative.plan_file.ref). Ergebnis: `initiative_link kind='bead'`.

→ **Annahme A1 (2026-06-14 teilweise verifiziert):** Der Join-Key existiert auf
Beads bereits als Label `plan:<slug>` — `qu-djrn` trägt `lane:plan, plan:backtest-gate`
(auto-derived aus `docs/plans/backtest-gate-prd.md`). Der Linker kann also primär
auf das `plan:<slug>`-Label matchen; `spec_id`/Description-Pfad sind Fallback.
Vorsicht: die **neuere „backtest-gate:"-Bead-Welle** (z.B. `qu-i6np`) nennt den PRD
teils nur in der Description-Prosa — Label-Konsistenz über beide Wellen vor dem
Linker-Lauf prüfen.

## Lösung

### L1 — Rig-Registry (Prefix → Rig-Dir + Bead-DSN)

Konfigurierbare Map statt Single-`BD_RIG`:

```
qu- → /opt/quantbot   · beads-DSN <quantbot>
st- → /opt/solartown  · beads-DSN :5433
cl- → /opt/solartown  · (gleiche DB, anderer Prefix)
…
```

Der Adapter leitet aus dem Bead-Prefix Rig-Dir + DSN ab. `bd show` läuft im
korrekten Rig-Dir; LISTEN läuft pro Bead-DB (eine Verbindung je DSN).

### L2 — Auto-Linker (neuer Adapter-Modus `--link`)

Scan über alle Rigs: für jeden Bead mit auflösbarem Join-Key, der zu einer
Initiative passt, `initiative_link kind='bead'` upserten (idempotent). Edge:
beim `bead_created`-NOTIFY mitlinken, nicht nur Status pushen.

### L3 — Multi-Rig Status-Push (bestehender Pfad, rig-aware)

Der vorhandene `runOnce`-Status-Push bleibt, aber `readBead` nutzt die
Rig-Registry statt `BD_RIG`. `bead_count`/Stage-Vorschläge funktionieren dann
für alle Rigs.

### L4 (Phase 0, manuell-überbrückt) — Backtest-Gate sofort seeden

Als sofortiger Proof-of-Value die 18 Backtest-Gate-Beads an `qb-backtest-gate`
linken (DELETE+INSERT der `kind='bead'`-Links). **Braucht explizites OK für
einen Schreibzugriff auf die geteilte `mario_brain`-DB** — Auto-Mode hat das
(korrekt) geblockt. Identisch zu dem, was L2 später automatisch erzeugt.

## Success-Criteria

- SC1: Nach `--link`-Lauf zeigt `qb-backtest-gate` `bead_count` = Anzahl der
  aktuell zur PRD gehörenden Beads (Stand 2026-06-14: 18 — Zahl ist Ist-Wert,
  kein Hard-Target; SC robust gegen Schwankung), verifiziert per
  `/api/initiatives`, jedenfalls > 0.
- SC2: `bd show` für `qu-*`-Beads gelingt aus dem Adapter (korrektes Rig-Dir);
  Status-Events (`activity`/`completed`) fließen für QuantBot-Beads.
- SC3: Linker ist idempotent — zweiter Lauf erzeugt keine Duplikat-Links.
- SC4: Verwaiste Beads (kein Join-Key / keine passende Initiative) werden
  geloggt, nicht still verschluckt (kein Silent-Drop).
- SC5: Multi-DB-LISTEN übersteht Reconnect mit Dawn-Sync je DB (kein Timer).

## Risiken / offene Fragen

- R-A: Join-Key fehlt auf Alt-Beads (A1). Fallback: Label-Nachrüstung per
  einmaligem Backfill, oder Description-Pfad-Parsing als Notlösung (fragil).
- R-B: Prefix-Kollision/Mehrdeutigkeit, wenn zwei Rigs denselben Prefix
  nutzen — Registry muss eindeutig sein.
- R-C: Mehrere Initiativen teilen denselben PRD-Slug (unwahrscheinlich, aber
  Linker braucht Konflikt-Verhalten: erste gewinnt + Log).
- R-D: Schreibzugriff auf `mario_brain` — Phase 0 + L2 mutieren shared State;
  Lauf nur als dediziertes Service-Credential, nicht mit Klartext-DSN.

## Phasen (Granularität, keine Zeit)

1. **Rig-Registry + rig-aware `readBead`** (Granularität 2) — Done wenn SC2
   **und** die Registry beim Laden Prefix-Eindeutigkeit erzwingt (Doppel-Prefix
   = Startup-Fehler, nicht stiller Last-Wins).
2. **Auto-Linker `--link` + Join-Key** (Granularität 3) — Done wenn SC1+SC3+SC4.
3. **Multi-DB-LISTEN** (Granularität 2) — Done wenn SC5.
4. **Phase 0 Seed** (Granularität 1, braucht DB-OK) — Done wenn die zugehörigen
   bead-Links in `mario_brain` bestätigt sind (Count == Bead-Zahl der PRD)
   **und** das DB-Schreib-OK von Mario im Plan-File dokumentiert ist.

---

> Multi-File / Architektur-Hebel am Adapter → Plan-Pipeline (kein Hack). Kein
> Bead vor Quick-Verdict.

## Reviewer-Verdict — quick (glm-5.1) — 2026-06-14

**Verdict:** `approved-with-notes`

Solider PRD-Draft mit klar belegtem Problem (Code+DB verifiziert), plausibler Nicht-Ziele-Sektion und überprüfbaren Done-Kriterien je Phase. Keine Zeitschätzungen, keine Konventionsverstöße. Architekturentscheidungen sind nachvollziehbar begründet und Alternativen (Fallback-Strategien) werden erkennbar verworfen.

**Findings:**
- [minor] **Phase 4 Done-Kriterium referenziert nur SC1 statt eigenes Kriterium** — Phase 4 (Phase 0 Seed) hat als Done-Kriterium 'Done wenn SC1 sofort' — das ist tautologisch mit Phase 2 und beschreibt keine eigenständige überprüfbare Bedingung für den manuellen Seed. Besser: 'Done wenn 18 bead-Links in mario_brain bestätigt und DB-OK dokumentiert.'
- [minor] **Rig-Registry-Eindeutigkeit nur als Risiko, nicht als Done-Kriterium** — R-B nennt Prefix-Kollision als Risiko, aber Phase 1 hat kein explizites Done-Kriterium, dass die Registry eindeutige Prefixes erzwingt (validiert oder dokumentiert).

**Asks:**
- [ ] Gib Phase 4 ein eigenständiges Done-Kriterium das nicht nur SC1 wiederholt sondern auch das DB-OK und die Link-Zahl explizit macht
- [ ] Füge Phase 1 ein Done-Kriterium hinzu das die Eindeutigkeit der Prefix-Map verifiziert (z.B. 'Registry lädt ohne Prefix-Duplikat-Fehler')
- [ ] Präzisiere bei SC1 ob die 18 ein Hard-Target oder ein Beispiel ist — falls Bead-Zahl zwischenzeitlich schwankt sollte das Kriterium robust sein
