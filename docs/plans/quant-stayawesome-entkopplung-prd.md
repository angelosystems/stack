---
title: Quant/Stay-Awesome-Entkopplung auf werkstatt
slug: quant-stayawesome-entkopplung
status: draft
layer: prd
parent_plan: null
scope: Logische Trennung der QuantBot- und Stay-Awesome-Infrastruktur auf werkstatt — gemeinsame Single-Points entflechten, ohne physischen Umzug.
created: 2026-06-11
review:
  quick: auto
  deep: spec-panel
  panel-mode: critique
  panel-focus: [requirements, architecture]
references:
  - /opt/stack/docs/plans/master-kanban.md
  - /opt/stack/docs/plans/portfolio-inventur.md
---

# Quant/Stay-Awesome-Entkopplung auf werkstatt

## Kontext (Inventur 2026-06-11)

Mario-Entscheid 2026-06-11: **logische Trennung auf werkstatt** (kein
physischer Umzug), Master-Kanban wird die eine Wahrheit, Solartown bleibt
gemeinsame Multi-Tenant-Fabrik (Trennung über Rigs).

Die Daten-Ebene ist bereits weitgehend getrennt (quantbot-pg :54330,
captable :54321, mario-brain :5434, solartown :5433). Verklebt sind vier
Stellen — drei davon haben sich am 2026-06-11 als reale Schadensquelle
gezeigt, als das 1TB-Volume /mnt/hc-tsdb volllief und neben der QuantBot-TSDB
auch die mario-brain-DB (und damit das Master-Kanban) mitriss.

```
heute:                                       Ziel:

quantbot-pg :54330 ──┬── QuantBot            quantbot-pg :54330 ─── QuantBot
                     └── weft_local  ✗       weft-db (eigen)    ─── Weft

/mnt/hc-tsdb (1TB) ──┬── TSDB (907G)         /mnt/hc-tsdb       ─── nur TSDB
                     └── docker-volumes ✗    root-disk          ─── alle anderen Volumes

master.stayawesome.app ── mario-brain ✗      Entscheid W4: bleibt oder neutral
```

## Arbeitspakete

Konvention: ⚒ Arbeit 1-5, ◆ Wert 1-5, ⚠ Gate, ◎ Eleganz 1-5.

### W1 — Docker-Volumes runter vom TSDB-Volume   ⚒2 ◆5 ◎4

`/var/lib/docker/volumes` ist als Ganzes auf `/dev/sdb[/docker-volumes]`
gemountet. Damit teilen sich **alle** Container-Volumes (mario-brain,
authentik, captable, inbox-zero, ollama-Modelle, solartown-remote-db) die
Platte mit der 907G-RawTick-Hypertable. Wächst die TSDB, liegen alle still.

- Ziel: Nur die TSDB bleibt auf /mnt/hc-tsdb; alle übrigen Volumes
  (zusammen ~16G) wandern auf die Root-Disk (66G frei).
- Weg: Neues Verzeichnis auf Root-Disk, Container geordnet anhalten,
  Volume-Daten rsyncen, Mount umbauen, Container starten.
- ⚠ Gate: Wartungsfenster je Container; Reihenfolge mario-brain zuerst
  (kleinster, größter Wert: Master-Kanban-Resilienz).

### W2 — weft_local raus aus quantbot-pg   ⚒3 ◆4 ◎4

Weft (Agenten-Infra) nutzt `postgres://weft@127.0.0.1:54330/weft_local`
— die QuantBot-Live-Geld-Postgres. Steht quantbot-pg, steht Weft mit;
umgekehrt erzeugt Weft Last auf der Trading-DB.

- Empfehlung: eigener kleiner Postgres-Container `weft-db` auf der
  Root-Disk (analog mario-brain-db), Dump + Restore von weft_local.
- Alternative (nur falls Container-Sparsamkeit wichtiger ist):
  weft_local in die solartown-Instanz :5433 — beides Agenten-Infra.
- ⚠ Gate: Weft-Services (api, orchestrator, node-runner) zeigen auf
  neue DSN; Restate-Replays prüfen.

### W3 — TSDB-Platzschutz dauerhaft   ⚒2 ◆5 ◎5

Root-Cause des Vorfalls: Die Compression auf RawTick lief historisch
(36/49 Chunks komprimiert), aber der Policy-Job existierte nicht mehr.
Neun Tages-Chunks à 63-119G liefen unkomprimiert auf, bis die Platte
voll war. Der Engpass blieb ab ~2026-06-02 mehrere Tage unbemerkt.

- Compression-Policy auf RawTick neu anlegen: compress_after 24h,
  initial_start 03:00 UTC, Intervall täglich (Spec aus qb-2dt2).
  **Wartet auf Mario-Freigabe** (Permission-Gate 2026-06-11).
- disk-pressure-alert.timer prüfen/erweitern: /mnt/hc-tsdb muss
  abgedeckt sein, Meldung edge-triggered bei Schwellen-Übergang.
- Folge-Frage (eigene Karte, nicht Teil dieses PRD): Warum produzierte
  der Ingest 63-119G/Tag — Firehose-Quelle prüfen, ob das gewollt ist.

### W4 — Domain-Zuordnung Master-Kanban   ⚒1 ◆2 ◎3

master.stayawesome.app serviert mario-brain (persönlich/portfolio-weit,
inkl. QuantBot-Initiativen) hinter Stay-Awesome-SSO. Inhaltlich ist das
kein Stay-Awesome-Tool.

- Empfehlung: vorerst belassen (geringer Hebel), aber im Repo
  dokumentieren, dass die Domain Portfolio-Scope hat. Neutral-Domain
  nur, wenn externe Nutzer (Mitarbeiter) Zugriff bekommen sollen.

### W5 — Master-Kanban als eine Wahrheit verdrahten   ⚒3 ◆5 ◎4

Das Portfolio-Schema (initiative.firma) trennt Quant/Stay-Awesome
bereits sauber auf Daten-Ebene. Was fehlt, ist der lebende Sync:

- initiative_link-Adapter für vibe-kanban (vk_workspace) und Beads
  reaktivieren bzw. edge-triggered nachziehen (kein Polling).
- PRDs aus den Repo-docs/plans als plan_file-Links einsammeln, damit
  der Plan-Review-Stand (status-Frontmatter) im Board sichtbar wird.
- Stage-Pflege: idea/now/soon/watching/done je firma-Spaltenfilter.

## Erfolgs-Kriterien (messbar)

- [ ] `findmnt /var/lib/docker/volumes` zeigt Root-Disk, nicht /dev/sdb.
- [ ] Auf /mnt/hc-tsdb liegt ausschließlich das TSDB-Datadir.
- [ ] `weft-api` DSN zeigt nicht mehr auf :54330; quantbot-pg hat keine
      weft-Rolle/DB mehr.
- [ ] timescaledb_information.jobs enthält eine Compression-Policy für
      RawTick; alle Chunks älter 48h sind komprimiert.
- [ ] disk-pressure-alert meldet Übergang >90% auf /mnt/hc-tsdb.
- [ ] Master-Kanban zeigt je firma (stayawesome/quantbot/…) aktuelle
      Initiativen mit lebenden bead_count/vk_count/plan_count.

## Limitations / Entscheidungen

- Kein physischer Server-Split (Mario-Entscheid 2026-06-11) — die
  Blast-Radius-Trennung endet bei gemeinsamer Maschine (RAM/CPU/Disk-IO).
  Co-Existence-Slices + earlyoom bleiben die Schutz-Ebene dafür.
- Solartown bleibt gemeinsam (eine DB :5433, Trennung über Rigs) —
  bewusst, Betriebslast vor Reinheit.
- Redis :6379 bleibt vorerst geteilt (Session/Cache, geringes Risiko);
  erst angehen, wenn ein konkreter Vorfall es rechtfertigt.

## Status-Update — 2026-06-11 (Session Fable)

- **W1 (teilweise umgesetzt):** mario-brain-db + ollama auf Root-Disk-Binds
  (/var/lib/mario-brain/{pg,ollama}, compose angepasst, verifiziert healthy).
  Rest-Cutover (/var/lib/docker/volumes weg von /dev/sdb) steht noch aus —
  Alt-Volumes auf sdb bleiben bis dahin als Rollback liegen.
- **W2 (umgesetzt):** weft-db (postgres:16-alpine, :54332, Daten
  /var/lib/weft-pg) via /opt/weft/weft-db.compose.yml; Dump+Restore
  verifiziert (6 projects, 1 execution); weft-api + weft-dashboard auf
  EnvironmentFile /root/.secrets/weft-db.env umgestellt, health ok,
  0 Verbindungen mehr auf quantbot-pg. Alte weft_local-DB auf :54330
  bleibt vorerst als Fallback (Drop nach stabiler Laufzeit).
- **W3 (umgesetzt):** Retention RawTick 21d (Job 1006), compress_after 24h
  (Job 1000) — beide Mario-approved. Chunk-Backlog (9× 63-119G) wird über
  temporäres Hetzner-Volume quantbot-cold (Tablespace cold_store)
  verschoben + dort komprimiert; Volume wird danach gelöscht.
  disk-pressure-alert-Abdeckung von /mnt/hc-tsdb noch zu prüfen.
- **W4 (entschieden):** Mario 2026-06-11 — Domain master.stayawesome.app
  bleibt; Portfolio-Scope dokumentiert hiermit.
- **W5 (teilweise umgesetzt):** planfile-adapter live (fsnotify auf 5 Repos,
  14 PRD-Initiativen synchron) + solartown-adapter --listen
  (bead_created/bead_closed-NOTIFY, edge-triggered) — beide als
  systemd-Services master-kanban-{planfile,solartown}. Offen:
  vk_workspace-Adapter (Routing-Konvention nötig) + github_pr-Adapter.

## Reviewer-Verdict — quick — 2026-06-11T06:02:56Z

- **Depth:** quick
- **Verdict:** needs-changes
- **Plan-Commit:** fb8e54d

### Findings

```json
{
  "mode": "impl",
  "plan": "quant-stayawesome-entkopplung",
  "verdict": "needs-changes",
  "R1": {
    "check": "R1",
    "status": "fail",
    "reason": "plan or delivery missing"
  },
  "R2": {
    "check": "R2",
    "status": "fail",
    "reason": "plan or delivery missing"
  },
  "R3": {
    "check": "R3",
    "status": "fail",
    "reason": "delivery missing"
  },
  "R4": {
    "check": "R4",
    "status": "fail",
    "reason": "delivery missing"
  },
  "R5": {
    "check": "R5",
    "status": "fail",
    "reason": "delivery missing"
  }
}
```

### Asks

See Findings above.
