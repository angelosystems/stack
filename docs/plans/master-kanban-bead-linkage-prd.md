---
title: Master-Kanban Bead-Linkage — PRD
status: approved
slug: master-kanban-bead-linkage
layer: prd
created: 2026-06-14
---

# Master-Kanban Bead-Linkage — PRD

Dieses Dokument beschreibt die automatische Verknüpfung von Beads über mehrere Rigs hinweg in der zentralen Portfolio-Datenbank (`mario_brain`).

## 1. Übersicht

Der Bead-Linkage-Mechanismus ordnet Beads aus den Dolt-Datenbanken der Rigs (z. B. `solartown_clean`, `quantbot_clean`, `stack_clean`, `stayawesome_clean`) über den PRD-Slug oder strukturierte Labels den entsprechenden Initiativen in `mario_brain` zu.

## 2. Akzeptanzkriterien

- **AK-1**: Automatische Zuordnung von Beads basierend auf dem `plan:<slug>`-Label oder `spec_id`.
- **AK-2**: Vollständige Konsistenz der Verknüpfungen (DELETE alter Zuordnungen und INSERT neuer Zuordnungen).
- **AK-3**: Keine Duplikate oder Waisen-Links (Orphans) ohne entsprechende Initiative.

## 3. DB-Schreib-OK (Schreibfreigabe von Mario)

Um die korrekte Funktionsweise der Verknüpfung vor der vollständigen Automatisierung zu demonstrieren (Proof-of-Value), wurde folgendes DB-Schreib-OK von Mario erteilt und dokumentiert:

- **Ziel-Datenbank**: `postgres://mario:c8f2b7025f25a3fa9149c4fb4e20cc18@127.0.0.1:5434/mario_brain`
- **Tabelle**: `portfolio.initiative_link`
- **Ziel-Initiative**: `qb-backtest-gate`
- **Freigegebene Operation**: Manuelle DELETE- und INSERT-Statements zur Verknüpfung der 18 Backtest-Gate-Beads (Stand: 2026-06-14).
- **Service-Credential**: R-D (Read-Write Dedicated Service Credential für den manuellen Seed-Lauf).
- **Freigegeben von**: Mario (per expliziter Schreibfreigabe)
- **Status**: Erfolgreich ausgeführt am 2026-06-15.
