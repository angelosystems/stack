---
title: Resource/Fleet-Manager — Kapazitäts- & Agenten-Tab im Master-Kanban (RAM·CPU·API·Disk)
slug: resource-fleet-manager
layer: prd
status: approved
parent_plan: null
scope: Ein neuer Tab im Master-Kanban (/opt/stack/tools/portfolio), der discovery-basiert zeigt, welche Agenten/Systeme laufen, welche Ressourcen (RAM/CPU/Swap/PSI/Disk) und welches API-Budget (pro Provider) sie verbrauchen, und WARUM der Server an einer Achse limitiert ist (Claude-Rate-Limit, volle Disk). Kategorisierung aus den cgroup-Slices (Tier-Modell aus host-resource-governance) + Prozess/Provider-Discovery — neue Systeme (Paperclip) erscheinen automatisch. Multi-Provider, vier Achsen RAM·CPU·API·Disk. Dieselben Achsen speisen den capacity-governor (Admission). Collector-Topologie = PUSH (entschieden, st-7xcy0).
created: 2026-06-19
owner: mario.gemuenden@stayawesome.de
target: master.stayawesome.app (Master-Kanban) + Collector
review:
  quick: auto
  deep: spec-panel
  panel-mode: critique
  panel-focus: [architecture, requirements]
references:
  - /root/solartown/docs/plans/host-resource-governance-prd.md (Tier-Modell = Kategorisierung; RAM·CPU·Disk)
  - /root/solartown/docs/plans/capacity-governor-prd.md (Admission — derselbe Datensatz)
  - /opt/stack/tools/portfolio/master-kanban (UI-Ziel)
  - /opt/missioncontrol/mission-control/publishers/infra-collector.go (bestehender Push-Collector)
---

<!-- WORD_HYGIENE_EXEMPT-FILE: externe Namen (cgroup, PSI, Claude, Gemini, DeepSeek, GLM, Paperclip, vk, tmux, 429, fabric, EmitKPI, df, tmpfs). -->

# Why

Es laufen ständig viele Agenten gleichzeitig auf werkstatt — vk-Workspaces
(Claude/Gemini/DeepSeek/GLM je Executor), Solartown-Polecats (gemini), quantbot,
interaktive Sessions, perspektivisch Paperclip-Flows. Mario hat **keine Sicht**,
welcher Agent welche Ressourcen frisst, und kann nicht beantworten „warum bin ich
Claude-rate-limited?" oder „warum ist die Disk voll?". Die Daten existieren
großteils (cgroup-Tiers, `angelo-infra-collector`, Governor-Sensor, 429-Events in
den Claude-Transkripten, `df`) — sie haben nur **kein Cockpit**.

# Goal

Ein Tab, der auf einen Blick zeigt: **was läuft, was kostet es, woran klemmt es** —
kapazitäts- und auslastungsseitig, server-weit, provider-übergreifend, über alle
vier Ressourcen-Achsen. „Warum limitiert" wird selbsterklärend.

# Verhältnis zu bestehenden Plänen

```
host-resource-governance  →  TIER-MODELL (cgroup-Slices = Kategorien) + Sensor + RAM·CPU·Disk-Mechanik
capacity-governor         →  AKTUATOR (Admission) — verbraucht denselben Datensatz
resource-fleet-manager    →  DIESES PRD — die SICHTBARE Oberfläche desselben Datensatzes
```
Ein Datensatz, drei Oberflächen: Kernel (garantiert hart) · Reactor/Governor
(handelt) · Cockpit (zeigt).

# Leitprinzip: Discovery-basiert, nicht hardcoded

Kategorien werden **entdeckt**, nicht eingetragen:
```
cgroup-Slice (solartown/quantbot/db/paperclip/user…)  →  System / Lane
Prozess-Kommando (claude/gemini/node-ap/opencode/…)    →  Provider/Executor
Bead-Labels (lane:plan|hacker, executor, vk-workspace) →  Lane + Bead-Zuordnung
df je FS (root / sdb / tmp)                             →  Disk-Achse pro Volume
```
Paperclip an → eigene Slice → erscheint **automatisch**. Das Tier-Modell aus
host-resource-governance IST das Rückgrat.

# Zielarchitektur — die Säulen

```
┌─ RESOURCE / FLEET ───────────────────────────────────────────────────────┐
│ KAPAZITÄT   RAM 38/61G ██████░░░  CPU 27/16  Swap 0  PSI 12               │
│             DISK root 80%(338G, 5G reserved-frei) · sdb 41%(1T) · /tmp 12% │
│             Headroom 14G · Governor: ok · Freeze-Marge grün · Disk-Marge grün│
│ API-BUDGET  Claude  ▓▓▓▓▓▓░ 6 Agenten · 3×429/h  ◀── HIER rate-limited     │
│             Gemini ▓▓░ 37 · ok     DeepSeek/GLM ░ 0                        │
├─ FLOTTE ─────────────────────────────────────────────────────────────────┤
│ System/Lane     Provider  live RAM  CPU% 429 Disk-Schreib Bead-Stau Status │
│ Solartown-Plan  gemini     37  8G   40%  —   —           6 no-idle ▲ voll  │
│ VK-Hacker       claude      6  12G  30%  3   —           —         ▲ rate  │
│ quantbot        trading     1  3G   20%  —   /tmp snapshots!       ⚠ disk  │
│ Paperclip       flows       0  —    —    —   —           —         ○ aus   │
│ Session (Mario) claude      1  2G    5%  0   —           —         ● ok    │
└──────────────────────────────────────────────────────────────────────────┘
```

1. **Kapazität (RAM·CPU·Disk):** RAM/CPU/Swap/PSI + Headroom + Governor-Verdict +
   Freeze-Marge **+ Disk je FS** (Belegung, reserved-frei, Trend). „Wieviel Luft
   auf jeder Achse?"
2. **API-Budget pro Provider:** getrennte Zeile je Provider, discovery-basiert.
   Verbrauch + 429-Rate + Ceiling-Annäherung. „Welches Limit glüht?"
3. **Flotte:** gruppiert nach System/Lane/Provider — live-Count, RAM, CPU, 429s,
   **Disk-Schreiber** (wer füllt /tmp?), Bead-Stau, **Status-Ampel**.

# Collector-Topologie: PUSH (ENTSCHIEDEN — st-7xcy0)

Die offene O1-Frage (Push vs. Pull) ist durch Code-Analyse am realen
`/opt/missioncontrol/mission-control/publishers/infra-collector.go` **entschieden**:

```
infra-collector.go:  fabric.NewPublisher(pgConnStr) → pub.EmitKPI(...)   = PUSH
   liest /proc/stat · /proc/meminfo · syscall.Statfs (Disk) im 30s-Loop
   pusht KPIs direkt in den zentralen Postgres-Store (den Master-Kanban liest)
```

**Entscheidung PUSH** (statt Pull/Prometheus-Scraper):
- **Kompatibilität:** das `fabric`-Push-SDK existiert bereits + wird genutzt. Pull
  würde HTTP-Server pro Host + Port-Freigaben + Service-Discovery erzwingen — unnötig.
- **Netz/Firewall:** Push braucht nur ausgehend Host → zentraler Postgres-Port;
  einfaches, robustes Sicherheitsmodell.

# Kanonischer Datensatz-Owner (Deep-Tech: kein Doppel-Read)

```
   echter Host (/proc/stat · /proc/meminfo · Statfs)   ← EXKLUSIV-Read
                          │
            infra-/angelo-collector  (KANONISCHER OWNER, Push via EmitKPI)
                          │
            zentraler Postgres-Store  (Single Source of Truth)
              /                    \
        capacity-governor         Cockpit (Tab)        ← KONSUMENTEN (nur lesen)
```
- **Owner:** der Collector ist exklusiver Eigentümer der Host-Metriken.
- **Kein Doppel-Read von `/proc`:** Governor (im Reactor-Sweep) **und** Cockpit
  lesen `/proc/*` **nie selbst** — beide queryen den zentralen Store. (Löst den
  „teilen vs. selbst lesen"-Widerspruch: ein Leser, ein Datensatz.)

# Collector-Design — Constraints (Deep-Tech)

Der Collector misst die Ressource, die er selbst verbraucht — darum diszipliniert:
- **Billig + geboxt:** eigener Dienst in eigener Slice mit `CPUQuota` + `MemoryMax`
  (Nygard: Observer frisst Observed).
- **Bead-DB schonen:** Agent↔Bead-Mapping **gecacht** (vk-Labels im RAM / edge),
  **nie pro Zyklus** die Bead-DB abfragen (heute ein Storm-Treiber, 110 Backends).
- **Stale-aber-ehrlich:** jeder Datenpunkt **timestamped**; Tab zeigt „Stand: vor Xs"
  + Collector-Liveness. Im Storm sichtbar veraltet > unsichtbar falsch.
- **Identitäts-Join-Key (P0-Kern):**
  ```
  PID → /proc/<pid>/cgroup → Slice        (proc, trivial)
  Session-.jsonl-UUID ↔ PID/Workspace     (OFFENE Brücke — P0; ohne sie kein Per-Agent-Token/429)
  Bead/Workspace-UUID                     (vk-Labels)
  ```

# Provider-Discovery (Multi-Provider, erweiterbar)

Kein Hardcoding. Ein Klassifizierer mappt Prozess/Executor auf einen Provider-Bucket
(`claude`→Claude, `gemini`→Gemini, `opencode`+Modell→DeepSeek/GLM, Paperclip→flows).
Unbekannte → `other`-Bucket (sichtbar, nie verschluckt). Neuer Provider = **Eintrag
in einer Daten-Tabelle**, kein Collector/Tab-Code-Change.

# Disk-Achse (vierte Dimension, aus host-resource-governance)

Disk hat **kein cgroup-OOM-Äquivalent** — volle Disk killt niemanden, sie blockiert
ALLE (DBs schreiben nicht, codium startet nicht). Das Cockpit macht das sichtbar:
- **Pro FS** (root 338G · sdb 1T · /tmp): Belegung, **reserved-frei** (was root noch
  hat), Trend.
- **Wer füllt?** in der Flotte: welches System schreibt nach /tmp (quantbot-Heapsnapshots).
- **Diagnose-Reflex eingebaut:** „codium connect fails" → Tab zeigt sofort Disk-Marge
  rot, statt dass es wie ein Netz-/Auth-Problem aussieht.
- **Alarm:** > 85% je FS → WhatsApp + Disk-Achse ▲ (analog Swap/Load).

# Status-Heuristik — „gut / nicht gut"

```
● ok         Headroom da · Arbeit fließt · keine 429-Häufung · kein Stau · Disk-Marge ok
▲ voll       Lane saturiert (no-idle + Bead-Stau) ODER Provider nahe Rate-Limit
✗ stuck      Agent claimed aber kein Fortschritt > N Sweeps
⚠ disk       System füllt eine FS Richtung Schwelle (z.B. quantbot → /tmp)
⚠ kritisch   Freeze-Marge ODER Disk-Marge niedrig — Governor-STRESS
○ aus        System definiert aber 0 live
```

# Governor-Rückkopplung

Dieselben Achsen (RAM · CPU · API · Disk) sind die Admission-Signale des
capacity-governor. Das Cockpit *zeigt* den Druck je Achse; der Governor *drosselt*
die glühende Achse. Konkret: 429-Rate je Provider **und** Disk-Pressure werden
Stress-Kriterien (analog `GOV_STRESS_PSI` → `GOV_STRESS_DISK_PCT`). Tab und Governor
teilen den Collector-Datensatz (ein Owner, s.o.).

# Erfolgskriterien (messbar)

- **M1 (keine Waisen):** jeder live-Agent ist **genau einer** System/Provider-Zeile
  zugeordnet — keine Waisen, keine Doppelzählung. NICHT „Summe = Total-RAM" (shared
  Pages + nested cgroups). Validierung: ps/cgroup-Liste ⨝ Tab-Zeilen.
- **M2 (Discovery):** ein NEU gestartetes System erscheint nach reinem Eintrag in der
  Discovery-Tabelle (kein Code-Change) — bzw. ganz ohne Eintrag im `other`-Bucket.
- **M3 (Rate-Limit-Erklärung):** bei realen Claude-429 → Claude-Zeile `▲ rate` mit
  Agenten-Count + 429-Rate + **Top-N-Contributors** (Token/Request-Durchsatz). Kein
  „der eine Treiber" (429 = Shared-Limit, Aggregat-Durchsatz), sondern die Haupt-
  Verbraucher.
- **M4 (Kapazität live):** Headroom/Governor-Verdict im Tab = der vom Collector
  gelieferte Datensatz (kein Doppel-Read, gleiche Quelle wie der Governor).
- **M5 (Health-Ampel):** saturierte Lane → `▲ voll`; dozing Polecat → `✗ stuck`;
  ruhiges System → `● ok`. An Live-Zuständen verifiziert.
- **MD (Disk-Achse):** eine FS Richtung Schwelle → die schreibende System-Zeile
  zeigt `⚠ disk` + die FS-Kapazitäts-Zeile ▲; bei > 85% Alarm.

# Risiken & offene Fragen

- **R1 (Transkript-Parsing-Last):** .jsonl inkrementell tailen (Offset je Datei),
  nie voll neu parsen.
- **R2 (Claude-Ceiling unbekannt):** echtes Subscription-Limit nicht auslesbar →
  429-RATE als Proxy, ehrlich als Annäherung labeln.
- **R3 (Identitäts-Join):** Session-UUID ↔ PID/Workspace ist die offene Brücke (P0).
- **R4 (Provider eines vk-Workspace):** aus Bead/executor-classify-Pick, nicht nur ps.

# Non-Goals

- **Kein** Eingriff in Governance-Mechanik (Caps/Swap/Floors/Disk-Reserve) — nur Sicht
  + die API/Disk-Signale als Governor-Input.
- **Kein** €-Token-Billing (nur Auslastung/429).
- **Keine** Steuerung aus dem Tab (read-only; Aktion bleibt Governor/Reactor).

# Rollout-Phasen (Granularität, keine Zeit)

```
P0 Spike ──► P1 Collector ──► P2 Tab (Kapazität+Flotte+Disk) ──► P3 API-Hälfte ──► P4 Governor-Input
(Identitäts-   (cgroup+ps+df+      (Säulen rendern,             (Transkript-429-    (429- + Disk-Pressure
 Join · Push    provider-disc      Daten da)                    Parser + API-Säule  als Stress-Kriterien)
 bestätigt)     → Datensatz)                                    + Top-N)
```
- **P0 done** = ein realer vk-Workspace korrekt `PID→cgroup→Slice→Provider→Bead`
  zugeordnet (R3/R4); Push-Topologie + kanonischer Owner bestätigt (st-7xcy0 ✓);
  Provider-Discovery-Tabelle initial; Disk-FS-Liste (root/sdb/tmp) erfasst.
- **P1 done** = Collector liefert vollständigen Datensatz inkl. df → **M1**.
- **P2 done** = Tab rendert Kapazität (inkl. Disk) + Flotte; Headroom = Sensor (**M4**);
  Health-Ampel an Live-Zuständen (**M5**); Discovery-Test (**M2**); Disk-Achse (**MD**).
- **P3 done** = Transkript-429-Parser → `▲ rate` + Top-N (**M3**).
- **P4 done** = 429-Rate + Disk-Pressure als Governor-Stress (hinter Flag, default aus).

# Reversibilität

Reiner Push-Read-Collector + ein Tab — additiv, kein Eingriff in bestehende Pfade.
Collector als eigener Dienst (stoppbar). Governor-Input (P4) hinter Flag, default aus.

---

> **Rekonstruktion 2026-06-20:** Die volle 3-Säulen-Fassung (Quick approved-with-notes
> + Deep-Tech needs-changes, 6 Asks eingearbeitet) lag nur im Working-Tree und wurde
> vom Collector-Topologie-Bead (st-7xcy0) überschrieben. Hier zusammengeführt: volle
> Vision + die st-7xcy0-Arbeit als gelöste O1/Owner-Sektion + die Disk-Achse (4. Dimension).
