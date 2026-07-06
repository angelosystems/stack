---
title: Release-Ledger + Releases-View (Release-Pipeline Phase 1) — Delivery-Report Session B
slug: code-fabrik-release-pipeline-phase1
status: in-progress
layer: session
parent_plan: /root/solartown/docs/plans/code-fabrik-release-pipeline-prd.md
scope: >
  Delivery-Report für WP2-Rest + WP3 + WP4 des approved Release-Pipeline-PRD
  (E7 Phase 1): Ledger portfolio.deployments live, Reconciler gebaut+bewiesen,
  Releases-View + Karten-Badge committed, /version-Vertrag auf drei Adapter.
  Binary-/UI-Deploys stehen aus (Mario-Wort) — Kommandos unten.
created: 2026-07-06
review:
  quick: auto
  deep: none
references:
  - /root/solartown/docs/plans/code-fabrik-release-pipeline-prd.md
  - /opt/docs/coding-factory/20-vision.md
---

# Delivery — Release-Ledger + Releases-View (Session B, 2026-07-06)

## Was geliefert ist (Commits lokal auf /opt/stack, KEIN Push)

| Commit | Inhalt |
|---|---|
| `fa883f6` | **WP3a** `schema/portfolio-017-deployments.sql` — Ledger `portfolio.deployments` (D9-Key service+environment, D11-Unique service+env+git_sha, Outbox-Index D10 ohne Konsument), `deploy_state_map` (publizierte Mapping-Tabelle + rank), `initiative.deploy_state/live_version/live_sha`, `initiative_summary` = Live-Def + 3 Felder. **Live angewendet** (validiert: Scratch beide Ausgangszustände, BEGIN/ROLLBACK-Probe, Re-Run idempotent). |
| `f552776` | **WP3b/WP4-API** `deployments_reconcile.go` + Tests: `mk deployments reconcile` (Übergangstabelle D13 pur + 12 Fälle grün; http/cli-Proben D18; CAS-Update mit Lease-Riegel; Denormalisierung = einzige Schreibquelle der Karten-Felder), `/api/releases`. Volle Paket-Suite grün. |
| `e68b3c2` | **WP3-Verdrahtung** `ledger-record.sh` (gemeinsamer deploy.sh-Baustein: deploying-Zeile bei Deploy-Start, Upsert-Idempotenz, Gift-SHA-Quarantäne D15 mit Human-Clear-Anleitung), master-kanban/deploy.sh nutzt ihn. |
| `1e96383` | **WP4-UI** Cockpit: Releases-Tab (Head-Zeile je service+env, Status-Chips, Lease-Schloss, Initiative→Detail) + Karten-Badge `⏚ deploy_state live_version`. |
| `6c85604` | **WP2-Rest** planfile-/vibekanban-/solartown-adapter: `version [--json]` (5-Feld-Vertrag, vor flag.Parse), deploy.sh stampt version/sha/built_at + schreibt Ledger-Zeile (cli-Probe = absoluter Binary-Pfad). |
| `435d39f` | **WP3-Nachzügler** `tools/mk-health/mk-health.sh` versioniert (Drift-Sorte wie Cockpit E3) + Reconcile-Aufruf nach health.json; .gitignore-Allowlist erweitert. |

## Beweise (gegen die echte Live-DB :5434 + laufendes :7780, ohne Service-Berührung)

- **deploying→live:** Backfill-Zeile master-kanban@prod-mvp (fb22123 = real laufender
  Stand) wurde vom Reconciler via `/api/version`-Match bestätigt. ✓
- **Rot ⇒ errored (WP3-Done):** live-Zeile mit toter Probe-URL jenseits des
  Smoke-Fensters → `errored`. ✓
- **Lease-Riegel (D13):** geleaste Zeile mit toter URL blieb unangetastet. ✓
- **Doppel-Zustellung (D11):** zweiter Upsert derselben (service,env,sha) → `count=1`. ✓
- **Quarantäne (D15):** `ledger-record.sh` auf rolled_back-SHA → exit 1 + Freigabe-Anleitung. ✓
- **Steady State:** Folgelauf 0 Übergänge; Karte `sk-cicd-stack-tooling` zeigt
  `deploy_state=live · live_version=fb22123` (über `initiative_summary`, dem
  /api/initiatives-Pfad). ✓
- Beweis-Zeilen (`wp3-beweis-*`) geräumt; Ledger hält genau den wahren Ist-Stand.

## Deploy-Ergebnis (Mario-Go „Push deploy", 2026-07-06 ~15:00 UTC)

Alle Schritte gefahren; Sequenz aus diesem Report 1:1:

- **master-kanban @ bb3a8b2 live** (Ledger #7, `prev_version=fb22123` erfasst,
  Initiative `sk-cicd-stack-tooling`), `/api/version` + neuer `/api/releases`
  antworten.
- **planfile-/vibekanban-adapter @ bb3a8b2 live** (Ledger #8/#9, cli-Probe),
  Units active.
- **solartown-adapter: Binary geswappt + Vertrag ok, Unit blieb MASKIERT**
  (Notbremse 06.07. — Entmasken steht Session B nicht zu). deploy.sh brach
  per `set -e` nach dem fehlgeschlagenen `systemctl restart` korrekt ab.
  Ledger #10 sagt `live` — **bewusste Phase-1-Grenze: die cli-Probe bestätigt
  das Artefakt, nicht den laufenden Prozess**; Prozess-Wahrheit bringt
  `--selfcheck` (D18-Vollausbau) mit WP5. Beim späteren Unmask stimmt alles
  ohne Nacharbeit.
- **mk-health.timer versöhnt produktiv:** die 15:01/15:02-Läufe schrieben die
  `deploying→live`-Übergänge selbst (`--quiet`); manueller Kontroll-Lauf
  danach: 4 Head-Zeilen, 0 Übergänge (Steady State). Karte zeigt
  `live · bb3a8b2`. Stage-Beweis-Binary geräumt.

## Was AUSSTEHT

- **git push** — vom Auto-Klassifizierer geblockt (auch nach Mario-Go).
  Manuell: `git -C /opt/stack push origin main` (8 Commits ahead), oder
  Permission-Regel für `git push` setzen.

## Limitations / Befunde am Rand

- **`st-code-fabrik-release-pipeline`-Initiative verschwand** während der Session
  vom Board (Parallel-Arbeit); Ledger-Zeilen laufen auf `sk-cicd-stack-tooling`
  (CI/CD Stack-Tooling). Wenn die PRD-Karte wieder auftaucht: `UPDATE
  portfolio.deployments SET initiative_id='…'` genügt, Reconciler zieht nach.
- **Migrationskette replayed nicht** (Alt-Drift: 004/014 brechen auf frischer 001;
  014 definierte eine `lane`-View-Spalte, die live nie existierte). 017 ist
  deshalb DROP+CREATE und gegen beide Ausgangszustände validiert. Kette
  aufräumen = Kandidat fürs Checkout-Migrations-PRD (unclaimt).
- **WP5/Reaktor unangefasst** (Tabu Session B): Outbox-Status `pending` hat
  Struktur + Index, aber keinen Konsumenten; `deploy_reactor.go` (Webhook-Pfad)
  bleibt geparkt wie vorgefunden (D7). Event-Erzeugung MR_MERGED_GREEN liegt
  beim Merger (Session A / Test-Gate).
- Reconciler-Semantik bewusst: historische ältere `live`-Zeilen bleiben stehen
  (Live-Query nimmt DISTINCT-ON-Head); `errored`-Head heilt bei Match selbst
  zurück auf `live`; env-Mismatch der Probe wird geloggt, flippt aber keinen
  Status (Config-Vertrag D20b kommt mit WP5).
