---
title: Coding Factory — Master-Kanban Reconciliation & Tenant-Modell
slug: coding-factory-reconciliation
status: draft
layer: prd
parent_plan: null
scope: Master-Kanban gegen Plan-Files + Beads abgleichen, Initiativen entrümpeln/dedupen/splitten, firma→component-Achse ergänzen, Inbox entmüllen, und unter dem Dach "Coding Factory" einen sauberen Frischstart herstellen.
created: 2026-06-29
review:
  quick: auto
  deep: business-panel        # cross-rig + Strategie-Relabel
  panel-mode: discussion
references:
  - /opt/stack/docs/plans/master-kanban-bead-linkage-prd.md
  - /opt/stack/docs/plans/kanban-flow-manager-prd.md
  - /opt/docs/konventionen/plan-konvention.md
---

# Coding Factory — Master-Kanban Reconciliation

> Read-only-Befund aus 2026-06-29. **Keine DB wurde verändert.** Apply-Phase ist
> auf explizites Mario-Go gegated (prod-DB `mario_brain` @ :5434).

## 0. Datenlage (verifiziert, nicht geraten)

| Quelle | Ort | Inhalt |
|---|---|---|
| Master-Kanban | `portfolio.*` in `mario_brain` @127.0.0.1:5434 | 94 aktive Initiativen, 57 plan_items, 292 initiative_links, 41 unlinked_items |
| Beads | `beads.*` in `solartown_clean` @127.0.0.1:5433 | ~19,6k issues (closed 18.240 / open 1.062 / hooked 244 / in_progress 12) |
| QuantBot | `quantbot` @127.0.0.1:54330 | eigener Tenant-Backend |
| Plan-Files | 6 kanonische Repo-Wurzeln + ~12 Spiegel | ~170 Files, davon ~110 unverlinkt |

## 1. Diagnose — 6 systemische Lücken (= der "Mess")

1. **Stage ≠ Realität.** Board-`stage` driftete von der Evidenz ab.
   `qb-backtest-gate`=idea aber 19 Beads; `st-smoke-cascade`=done aber 7 offene Pläne.
2. **Status flippt nie auf done.** 4 von 57 plan_items sind `done`, Rest hängt auf
   `approved`. → File-Status ist als Fertig-Signal wertlos; **Beads + Delivery-Files +
   Deploy-Check** sind die Wahrheit.
3. **„now"-Geister.** ~12 Initiativen auf `now` ohne Plan UND ohne Bead → hier muss
   das **PRD vorne rein** oder demoten.
4. **Linkage-Rot.** ~110 Plan-Files unverlinkt; erwiesen gelieferte Initiativen
   (`sa-dns-migration`, `sa-sso-stack`) haben 0 Links.
5. **Duplikate & Sammelklumpen.** `catch-all`/`catchall` in jeder firma;
   `qb-paperclip-consolidation`/`-controlplane`; Sammel-Initiativen mit fremden
   Plänen (hermes↔schaltzentrale, smoke-cascade↔statuspage).
6. **Fehlende Achse.** `firma` ≈ Tenant existiert, aber `component`
   (machinery | platform | company) — deine Stack-vs-Firma-Unterscheidung — fehlt.

## 2. Zielmodell — Coding Factory

**„Coding Factory" = UI-Label/Dach über dem Kanban**, NICHT ein neuer firma-Wert.
`firma` bleibt der Tenant. Neue Achse `component` trennt Maschine von Produkt:

```
CODING FACTORY (Dach)
  ├── component=machinery →  firma solartown (st-) + stack-infra (Teil sk-)
  ├── component=platform  →  multi-tenant Produkte: 1 Codebase, 1 config/Firma
  │                          (paperclip, documenso, tenant-workspaces, kanban*)
  └── component=company   →  single-tenant: stayawesome(sa-) quantbot(qb-)
                             angeloos(ag-) personal(mb-)
```

**Lifecycle-Bucket ↔ bestehende stage** (kein Schema-Bruch, nur Disziplin):

| Bucket | stage | Definition |
|---|---|---|
| Shipped | `done` | Delivery-Report existiert UND auf Zielmaschine verifiziert |
| In-flight | `now` | Beads offen/hooked ODER VK-Session live |
| Stalled | `now`+Marker | PRD approved, Execution gestartet, hängt — Rettungskandidat |
| Idea | `idea` | PRD/Brainstorm, nie in Execution |
| Watching/Rest | `watching`/archived | Dauerbeobachtung bzw. ruhend → Icebox |

**Kanonische Plan-Wurzeln (SoT), alles andere = Spiegel-Müll (Walk ignoriert):**
`/opt/stack` · `/root/solartown` · `/opt/docs` · `/root/stayawesomeOS` ·
`/opt/quantbot` · `/opt/paperclip`(platform) · `/root/mario-brain`(personal).
Ausschluss: `*/rig/docs/plans` (Rig-Checkouts), `solartown-staging`, `weft-lab`,
`_archived-mayor-*`.

## 3. Reconciliation-Tabelle (94 Initiativen)

> **Hinweis (superseded by §8):** Die `component`-Gruppierung unten ist durch das finale Tier-Modell (§8) ersetzt — `machinery`→`code-fabrik`, `platform`→`library`, `company`→`product`. Die KEEP/VERIFY/MERGE/SPLIT-Aktionen bleiben unverändert gültig.

Legende: **KEEP** stage ok (ggf. Linkage nachziehen) · **VERIFY** als/vermutlich
erledigt → Zielmaschine prüfen, dann done · **PROMOTE** unterbewertet (reale
Bead/Plan-Aktivität) · **NEEDS-PRD** now-Geist, PRD vorne rein oder demote ·
**MERGE→x** Duplikat · **SPLIT** fremde Pläne raustrennen · **ARCHIVE** tot ·
**RETENANT/RENAME** Zuordnung/Name falsch. Evidenz: p=plans d=done o=offen b=beadlinks.

### angeloos (→ component: company:angeloos)
| id | stage | evidenz | aktion |
|---|---|---|---|
| ag-llm-sidecar-revival | done | b1 | KEEP |
| ag-cockpit-architektur | idea | — | KEEP (backlog) |
| ag-cross-tenant-trennung | now | — | NEEDS-PRD |
| ag-catch-all | watching | — | KEEP (merge-target) |
| ag-catchall | watching | — | MERGE→ag-catch-all |
| ag-jcode-harness | watching | — | KEEP |
| ag-whatsapp-bridge-pflege | watching | — | KEEP |

### mariobrain (→ component: company:personal; firma→`personal` umbenennen?)
| id | stage | evidenz | aktion |
|---|---|---|---|
| mb-phase-1-live | done | — | KEEP (link) |
| mb-hermes-mobile-access | idea | p8 d4 o4 | **SPLIT** — 7 schaltzentrale-Pläne raus in eigene Initiative `mb-schaltzentrale`; hermes bleibt, PROMOTE |
| mb-phase-2-pgvector | idea | — | KEEP |
| mb-schaltzentrale-tabs-completion | idea | p1 o1 | KEEP (unter mb-schaltzentrale) |
| mb-schaltzentrale-tagescockpit | idea | — | VERIFY→done (delivery-file existiert) |
| mb-schaltzentrale-v2-ziele-reviews-chat | idea | — | VERIFY→done (delivery-file existiert) |
| mb-master-kanban-build | now | — | NEEDS-PRD + **RETENANT→stack/platform** (das ist DIESE Arbeit) |
| mb-vault-segmentation-p1 | now | — | NEEDS-PRD |
| flow-manager-digest | watching | — | MERGE→sk-kanban-flow-manager |
| mb-catch-all | watching | — | KEEP (target) |
| mb-catchall | watching | — | MERGE→mb-catch-all |

### quantbot (→ company:quantbot)
| id | stage | evidenz | aktion |
|---|---|---|---|
| qb-live-trading-day-1 | done | — | KEEP |
| qb-master-blueprint | done | — | KEEP |
| qb-backtest-gate | idea | p1 b19 | **PROMOTE** (19 Beads) |
| qb-live-pipeline-reactivation | idea | p1 b13 | **PROMOTE** |
| qb-market-scanner | idea | p1 | KEEP |
| qb-polymarket-feed-leak | idea | p1 | VERIFY |
| qb-researcher-lane | idea | p1 | KEEP |
| qb-paperclip-consolidation | now | — | **MERGE→qb-paperclip-controlplane** + RETENANT(paperclip=platform) |
| qb-paperclip-controlplane | now | — | NEEDS-PRD (merge-target) |
| qb-pusd-trade-flow-mystery | now | — | VERIFY→ARCHIVE (pUSD lt. Memory RESOLVED) |
| qb-v2-only-policy | now | — | VERIFY→done (V2-only live ab 04-30) |
| qb-catch-all | watching | — | KEEP (target) |
| qb-catchall | watching | — | MERGE→qb-catch-all |
| qb-kingdom-dashboard | watching | — | KEEP |

### solartown (→ component: machinery)
| id | stage | evidenz | aktion |
|---|---|---|---|
| st-mq-auto-merger | done | — | KEEP |
| st-promote-completion | done | b2 | KEEP |
| st-sage-advisor | done | — | KEEP |
| st-smoke-cascade | done | p7 o7 b13 | **SPLIT** (service-status-panel, statuspage-service-skeleton, read-only-postgres = eigene); Phasen 1-3 → VERIFY |
| st-solartown-polecat-lane-rebuild | done | b11 | KEEP |
| st-solartown-production-lane | done | p2 b15 | VERIFY |
| st-v15-haertung | done | — | KEEP |
| st-angelo-vk-bridge | idea | p1 | KEEP (RENAME: `-bridge`→Capability lt. Naming-Konvention) |
| st-bead-native-reviewer | idea | p1 o1 | KEEP |
| st-sage-advisor-opus-reactor | idea | p1 | KEEP |
| st-town-resilience-hardening | idea | p1 b5 | **PROMOTE** |
| st-vk-shared-mcp-stack | idea | p1 | KEEP |
| st-dolt-decommission | now | — | NEEDS-PRD (Dolt-Removal lt. Memory laufend) |
| st-end-to-end-ingest | now | — | NEEDS-PRD |
| st-quantbot-paperclip-rollout | now | b1 | KEEP/LINK |
| st-reactor-fixes | now | — | VERIFY→done (4 Reactor-Fixes lt. Memory) |
| st-staging-mode-c | now | — | VERIFY |
| st-vk-hacker-lane-restauration | watching | p1 o1 | VERIFY→done (delivery-file existiert) |
| st-weft-migration | watching | — | KEEP |
| st-catch-all | watching | — | KEEP (target) |
| st-catchall | watching | — | MERGE→st-catch-all |

### stack (→ component: machinery ODER platform, je Zeile)
| id | stage | evidenz | aktion |
|---|---|---|---|
| sk-deck-operationalisierung | done | p2 o2 b7 | **SPLIT** (detox-bulk-ansicht raus) + VERIFY · platform |
| sk-master-kanban-bead-linkage | done | p1 b7 | KEEP · platform |
| sk-master-kanban-dispatch | done | p1 b9 | KEEP · platform |
| sk-paperclip-stack | done | p1 o1 b6 | VERIFY · platform |
| sk-tenant-workspaces | done | p1 b1 | KEEP · platform |
| sk-capture-completeness | idea | p1 | KEEP · machinery |
| sk-cicd-stack-tooling | idea | p1 b11 | **PROMOTE** · machinery |
| sk-fundraising-crm-twenty | idea | p2 (beide abandoned) | **ARCHIVE** |
| sk-kanban-flow-manager | idea | p1 | KEEP · platform |
| sk-master-kanban-mcp-copilot | idea | p1 | KEEP · platform |
| sk-secrets-topologie | idea | p1 o1 | VERIFY (Vault lt. Memory live) · machinery |
| sk-vk-sage-workspace-steward | idea | p2 (1 delivered) | VERIFY→done · machinery |
| sk-quant-stayawesome-entkopplung | now | p1 o1 | VERIFY→done (W1-W3 lt. Memory) · machinery |
| sk-resource-fleet-manager | now | p1 o1 | KEEP-now · machinery |
| sk-catch-all | watching | — | KEEP (kein dup) |
| sk-documenso-werkstatt-migration | watching | p1 o1 | VERIFY→done (Documenso live) · platform |

### stayawesome (→ component: company)
| id | stage | evidenz | aktion |
|---|---|---|---|
| sa-captable-fundraise-views | done | p2 o2 b11 | VERIFY |
| sa-dns-migration | done | — | KEEP |
| sa-inbox-zero | done | — | KEEP |
| sa-office-gcp-bootstrap | done | — | KEEP |
| sa-sa-mews-finance-reporting | done | p1 b4 | KEEP (RENAME id `sa-sa`→`sa`) |
| sa-sso-stack | done | — | KEEP |
| sa-buergschafts-fundraising | idea | p1 o1 | VERIFY/SPLIT (Microsite live, Fundraising offen) |
| sa-fin-repo-v2 | idea | — | KEEP |
| sa-gaia-core | idea | — | KEEP |
| sa-gaia-mail-concierge | idea | p2 b11 | **PROMOTE** |
| sa-lobby-terminal | idea | — | KEEP |
| sa-marketing-automation | idea | — | KEEP |
| sa-mitarbeiter-dashboard | idea | — | KEEP |
| sa-server-konsolidierung | idea | p1 o1 | VERIFY→done (Audit + M10-Verification existiert) |
| sa-finance-os-kpi-tracker | now | p1 o1 b35 | KEEP-now (Deploy wartet auf Mario) |
| sa-hr-strecke | now | p1 o1 | KEEP-now (+ Personio-Beads aus Inbox linken) |
| sa-markthalle-buergschaft | now | — | **ENTSCHEIDUNG**: Biz-Prozess, kein Code → eigenes Ops-Board? |
| sa-mews-skr04-bridge | now | — | NEEDS-PRD (SKR04-direkt lt. Memory entschieden) |
| sa-stack-roadmap | now | — | NEEDS-PRD |
| sa-bitwarden-org | watching | — | KEEP |
| sa-catch-all | watching | — | KEEP (target) |
| sa-catchall | watching | — | MERGE→sa-catch-all |
| sa-documenso-user-mgmt | watching | — | KEEP (platform) |
| sa-muse-klaerung | watching | — | KEEP (Muse=Mews PMS) |
| sa-vsop-captable-sot | watching | p1 b9 | **PROMOTE** |

## 4. Inbox (`unlinked_item`, 41) — entmüllen

~33 sind **Machinery-Transienten** (Refinery/Witness/merge-conflict/`mol-polecat-work`
/vk_workspace) → gehören NICHT ins Backlog, **Filter im Detector** statt manuell zuordnen.
Echte Reste zum Linken: Personio-Beads (→ `sa-hr-strecke`), R3-allowedActions +
PG-Reconnect-Audit (→ quantbot). 2× „Quelle unerreichbar" (angeloos) → Detektor-Bug.

## 5. Apply-Plan (jede Stufe einzeln auf Mario-Go, reversibel)

```
0  pg_dump portfolio-Schema  ──►  Backup vor jedem Schreibzugriff
1  MERGE Duplikate           ──►  6 catch-all + paperclip-Paar + flow-manager-digest
2  SPLIT Sammelklumpen       ──►  hermes/schaltzentrale · smoke-cascade · deck
3  RESTAGE aus Evidenz       ──►  VERIFY-Items auf Zielmaschine prüfen → stage/status
4  LINK Orphans              ──►  ~110 kanonische Plan-Files → plan_item (Walk mit Spiegel-Ausschluss)
5  component-Achse           ──►  ALTER TABLE initiative ADD component; backfill
6  Relabel + Inbox-Filter    ──►  UI-Dach "Coding Factory"; Machinery-Transienten raus
7  ARCHIVE Tote              ──►  fundraising-crm-twenty, pusd-mystery(nach Verify)
```

## 6. Offene Entscheidungen für Mario

- **D1** „Coding Factory" als UI-Label über `firma` (empfohlen) — oder eigener firma-Wert?
- **D2** `component`-Achse als neue Spalte auf `initiative` (empfohlen) — oder aus `firma`+id ableiten?
- **D3** Biz-Items (`sa-markthalle-buergschaft`, evtl. `sa-muse-klaerung`) — in den Coding-Kanban oder separates Ops-Board?
- **D4** firma `mariobrain`→`personal` umbenennen + `mb-master-kanban-build`→stack verschieben?
- **D5** Apply autonom (Multi-Agent-Review reicht) oder Stufe-für-Stufe mit deinem Go?

## 7. Apply-Log

### Stage 1+2 — 2026-06-29 (reversibel, applied)

Backup: `/opt/backups/portfolio-pre-reconciliation-2026-06-29.dump` (pg_dump -Fc, restore via `pg_restore`).

- **MERGE** (soft-archive, 7): `ag-catchall`, `mb-catchall`, `flow-manager-digest`, `qb-catchall`, `qb-paperclip-consolidation` (1 Link → controlplane verschoben), `st-catchall`, `sa-catchall`.
- **SPLIT**:
  - `mb-schaltzentrale` (neu, stage=now) sammelt 8 schaltzentrale-Pläne; `mb-hermes-mobile-access` behält nur hermes; 3 schaltzentrale-Sub-Initiativen archiviert.
  - `st-statuspage` (neu) ← service-status-panel + statuspage-service-skeleton; `st-read-only-postgres` (neu) ← read-only-postgres-anbindung; `st-smoke-cascade` behält 4 Smoke-Pläne.
  - `sk-detox-bulk-ansicht` (neu) ← detox-bulk-ansicht; `sk-deck-operationalisierung` behält deck.
- Ergebnis: **89 aktive** Initiativen.

### Stage 3-7 — offen (warten auf Go)
VERIFY-Restage · NEEDS-PRD · Orphan-Linkage · tier+Tags-Migration · Relabel · Archive.

## 8. Modell v2 — final (Besprechung 2026-06-29)

> Ersetzt das `component`-Framing aus §2. firma-als-Topf → **drei Tiers + zwei Tag-Achsen**.

**Drei Tiers:**
```
library      wiederverwendbar, jede Firma   Mail · Paperclip · Documenso(+UserMgmt) · Tenant-Workspaces · SSO · Inbox-Zero   firma=shared
code-fabrik  der Motor                       Master-Kanban + solartown(Rigs/Polecats/Beads) + Vibe-Kanban                      firma=shared
product      firmenspezifisch                SA: Finance-OS · Cap-Table · Bürgschaft · HR · Mews-SKR04 ; QB: Trading ; …        firma=<firma>
```
- „solartown"/„stack" sind KEINE firmas mehr — sie waren Tiers. Rename ist **logisch/Board**; physisch (`/root/solartown`, `solartown_clean`, `/etc/solartown`, Rig-Namen) bleibt Legacy bis zur eigenen Migration (eigenes PRD).

**Zwei Tag-Achsen** (Facetten, orthogonal zu `tier`):
- `firma`-Tag (n) — wer nutzt es (product=1 Firma, library=mehrere/all, code-fabrik=factory)
- `software`-Tag (n) — welches Tool/Codebase (Mail, Documenso, Finance-OS, Kanban, Trading …)
- → **per-software gefilterte Views**: jede Software = ein virtuelles Board, firmenübergreifend. Die Menge der software-Tags = Software-Katalog.
- Drill-Hierarchie: `Software → Initiative(PRD) → Beads → Workspace`.

**Datenmodell:**
- `tier` Spalte (`library|code-fabrik|product`) auf `initiative`.
- `initiative_tag(initiative_id, kind, value)`, kind ∈ {firma, software}.
- Portfolio-Tool (Go, `/opt/stack/tools/portfolio`) muss `tier`+Tags lesen/filtern → **Code-Folgearbeit** (eigene Beads).

**Best Practices (→ Verhalten der Code-Fabrik):**
1. Status aus Evidenz ableiten (Reactor: Beads+Delivery→`stage`); Hand-Override nur via `stage_locked_by_human`.
2. WIP-Limit auf `now` pro firma/tier.
3. DoD = `-delivery.md` + verifizierter Deploy.
4. `idea→now` nur mit approved Quick-Verdict (Plan-Konvention-Gate).
5. Health-`status_dot` aktivitätsbasiert (grün=match, gelb=drift, rot=`now` ohne Bead-Bewegung).
6. Inbox-Auto-Filter (Machinery-Transienten raus).
7. Reconciliation-Reactor → Drift-Mismatches in `manager_digest` (selbstheilend).
8. (optional) Karten-Templates pro tier; Pflichtfeld „blocked-Grund".

**Ausführungs-Split:**
- *Migration (additiv, SQL):* `tier`-Spalte + `initiative_tag`-Tabelle + Backfill (tier, firma=shared, software-Tags).
- *Code-Fabrik-Arbeit (Beads):* Tool liest Tags/tier, per-software-Views, die 7 Reactors/Gates.

**Stage 1+2-Kompatibilität:** Die bereits applied Merges/Splits sind modell-agnostisch (strukturelle Dedup) — `tier`/Tags sitzen rein additiv obendrauf, keine Korrektur/Rückabwicklung nötig.

**Definition of Done (§8):** (1) jede aktive Initiative hat `tier` + ≥1 firma-Tag + (wo sinnvoll) software-Tag; (2) Portfolio-Tool filtert nach tier/firma/software inkl. per-software-Board; (3) Inbox zeigt keine Machinery-Transienten; (4) Reconciliation-Reactor läuft und meldet Drift nach `manager_digest`.

## Reviewer-Verdict — quick (glm-5.1) — 2026-06-30

**Verdict:** `approved-with-notes`

Außergewöhnlich fundierter PRD mit verifizierter Datenlage, klarer Diagnose, expliziter Scope-Abgrenzung und radikal ehrlichen offenen Fragen. Alle Phasen des Apply-Plans haben überprüfbare Done-Kriterien und der Reversibilitäts-Hebel (pg_dump vor jedem Schreiben) ist sicher gestellt. Section 8 überschreibt Vorgänger-Modelle ohne Vollständigkeits-Bereinigung, was zu Inkonsistenz im Dokument führt.

**Findings:**
- [minor] **Modell-Evolution erzeugt Inline-Widersprüche** — §2 etabliert das component-Modell und mündet in der Reconciliation-Tabelle, die EXPLIZIT nach component gruppiert ist. §8 erklärt component dann für tot und ersetzt es durch drei Tiers + zwei Tag-Achsen. Da die Tabelle in §3 nie angepasst wurde, referenziert sie ein verworfenes Modell. Die physische Migration in §8 bleibt syntaktisch unklar (initiative_tag als Tabelle deklariert, dann iniciativa_id statt initiative_id, Syntax unstabil).
- [minor] **Apply-Log ist historisch, spiegelt aber nicht das finale Modell wider** — Stage 1+2 wurde auf Basis des alten §2-Modells (component) bereits exekutiert. Mit dem Pivot aus §8 zum Tier+software-Tag-Modell ist unklar, ob die durch Stage 1+2 bereits vollzogenen Merges/Splits noch zum finalen Ziel kompatibel sind oder korrigiert/rückgängig gemacht werden müssen.

**Asks:**
- [ ] Entscheide D1 und D2 aus §6 endgültig in §8: Der apply_log zeigt, dass bereits auto-applied wurde. Kontrastiere, wie die finale Lösung jetzt konkret aussieht und welche verbleibenden Schritten anfallen
- [ ] Überarbeite §2 und §3 Tabelle, sodass sie nicht das später verworfene component-Modell propagiert
- [ ] Kläre, ob die in Stage 1+2 bereits vollzogenen Merges/Splits unbeschadet unter dem §8-Modell weiter funktionieren oder ob Korrekturen nötig werden
- [ ] Definiere eine initiale Definition of Done für §8 Apply: Was genau muss im Portfolio-Tool (Code-Folgearbeit) umgesetzt sein, damit die Reconciliation als vollständig abgeschlossen gilt
