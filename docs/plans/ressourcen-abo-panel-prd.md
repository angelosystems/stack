---
title: Ressourcen-Panel „Abos“ — Abo-Limits live + Service-Registry im Master-Kanban
slug: ressourcen-abo-panel
layer: prd
status: in-progress
parent_plan: resource-fleet-manager-prd.md
software: master-kanban
scope: Die /ressourcen-Seite bekommt zwei neue Sektionen — Abo-Karten mit echten Server-Limits (5h/7d/Modell-Fenster) für alle Claude-Abos + Gemini-€-Budget, und eine Service→Abo-Registry-Sicht (Paperclip, Fabrik, VK, Solartown, AP, opencode). Datenquellen: claude-abo-watch (existiert), oneapi-spend-guard (JSON-Output neu), resources.yaml (neu, git).
created: 2026-07-11
owner: mario.gemuenden@stayawesome.de
target: master.stayawesome.app/ressourcen
review:
  quick: auto
  deep: none
references:
  - resource-fleet-manager-prd.md (Dach — API-Budget-Säule; dessen R2 „Ceiling nicht auslesbar“ ist widerlegt)
  - /root/mario-brain/vault/angeloos/wiki/claude-abo-inventar.md (Account-Inventur 2026-07-11)
  - /opt/claude-abo-watch/ (Collector Claude-Abos, gebaut 2026-07-11)
  - /usr/local/bin/oneapi-spend-guard (Collector Gemini-€)
---

<!-- WORD_HYGIENE_EXEMPT-FILE: externe Namen (Claude, Anthropic, Gemini, GLM, DeepSeek, Z.ai, Paperclip, ActivePieces, oauth, ratelimit, sk-ant-oat, systemd, nginx, fsnotify, YAML, curl, jq). -->

# Why

Vier Claude-Abos (claude1, claude2, info@, Mario persönlich) plus Gemini-,
GLM- und DeepSeek-Kontingente treiben alle Agenten-Systeme — aber es gibt
keine Sicht, welches Abo wie voll ist und welcher Service auf welchem Konto
läuft. Konsequenz diese Woche: der Paperclip-CEO stand zwei Tage, weil sein
Konto still am Wochenlimit hing; info@ lief unbemerkt ins 5h-Limit, während
claude2 bei 23 % Wochenlast idlete. Token-Kapazität verfällt auf einem Konto,
während ein anderes drosselt.

Seit 2026-07-11 existiert `claude-abo-watch` (werkstatt): er liest **echte
Server-Limits** aller Claude-Abos (usage-API bei Voll-Scope-Token,
ratelimit-Header per 1-Token-Ping bei setup-Token) und alarmiert per
WhatsApp. Damit ist die R2-Annahme des Dach-PRDs („Subscription-Limit nicht
auslesbar, 429-Rate als Proxy“) **widerlegt** — der API-Säule fehlt nur noch
die Oberfläche. Und: WhatsApp meldet Ereignisse, beantwortet aber keine
Fragen wie „auf welches Konto lege ich den nächsten Fabrik-Lauf?“.

# Goal

Auf `master.stayawesome.app/ressourcen` auf einen Blick: **welches Abo hat
wie viel Luft** (Balken je Fenster, Ampel, Reset-Zeit) und **welcher Service
zieht aus welchem Abo** (Registry-Tabelle, alle Boxen). Dieselben Schwellen
wie der Watcher — eine Wahrheit, zwei Ausgaben (Alarm + Panel).

# Verhältnis zum Dach-PRD (resource-fleet-manager)

```
resource-fleet-manager  →  HOST-Sicht werkstatt: wer (Prozess/Agent) verbrennt RAM·CPU·API·Disk — discovery-basiert
ressourcen-abo-panel    →  PROVIDER-Sicht cross-box: wie viel Luft je Konto + wo ist jedes Konto hinterlegt — registry-basiert
```

Beide teilen die Seite `/ressourcen` (heute 3 Sektionen: Kapazität, Agenten,
Flotte). Dieses PRD ergänzt Sektion 4 „Abos & Token-Budgets“ und Sektion 5
„Service → Abo“. Die Fleet-Säule „API-Budget“ (P3 dort, 429-Proxy) kann
später auf den Watcher-Datensatz umziehen — hier Non-Goal.

**Abgrenzung Discovery vs. Registry (bewusst):** Das Dach-Leitprinzip
„discovery-basiert, nicht hardcoded“ gilt für werkstatt-Prozesse. Abo→Service-
Bindings liegen aber auf vier Boxen (werkstatt, vault, mario-prod, sa-prod)
in Token-Files und DB-Secrets — nicht discoverbar ohne Credential-Scans.
Darum: **Stammdaten als git-versioniertes YAML**, Pflege bei Token-Änderung
(dieselbe Hand, die den Token verdrahtet, trägt die Zeile nach — analog
Vault-Konvention „neue Credentials in den Bucket“).

# Architektur

```
claude-abo-watch (Timer 20min) ── state/last.json ──┐
oneapi-spend-guard status --json (NEU) ─────────────┼──► master-kanban GET /api/abos ──► /ressourcen Sektion 4+5
resources.yaml (/opt/stack, git, fsnotify) ─────────┘        (Go, :7780, Cache 60s)         (Alpine+Tailwind)
```

- **Ein Owner pro Datensatz** (Dach-Prinzip): Watcher = Owner der
  Claude-Limits, spend-guard = Owner der Gemini-€, Registry = Owner der
  Bindings. Der Endpoint merged nur, misst nie selbst.
- **Stale-aber-ehrlich** (Dach-Prinzip): `last.json` bekommt `generated_at`;
  Panel zeigt „Stand: vor Xs“ und markiert tote Feeds sichtbar.

## Registry: `/opt/stack/tools/portfolio/resources.yaml`

```yaml
resources:
  - id: claude1            # kind: claude-abo | api-budget | plan-ohne-feed
    kind: claude-abo
    plan: max
    feed: claude-abo-watch  # Watcher-Account-Name
    bindings:
      - {service: werkstatt CLI-Sessions, box: werkstatt}
  - id: claude2
    kind: claude-abo
    plan: max
    feed: claude-abo-watch
    bindings:
      - {service: Coding-Fabrik (7 claude_local), box: vault}
      - {service: Paperclip-CEO, box: mario-prod}
  - id: info
    kind: claude-abo
    plan: max
    feed: claude-abo-watch
    bindings:
      - {service: Paperclip Stay Awesome, box: sa-prod}
      - {service: Paperclip QuantumShift (paperclip-server.env), box: mario-prod, verified: false}
  - id: mario
    kind: claude-abo
    plan: max
    feed: none              # Token fehlt — Karte zeigt „nicht messbar“
    bindings:
      - {service: Mario Desktop, box: mac}
  - id: gemini-ultra
    kind: api-budget
    feed: oneapi-spend-guard
    bindings:
      - {service: Solartown-Polecats via one-api, box: werkstatt}
  - id: glm-zai
    kind: plan-ohne-feed
    bindings:
      - {service: Vibe Kanban (Standard-Executor), box: werkstatt}
      - {service: Solartown plan-reviewer + sage-advisor, box: werkstatt}
  - id: deepseek
    kind: plan-ohne-feed
    bindings:
      - {service: opencode QuantumShift-CTO, box: mario-prod}
```

Startbefüllung = Inventur 2026-07-11 (Vault-Doku). `verified: false`-Zeilen
werden im Panel als „unverifiziert“ gerendert; die Verifikation (z. B.
mario-prod `paperclip-server.env` == info@ per 7d-Reset-Vergleich) ist Teil
von P1. ActivePieces-Flows werden bei Befüllung geklärt (nutzen heute
gt-llm-sidecar / keine eigenen LLM-Abos — zu verifizieren) und als Binding
der tatsächlichen Ressource eingetragen.

## Collector-Änderungen (klein, je Owner)

1. `claude-abo-watch check`: `generated_at` in `last.json` (eine jq-Zeile).
2. `oneapi-spend-guard status --json`: Monats-€/Limit, Tages-€/Tripwire,
   Guard-Zustand (enabled/tripped) als JSON auf stdout. Textausgabe bleibt.
3. Kein neuer Daemon, kein neuer Timer (Watcher-Timer existiert als Unit,
   Install steht aus — Vorbedingung, siehe R1).

## Endpoint `GET /api/abos` (master-kanban, Go)

Merged Registry + Feeds zu einem Antwort-JSON pro Ressource: `id, kind,
plan, bindings[], limits[] (window, percent, severity, resets_at), feed_age_s,
feed_status (ok | stale | fehlt | error+hint)`. YAML-Read via fsnotify-Cache
(Muster planfile-adapter), Feed-Reads mit 60s-Cache. Fehlende/kaputte Feeds
liefern `feed_status`, nie ein leeres Weglassen der Ressource.

## UI: zwei Sektionen in `ressourcen.html` (Idiom der Seite: Alpine+Tailwind)

```
┌─ ABOS & TOKEN-BUDGETS ────────────────────────────────────────────────┐
│ claude1 · Max        claude2 · Max        info · Max      mario · Max │
│ 5h ███████░░ 63%     5h █░░░░░░░░  2%     5h ██████████ 100% ✗        │
│ 7d █████░░░░ 49%     7d ███░░░░░░ 23%     7d ████░░░░░░ 38%   Token   │
│ Fable ████████ 79%▲  Reset 15.07. 21:00   Reset 11.07. 05:20  fehlt   │
│ [werkstatt-Sessions] [Fabrik ×7][CEO]     [PC-SA][PC-QS?]     [Mac]   │
│ gemini-ultra · one-api  Monat ██░░ 41€/200€ · Tag 3€/100€ · Guard ok  │
│ glm-zai · kein Feed [VK][Reviewer]   deepseek · kein Feed [CTO]       │
├─ SERVICE → ABO ───────────────────────────────────────────────────────┤
│ Service                      Ressource   Box        Status            │
│ Coding-Fabrik (7 Agenten)    claude2     vault      ● ok              │
│ Paperclip QuantumShift       info ?      mario-prod ✗ 5h-Limit  unver.│
│ Vibe Kanban                  glm-zai     werkstatt  ○ kein Feed       │
│ …                                                                     │
└───────────────────────────────────────────────────────────────────────┘
```

Ampel = Watcher-Schwellen (WARN ≥ 80 %, CRIT ≥ 95 % bzw. API-severity),
im Go-Endpoint berechnet — Frontend rendert nur. Service-Zeilen erben die
schlechteste Ampel ihrer Ressource.

# Erfolgskriterien (messbar)

- **M1 (Vollständigkeit):** Jede Registry-Ressource erscheint im Panel —
  mit Feed als Balken+Reset, ohne Feed als ausgewiesene „kein Feed“/„Token
  fehlt“-Karte. Nichts wird verschluckt. Validierung: YAML-Einträge ⨝
  gerenderte Karten.
- **M2 (eine Wahrheit):** Panel-Ampel und WhatsApp-Alarm feuern auf
  denselben Zustand — verifiziert an einem realen Limit-Fall (z. B. info@
  5h-rejected: Karte rot UND Alarm-Marker im Watcher-State vorhanden).
- **M3 (Service-Frage beantwortet):** „Welches Abo nutzt <Service>?“ ist
  ohne Grep beantwortbar für: beide Paperclip-Instanzen, Coding-Fabrik,
  CEO, VK, Solartown-Crew, AP-Flows, opencode-CTO, werkstatt-Sessions,
  Mario-Desktop. Unverifizierte Bindings sind sichtbar markiert; die zwei
  offenen Verifikationen (mario-prod-Token, AP-Flows) sind in P1 erledigt.
- **M4 (stale sichtbar):** Watcher-Timer gestoppt → Panel zeigt Feed-Alter
  rot statt eingefrorener Zahlen.
- **M5 (Registry-Erweiterung ohne Code):** Neue Ressource/Binding =
  YAML-Zeile + ggf. Watcher-`accounts.conf`-Zeile — kein Go-/HTML-Change.

# Risiken & offene Fragen

- **R1 (Vorbedingung Timer):** Watcher-Units liegen bereit, Install ist
  deny-gated (/etc/systemd) → braucht Marios Go. Ohne Timer ist das Panel
  ein Standbild (M4 macht das wenigstens sichtbar).
- **R2 (claude1-Token rotiert):** Session-Token aus `.credentials.json`
  läuft ab, wenn länger keine werkstatt-Session lief → Karte „Token
  abgelaufen“ mit Fix-Hinweis. Bewusst KEIN setup-Token-Ersatz als Default:
  der verlöre die Modell-Fenster-Detailtiefe (7d-Fable) der usage-API.
- **R3 (Ping-Nebenwirkung):** Header-Probe kostet ~23 Token je Messung und
  hält das 5h-Fenster des beobachteten Abos aktiv (Fenster startet mit
  erster Anfrage). Bewusst akzeptiert, im Watcher dokumentiert.
- **R4 (inoffizielle Endpoints):** usage-API und ratelimit-Header sind
  undokumentiert und können sich ändern → Watcher degradiert auf
  Fehler-Karte + „nicht messbar“-Alarm; Panel bleibt ehrlich statt falsch.
- **R5 (Registry-Drift):** Bindings sind Handpflege — Drift-Risiko analog
  Vault-credentials.md, gemildert durch git-Review + M3-Verifikationsliste.

# Non-Goals

- **Kein Steuern/Routing** aus dem Panel (Abo-aware Dispatch = eigene
  Ausbaustufe am capacity-governor, nicht hier).
- **Kein €-Billing für Claude-Abos** (Flatrate; €-Sicht nur für one-api).
- **Keine Per-Agent-Attribution** „wer hat die Token verbrannt“ — das ist
  Fleet-P3 (Transkript-Parser) im Dach-PRD.
- **Kein Token-Minting-Flow** (setup-token-Tanz bleibt manueller Ablauf).

# Rollout-Phasen (Granularität, keine Zeit — je Phase Gate nach ADR-0009)

```
P1 Registry+Endpoint ──► P2 Panel-Sektionen ──► P3 Gemini-JSON ──► P4 Best-effort-Feeds
(resources.yaml initial     (Sektion 4+5 in         (spend-guard        (DeepSeek balance-API;
 + Verifikationen M3         ressourcen.html,        --json + Karte)     Mario-Token nach
 + /api/abos + last.json     M1/M2/M4 grün)                              setup-token-Tanz)
 generated_at)
```

- **P1 done** = `/api/abos` liefert alle Registry-Ressourcen mit Live-Zahlen
  der Claude-Feeds; beide offene Bindings verifiziert oder als offen markiert.
- **P2 done** = M1, M2, M4 an der Live-Seite verifiziert.
- **P3 done** = Gemini-Karte zeigt Monats-/Tages-€ gegen Limits aus dem Guard.
- **P4 done** = mindestens ein zusätzlicher Feed live ODER dokumentiertes
  Nicht-Gehen (z. B. Z.ai ohne Quota-API).

# Reversibilität

Rein additiv: zwei HTML-Sektionen, ein Go-Endpoint, ein YAML, zwei
Collector-Flags. Rückbau = Sektionen + Route entfernen; der Watcher bleibt
eigenständig nützlich (WhatsApp-Alarm unabhängig vom Panel).

## Reviewer-Verdict — quick (glm-5.2) — 2026-07-11

**Verdict:** `approved`

Ein außergewöhnlich rundes PRD: Das Problem ist mit konkreten Vorfällen belegt, die Scope-Abgrenzung ist explizit und plausibel, jede Phase hat harte Done-Kriterien, Alternativen werden bewusst verworfen, und Risiken inklusive Reversibilität werden ehrlich adressiert. Es gibt weder Major- noch Minor-Findings — das Dokument hält sauber alle Konventions-Vorgaben ein, insbesondere das Verbot von Zeitschätzungen.
