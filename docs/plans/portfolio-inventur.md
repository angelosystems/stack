---
title: Portfolio-Inventur (Stage 0)
status: draft
created: 2026-05-03
related:
  - docs/plans/master-kanban.md
source:
  - MEMORY.md (project_*-Einträge)
  - bd ls --status=open auf /opt/solartown
  - gh pr list --repo angelosystems/solartown (9 offen)
  - find /opt /root -name "*-backlog.md" + plans/-Verzeichnisse
  - vk session list — TODO Stage-0-Followup
---

# Portfolio-Inventur — ~35 Initiativen

Stage-0-Output für Master-Kanban-Plan. Pro Firma sortiert. Stage-Heuristik:
- `now` = aktiv, in den letzten 7d bewegt
- `soon` = priorisiert, noch nicht angepackt
- `watching` = passiv, läuft im Hintergrund / wartet auf Trigger
- `idea` = Vision, kein Commit
- `done` = abgeschlossen, als Anker im Board sichtbar

**Mario-Review nötig:** Granularität trifft Initiative-Niveau (~2-Wochen-Brocken)? Doppel-Counts (z.B. mb-master-kanban vs. ag-master-kanban) konsolidieren? Stage-Einordnung passt?

---

## 🏨 STAY AWESOME (12)

```yaml
- id: sa-markthalle-buergschaft
  firma: stayawesome
  stage: now
  primary_backend: plan_file
  title: "Markthalle Bürgschaft Sparkasse Mai 2026"
  drill:
    - kind: plan_file
      ref: /root/stayawesomeOS/docs/plans/markthalle-bank-nachforderung.md
  notes: aktiver Vorgang aus Memory project_stayawesome

- id: sa-fred-vsop-offboarding
  firma: stayawesome
  stage: now
  primary_backend: plan_file
  title: "Fred Neust Austritt + VSOP-Nachzeichnung"
  drill:
    - kind: plan_file
      ref: /root/stayawesomeOS/docs/plans/fred-neust-offboarding.md
  notes: B2B nicht AN, IDN-Wohnsitz, GESSI-Format; 3 Wording-Fixes vor Sig

- id: sa-mews-skr04-bridge
  firma: stayawesome
  stage: now
  primary_backend: plan_file
  title: "Mews-LedgerAccountCode → SKR-04 (direct, kein eigenes COA)"
  notes: 14/15 direct-mapped, USALI als View; Memory project_stayawesome_mews_skr04_bridge

- id: sa-cards-pleo
  firma: stayawesome
  stage: soon
  primary_backend: plan_file
  title: "Kreditkarten-Setup Pleo-Alternative"
  drill:
    - kind: plan_file
      ref: /root/stayawesomeOS/docs/plans/cards-backlog.md
    - kind: plan_file
      ref: /root/stayawesomeOS/docs/plans/cards-pleo-alternative.md

- id: sa-wdl-inventur
  firma: stayawesome
  stage: soon
  primary_backend: plan_file
  title: "WDL-Inventur 363 PandaDoc-docs Korrektur"
  notes: 5+ vertragliche Wellen 2022-2026; Wilmes 2023 = partiarisch

- id: sa-documenso-user-mgmt
  firma: stayawesome
  stage: watching
  primary_backend: plan_file
  title: "Documenso self-hosted — User-Anlage via Container"
  drill:
    - kind: plan_file
      ref: /root/stayawesomeOS/docs/plans/documenso-authentik-options.md
  notes: sign.stayawesome.app live, UI-Signup geht NICHT

- id: sa-office-gcp-bootstrap
  firma: stayawesome
  stage: done
  primary_backend: plan_file
  title: "GCP-Projekt + gaia-ai SA + DWD + Chat"
  notes: live seit 2026-04-30; Phase-2-Bot für Mitarbeiter offen

- id: sa-sso-stack
  firma: stayawesome
  stage: done
  primary_backend: plan_file
  title: "oauth2-proxy + Workspace SSO für *.stayawesome.app"
  notes: live seit 2026-04-30 (werkstatt)

- id: sa-inbox-zero
  firma: stayawesome
  stage: done
  primary_backend: plan_file
  title: "Inbox Zero self-hosted — inbox.stayawesome.app"
  notes: live 2026-05-01; PUBSUB+LLM+API_KEY_SALT required

- id: sa-dns-migration
  firma: stayawesome
  stage: done
  primary_backend: plan_file
  title: "DNS-Migration 8 Zonen → Cloudflare"
  drill:
    - kind: plan_file
      ref: /root/stayawesomeOS/docs/plans/dns-backlog.md
  notes: 2026-04-30 alle NS auf CF; Registrar bleibt IONOS

- id: sa-bitwarden-org
  firma: stayawesome
  stage: watching
  primary_backend: plan_file
  title: "Bitwarden Org auf vault.bitwarden.com (US)"
  notes: Gaia AI Service-Account konfiguriert

- id: sa-fin-repo-v2
  firma: stayawesome
  stage: idea
  primary_backend: plan_file
  title: "fin-Repo V2 — Schema + USALI-Mapping als Wiederverwendungsbasis"
  notes: Mario+Enver's Vorgänger-Cockpit (Next.js+Prisma+Postgres)
```

---

## ⚡ SOLARTOWN (10)

```yaml
- id: st-promote-completion
  firma: solartown
  stage: now
  primary_backend: solartown
  title: "Promote 2026-05-01 vervollständigen — Phase-1a-Fixes im richtigen Tree"
  drill:
    - kind: bead
      ref: cl-fzrdyr
    - kind: bead
      ref: cl-cm90qz
  notes: town-main + bare-main haben KEINEN common ancestor; blockiert Sicherheits-Card tr-pnl2f

- id: st-quantbot-paperclip-rollout
  firma: solartown
  stage: now
  primary_backend: solartown + plan_file
  title: "Epic qb-4m4 — QuantumShift × Paperclip Final Rollout (21 Subtasks)"
  drill:
    - kind: plan_file
      ref: /root/gt/strategiekreis/plans/quantbot-paperclip-rollout-final-implementation.md
    - kind: bead
      ref: qb-4m4
  notes: Live-Geld-Code, eine Änderung pro Deploy; Plan v2 reviewed

- id: st-end-to-end-ingest
  firma: solartown
  stage: now
  primary_backend: solartown + github + plan_file
  title: "Epic-Ingest Pipeline Stages 3-5 + WhatsApp Channel-2 + plan-reviewer-loop"
  drill:
    - kind: plan_file
      ref: /opt/solartown/docs/plans/end-to-end-ingest-vision.md
    - kind: plan_file
      ref: /root/gt/strategiekreis/plans/end-to-end-ingest-vision-implementation.md
    - kind: github_pr
      ref: angelosystems/solartown#11
  notes: live ab 2026-05-01

- id: st-staging-mode-c
  firma: solartown
  stage: now
  primary_backend: plan_file
  title: "Staging Mode C 2026-05-03"
  drill:
    - kind: plan_file
      ref: /root/gt/strategiekreis/plans/solartown-staging-mode-c-2026-05-03-implementation.md

- id: st-postgres-decom-finish
  firma: solartown
  stage: soon
  primary_backend: solartown
  title: "Postgres-Decommission abschließen"
  notes: Welle 1+2+3 gelandet, 2 systemd-Wurzeln + 1 metadata-flipper offen

- id: st-reactor-fixes
  firma: solartown
  stage: now
  primary_backend: solartown + github
  title: "Reactor-Stuck + Polecat-Watcher + Stuck-Claim-Recovery"
  drill:
    - kind: github_pr
      ref: angelosystems/solartown#12
    - kind: github_pr
      ref: angelosystems/solartown#7
    - kind: github_pr
      ref: angelosystems/solartown#5
  notes: Cluster aus 3 PRs; Reactor-Race + HUNG_POLECAT edge-trigger

- id: st-mq-auto-merger
  firma: solartown
  stage: done
  primary_backend: github
  title: "Auto-Merger + Pre-Reviewer + Reactor DB-pick"
  drill:
    - kind: github_pr
      ref: angelosystems/solartown#4
    - kind: github_pr
      ref: angelosystems/solartown#6
  notes: Refinery-Bypass 3600x speedup live

- id: st-sage-advisor
  firma: solartown
  stage: done
  primary_backend: solartown
  title: "Sage-Advisor Phase-2 — Hook-Block→GLM-5.1→advisor-mail"
  notes: live (clean-staging+prod); rig-town-map; Stdlib-Awareness validiert

- id: st-v15-haertung
  firma: solartown
  stage: done
  primary_backend: solartown
  title: "V15 Härtungs-Run — 6 Cards live (PR #4-#7)"
  notes: 17/20 DB-closed, 4/20 Files

- id: st-weft-migration
  firma: solartown
  stage: watching
  primary_backend: plan_file
  title: "weft-Migration in /opt/weft-lab/ Test-Tree"
  notes: separater Chat-Auftrag; clean/prod nicht beeinträchtigen
```

---

## 💰 QUANTBOT (7)

```yaml
- id: qb-pusd-trade-flow-mystery
  firma: quantbot
  stage: now
  primary_backend: vk + solartown
  title: "pUSD-Allowance + Trade-Flow klären (Allowances=0 trotz erfolgreicher Trades)"
  notes: Code-Edit in clob-executor.js BLOCKED bis Trade-Flow verifiziert

- id: qb-v2-only-policy
  firma: quantbot
  stage: now
  primary_backend: plan_file
  title: "V2-only Policy — kein neues USDC.e-Engagement"
  notes: ab 2026-04-30; clob-executor Collateral-Reihenfolge legacy

- id: qb-tsdb-compression-fix
  firma: quantbot
  stage: soon
  primary_backend: solartown
  title: "TSDB Columnstore-Policy Contention auf binance-book"
  notes: AccessExclusiveLock → Oracle/Backtest hangen

- id: qb-live-trading-day-1
  firma: quantbot
  stage: done
  primary_backend: plan_file
  title: "Erste Live-Trades 2026-04-29 — +$57.23 net über 78 trades"
  notes: 7 Strategies permitted via Kingdom UI

- id: qb-master-blueprint
  firma: quantbot
  stage: done
  primary_backend: plan_file
  title: "5-Mermaid Master-Karte + republish.sh idempotent"
  drill:
    - kind: plan_file
      ref: /opt/quantbot/docs/blueprint/

- id: qb-kingdom-dashboard
  firma: quantbot
  stage: watching
  primary_backend: github + plan_file
  title: "Kingdom Dashboard Next.js 16 :3333 — /risk Permit-Operations"
  drill:
    - kind: plan_file
      ref: /root/gt/strategiekreis/plans/kingdom-quantbot-zentrale-implementation.md
    - kind: plan_file
      ref: /root/gt/strategiekreis/plans/kingdom-home-restruct-implementation.md

- id: qb-paperclip-consolidation
  firma: quantbot
  stage: now
  primary_backend: solartown + plan_file
  title: "Paperclip-Consolidation"
  drill:
    - kind: plan_file
      ref: /opt/quantbot/research/warehouse/plans/paperclip-consolidation.md
  notes: verzahnt mit st-quantbot-paperclip-rollout (cross-firma?)
```

---

## 🧠 MARIO-BRAIN (4)

```yaml
- id: mb-master-kanban-build
  firma: mariobrain
  stage: now
  primary_backend: solartown + plan_file
  title: "Master-Kanban / Arbeitsoberfläche bauen"
  drill:
    - kind: plan_file
      ref: docs/vision/master-kanban.md
    - kind: plan_file
      ref: docs/plans/master-kanban.md
    - kind: plan_file
      ref: docs/plans/portfolio-inventur.md
  notes: DAS HIER — Stage 0 in Bearbeitung

- id: mb-vault-segmentation-p1
  firma: mariobrain
  stage: soon
  primary_backend: plan_file
  title: "Vault-Segmentation P1 — /root/.secrets/{stayawesome,quantbot}/ trennen"
  drill:
    - kind: plan_file
      ref: /root/mario-brain/vault/projects/vault-segmentation-audit.md
  notes: aus Vision-Tenant-Strang; lokal, reversibel

- id: mb-phase-1-live
  firma: mariobrain
  stage: done
  primary_backend: plan_file
  title: "Phase 1 — Sessions + FTS live"
  notes: live seit 2026-04-28; pgvector Phase 2 offen

- id: mb-phase-2-pgvector
  firma: mariobrain
  stage: idea
  primary_backend: plan_file
  title: "Phase 2 — Embeddings/pgvector über Sessions"
  notes: aus project_mario_brain Phase-1-implies-Phase-2
```

---

## ⚙ ANGELOOS / CROSS (5)

```yaml
- id: ag-llm-sidecar-revival
  firma: angeloos
  stage: now
  primary_backend: solartown (bead cl-aia40b)
  title: "gt-llm-sidecar reaktivieren (oder Promote-Replacement finden)"
  drill:
    - kind: bead
      ref: cl-aia40b
  notes: blockiert Plan-Reviewer-Tooling

- id: ag-cross-tenant-trennung
  firma: angeloos
  stage: soon
  primary_backend: plan_file
  title: "Cross-Tenant Trennung P1-P3 (Vault/Postgres/Memory)"
  notes: lokal, reversibel; aus Vision Tenant-Strang

- id: ag-jcode-harness
  firma: angeloos
  stage: watching
  primary_backend: plan_file
  title: "jcode harness evaluieren"
  notes: subscription-auth ✓, MCP nur stdio, Hooks ✗ → kein Polecat-Replace, geparkt 2026-04-29

- id: ag-whatsapp-bridge-pflege
  firma: angeloos
  stage: watching
  primary_backend: solartown
  title: "WhatsApp-Bridge Pflege (~20d re-auth)"
  notes: live; whatsmeow/lharries

- id: ag-cockpit-architektur
  firma: angeloos
  stage: idea
  primary_backend: plan_file
  title: "Cockpit-Architektur (übergeordnete Schaltzentrale)"
  drill:
    - kind: plan_file
      ref: /root/gt/strategiekreis/plans/0010-cockpit-architektur-implementation.md
    - kind: plan_file
      ref: /root/gt/strategiekreis/plans/0011-stayawesome-os-manifest-discovery-implementation.md
  notes: offene "wo lebt master-kanban"-Frage aus Vision
```

---

## Counts pro Firma

```
🏨 Stay Awesome   12  (3 now / 2 soon / 2 watching / 1 idea / 4 done)
⚡ Solartown      10  (5 now / 1 soon / 1 watching / 0 idea / 3 done)
💰 QuantBot        7  (3 now / 1 soon / 1 watching / 0 idea / 2 done)
🧠 mario-brain     4  (1 now / 1 soon / 0 watching / 1 idea / 1 done)
⚙ AngeloOS        5  (1 now / 1 soon / 2 watching / 1 idea / 0 done)
                ───
total             38
```

WIP-Limit-Check (Default 4 pro Firma):
- 🏨 Stay Awesome `now=3` ✓
- ⚡ Solartown `now=5` 🔴 **überschritten** — Promote/Paperclip/Ingest/Staging/Reactor sind alle gleichzeitig hot, das ist Realität
- 💰 QuantBot `now=3` ✓
- 🧠 mario-brain `now=1` ✓
- ⚙ AngeloOS `now=1` ✓

→ Diskussion: Solartown-WIP=4 zu eng? Per-Firma anpassen, z.B. Solartown auf 6.

---

## Offene Quellen für Stage-0-Followup

1. **vk session list** — REST-Call, vk-Sessions clustern und an Initiativen hängen
2. **bd-Beads als linked_beads** — many "now"-Initiativen haben aktive Beads die rein müssen
3. **plan-files in /opt/paperclip/** und **/opt/jcode-lab/** noch nicht durchgesehen
4. **stayawesomeOS aktive PRs** sind 0 — alle Arbeit läuft ohne PR? (oder local-branch-only)

---

## Was ich für Stage 1 brauche

Mario reviewt:
- ✅ / ❌ pro Initiative (zu klein? zu groß? ist es eine?)
- Stage-Korrekturen (z.B. ist `qb-pusd-trade-flow-mystery` wirklich `now` oder `watching`?)
- Cross-Firma-Karten? `qb-paperclip-consolidation` und `st-quantbot-paperclip-rollout` sind verzahnt — eine Karte oder zwei?
- WIP-Limits-Default-Anpassung pro Firma

Danach Stage 1: Single-File HTML-Mockup mit dieser Liste hardcoded, klickbar, drag-drop testen.
