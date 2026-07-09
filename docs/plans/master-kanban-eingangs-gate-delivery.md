---
title: Delivery — Master-Kanban Eingangs-Gate
slug: master-kanban-eingangs-gate-delivery
status: in-progress
layer: session
parent_plan: master-kanban-eingangs-gate-prd.md
scope: Delivery-Report zum Eingangs-Gate — was live ist, Gate-Outputs, was auf Mario-Freigabe wartet.
created: 2026-07-09
review:
  quick: auto
  deep: none
---

# Delivery — Master-Kanban Eingangs-Gate (2026-07-09)

Backup vor allen Schreibzugriffen:
`/opt/backups/portfolio-pre-eingangs-gate-2026-07-09.dump` (pg_dump -Fc, 16 MB).

## Step-Validation-Gates (ADR-0009)

| Schritt | Gate-Output |
|---|---|
| 1 (W1+W2) | go vet + go build planfile/solartown: 0 Fehler · Dawn-Sync-Smoke gegen Prod-DB gelaufen (scoped, dann voll) · Deploys via kanonische deploy.sh: Ledger #18 (planfile 38329a6), #19 (solartown 38329a6) · Services active |
| 2 (W3) | ADR-0011 geschrieben, Word-Hygiene-Hook ohne Beanstandung · Verweis-Kette plan-konvention.md → ADR-0011 → PRD steht · CLAUDE.md-Kurzverweis ergänzt |
| 3 (W4+W5) | go vet + go build vk-delegate & master-kanban: 0 Fehler · go test (Digest/Flow): ok · master-kanban deployed (serve restarted) · Link-Smoke: Slug → Initiative aufgelöst, Link angelegt+entfernt · vk-delegate --dry-run: Gate ok · flow-manager --dry-run: Digest-Zustellung ausgelöst |

## Success-Criteria (Checkliste)

- [x] **SC 1** `tier IS NULL` bei aktiven Karten = **0** (Selbstheilung im
  Dawn-Sync hat alle 26 Alt-Karten gefüllt; Neuanlagen kommen mit tier).
- [~] **SC 2** `/opt/code-factory`-PRDs erscheinen als Karten — im manuellen
  Voll-Sync bewiesen (u. a. `sk-vk-hacker-lane-v1`); der **laufende Watcher**
  sieht das Repo erst nach der PLANFILE_REPOS-Zeile in der systemd-Unit
  (→ Freigabe F2).
- [x] **SC 3** Cross-Repo-parent erzeugt keine Dup-Initiative mehr
  (repoForPath-Fix + Dedup-by-Path + Pfad-Kanonisierung; Regressions-Sync
  über alle 6 Repos: 0 Slug-Dups).
- [x] **SC 4** Root-Karte ohne parent_plan-Key trägt `triage:parent-check`
  (1 Karte markiert; bewusstes `null` räumt ab).
- [~] **SC 5** vk-delegate-Link: Code deployed, CLI-Smoke grün
  (Slug-Auflösung + Link-Insert). Erster echter Spawn liefert den
  Count-Beweis.
- [x] **SC 6** `unlinked_item`: 0 Transienten (Filter deployed, Sweep
  16:23 — 14 echte Reste bleiben).
- [x] **SC 7** ADR-0011 existiert; plan-konvention.md verweist darauf.
- [ ] **SC 8** Biz-Karten-Archiv (D3) — im Freigabe-SQL enthalten (F1).

## Über Plan hinaus (im Sync entdeckt + gefixt)

- **7 unsichtbare, teils approvte PRDs** eingesammelt (YAML-Frontmatter
  kaputt: ungequotete Doppelpunkte in `scope:`): capacity-governor,
  delivery-reviewer-hardening, host-resource-governance,
  pipeline-production-hardening, event-sniper-revival, sa-hr-strecke,
  secrets-topologie. Dutzende bisher unsichtbare approvte PRDs wurden als
  Karten erfasst (z. B. sa-beleg-orchestrator, qb-negrisk-overround-arb).
- **Doppel-Präfix-Wache**: `slug: sa-…` erzeugte `sa-sa-…`-IDs und
  verfehlte bestehende Karten — Slug wird jetzt gestrippt, IDs kollidieren
  natürlich (bewiesen: Roadmap-File hängt jetzt an bestehender Karte
  `sa-stack-roadmap` statt an einem Dup).
- **Legacy-Adoption**: Dedup adoptiert bestehende plan_item-Identitäten
  (auch krumme Alt-IDs) statt neue zu münzen.

## Wartet auf Mario-Freigabe (Auto-Mode-Classifier blockt, kein Bypass)

- **F1 — Karten-Chirurgie-SQL**: Renames (`sa-sa-*`→`sa-*`,
  `st-angelo-vk-bridge`→`st-angelo-vk-dispatch`), D4-RETENANT
  (`mb-master-kanban-build`→`sk-master-kanban-build`, firma stack), D3-Archiv
  der 2 Biz-Karten. Bereit unter
  `/opt/stack/tools/portfolio/eingangs-gate-renames.sql`; Ausführung:
  `psql <PORTFOLIO_DSN> -f eingangs-gate-renames.sql` (Backup liegt vor,
  jede Karte eigene Transaktion).
- **F2 — systemd-Unit-Zeile** (`/etc/systemd/system/master-kanban-planfile.service`):
  `PLANFILE_REPOS` um `/opt/code-factory=stack,` ergänzen + reload +
  Restart — dann synct der Watcher das Factory-Repo dauerhaft.

## Limitations

- Queue-Pfad von vk-delegate (Spawn später via Drainer) verlinkt noch
  nicht (ponytail: Link nur im Direkt-Spawn; Upgrade-Pfad = Drainer ruft
  linkKanban nach Replay).
- `docs/plans/` ist in stayawesomeOS gitignoriert — der
  sa-hr-strecke-YAML-Fix wirkt auf Disk, ist dort aber nicht versioniert.
- mk-verwalter bleibt ohne Live-Draht (bewusst, PRD W5-Ponytail):
  Zuordnungs-Findings laufen über den Flow-Manager-Digest an mariobrain/.
