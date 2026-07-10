# ⚠ Brücken-Notiz: Master Kanban ist herausgelöst (2026-07-10)

**Source of Truth für alles Master-Kanban ist ab jetzt
`angelosystems/master-kanban`** (Arbeits-Checkout `/opt/master-kanban`,
Dach: `code-factory/master-kanban`-Submodule). Betroffen: `master-kanban/`,
`adapters/`, `go.mod`/`go.sum` (Adapter-Modul), `eingangs-gate-renames.sql`,
`../mk-health/`, `../../schema/` (portfolio-DDL), `../../cockpit/`,
MK-Doku unter `docs/`.

**Warum liegen die Pfade hier trotzdem noch:** Der Outbox-Deploy-Reaktor baut
SHA-gepinnt aus GENAU EINER Repo-Wurzel (`deploy-reactor-manifest.yaml:
repo: /opt/stack`), und der Merger emittiert bei stack-Merges
`service=master-kanban` mit stack-SHA. Die stack-Kopie ist damit
**Deploy-Mirror** — würde sie entfernt, liefe jeder Deploy auf
„src existiert im Commit nicht".

**Regeln bis zur Umverdrahtung** (Folge-Schritt, koordiniert mit dem
Tester/Merger-WP2 des PRDs `code-factory-dach-aufraeumen` im docs-Repo):

1. Änderungen an MK-Code NUR im master-kanban-Repo.
2. Rücksync hierher nur als Ganzes (kein Cherry-Editing im Mirror).
3. Nach Umverdrahtung (Merger-Emission + Reactor-repo auf /opt/master-kanban)
   werden die Carve-out-Pfade hier in EINEM Commit entfernt (PRD WP1 Schritt 5).
