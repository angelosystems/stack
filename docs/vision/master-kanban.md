---
title: Master-Kanban / Arbeitsoberfläche
status: vision
created: 2026-05-03
source_chat: extract aus Chat über Master-Kanban-Mockup
related:
  - mario-brain (host für diese Vision)
  - AngeloOS (Meta-Layer in dem das Cockpit lebt)
  - vk (vibe-kanban — heute Sub-Boards pro Workspace)
  - bd, gt-mq (Solartown-Beads, native Detail-Layer)
---

# Master-Kanban / Arbeitsoberfläche — Vision-Extract

Dieser Extract bündelt einen Chat-Verlauf vom 2026-05-03, in dem Mario die Vision einer Master-Arbeitsoberfläche skizziert hat. Soll als Startpunkt für einen Folge-Chat dienen.

## Kern-Bedürfnis

Mario verwaltet heute **Sessions** auf mehreren Layern:
- vk-Workspaces (vibe-kanban)
- Solartown-Beads (`bd`) und `gt-mq`-Cards
- GitHub-PRs, `docs/plans/*-backlog.md` pro Repo
- Eigene mentale Top-Level-Sicht (Solartown, QuantBot, Stay Awesome, mario-brain, AngeloOS)

Das fühlt sich **unstrukturiert** an. Er schiebt Sessions von A nach B, hat unter Sessions wieder Sessions, verliert den Überblick auf Initiative-Ebene.

**Was er will:** Ein Master-Kanban als Teil eines KI-OS — Chat-Fenster + Schaltzentrale, in der Initiativen visuell rumgeschoben werden.

## Granularitäts-Frame

Wichtigste Entscheidung im Chat:

> "Ich denke nicht so an Sub-Themen sondern eher an Über-Themen.
>  Wie behalte ich als zukünftiger KI-Entwickler den Überblick
>  über die ganzen Sachen, die mein System baut und die ich baue?"

Granularität ist also **Portfolio-Level**, nicht Task-Level.

- Eine Karte = eine **Initiative** (~2-Wochen-Brocken)
- Nicht: Einzel-Task (lebt in vk/bd/gt-mq)
- Nicht: ganzes Programm (zu groß für Karte)
- Heuristik: Karte bewegt sich nie → zu groß. Bewegt sich täglich → zu klein.

## Strukturierungs-Regeln

```
Regel 1 — Granularität fixieren     (Initiative, ~2 Wochen)
Regel 2 — Swimlanes = stabil        (Domains/Firmen ändern sich kaum)
         Spalten = Bewegung         (Workflow-Phasen mit WIP-Limits)
Regel 3 — Master ≠ Detail           (Drill-Down auf natives Tool,
                                     Tasks niemals ins Master mischen)
```

## Layout (Stand des Chats)

Multi-Firma-View über Filter-Pills, gestackte Boards pro Firma:

```
┌────────────────────────────────────────────────────────────────────┐
│  master-portfolio                                                  │
│  Firmen: [✓ Stay Awesome] [✓ Solartown] [ QuantBot] [ m-brain] [✓] │
├────────────────────────────────────────────────────────────────────┤
│                                                                    │
│  ╔══ 🏨 STAY AWESOME ═══════════════════════════════════ 12 ╗      │
│  ║ Idea     │ Now (3/4) │ Soon  │ Watching  │ Done        ║      │
│  ║ Mews     │ Markthalle│ Docu  │ DNS-Tx    │ SSO  Office ║      │
│  ║ Vision   │ Bürgsch.  │ WDL   │ Bitwarden │ DNS  Inbox  ║      │
│  ║          │ Fred VSOP │       │           │             ║      │
│  ╚════════════════════════════════════════════════════════╝      │
│                                                                    │
│  ╔══ ⚡ SOLARTOWN ══════════════════════════════════════ 6  ╗      │
│  ║ Idea     │ Now (2/4) │ Soon  │ Watching  │ Done        ║      │
│  ║          │ Promote   │       │ Sage-2    │ V15         ║      │
│  ║          │ Sage-Adv  │       │ PG-Decom  │             ║      │
│  ╚════════════════════════════════════════════════════════╝      │
│                                                                    │
│  ╔══ ⚙ ANGELOOS ══════════════════════════════════════ 2   ╗      │
│  ║ Idea     │ Now (1/4) │ Soon  │ Watching  │ Done        ║      │
│  ║ Master   │ Portfolio │       │           │             ║      │
│  ╚════════════════════════════════════════════════════════╝      │
│                                                                    │
└────────────────────────────────────────────────────────────────────┘
```

**Eigenschaften:**
- Pills oben: Multi-Select Firmen, Default = alle
- Boards vertikal gestapelt, jede Firma full-width
- Spalten: `Idea | Now | Soon | Watching | Done` (universell pro Firma)
- WIP-Limit pro `Now`-Spalte (Default 4) — wird rot wenn überschritten
- Lane-Farben: ⚡ amber / 💰 grün / 🏨 cyan / 🧠 violett / ⚙ grau
- Karte = kompakt: Titel + Status-Dot + Drill-Link
- Click → Modal mit Detail + Drill-URL ins native Tool (vk/bd/gt-mq/docs)
- Drag innerhalb Firma erlaubt, Cross-Firma-Drag nicht (Karten ändern selten Eigentümer)

## Größere Vision: KI-OS-Einbettung

> "Ich habe so eine Vision, im Endeffekt, wo ich einen Chat irgendwie habe,
>  ein KI-Fenster. Und dann habe ich einen OS, praktisch KI-OS, wo im
>  Endeffekt sowas auftauchen würde."

Master-Kanban ist **not** standalone — es ist **eine Ansicht in einer übergeordneten Schaltzentrale**. Mario hat im Chat erwähnt: "Es gibt ja schon so eine Schaltzentrale und das ist im Endeffekt nochmal eine andere Ansicht."

**Offen:** Welche Schaltzentrale ist gemeint? Optionen die im Chat aufkamen, ohne Klärung:
- Kingdom (QuantBot-Dashboard, :3333, Nürnberg)
- mario-brain UI (gibt es noch keine Web-Oberfläche?)
- Stay-Awesome-Cockpit hinter SSO
- Neues AngeloOS-Cockpit (existiert noch nicht)

Möglich, dass die "Schaltzentrale" als Konzept noch unbesetzt ist und der Master-Kanban die erste Bewohnerin sein darf.

## Stufenplan zur Umsetzung

```
[1] Mockup standalone        →  HTML-File, klickbar, kein Build
       │                         /root/mario-brain/mockups/master-kanban.html
       │                         Layout final, Daten hardcoded
       ▼
[2] Daten extrahieren        →  data/portfolio.json
       │                         ~25 Initiativen aus Memories
       │                         schema: {id, firma, stage, title, drill_url}
       ▼
[3] In Schaltzentrale        →  /master Route in <Schaltzentrale>
       │                         Next.js-Page (Lane A), liest portfolio.json
       │                         Drag-Drop schreibt zurück
       ▼
[4] Persistenz hochziehen    →  Postgres-Tabelle (solartown-DB?),
                                 Edit-Log statt File-Overwrite
```

**Status im Chat:** Schritt 1 wurde geplant aber noch nicht ausgeführt — der Chat eskalierte auf die Vision-Frage und die Tenant-Trennung-Frage.

## Architektur-Kontext: Tenant-Trennung

Nebenstrang im Chat — relevant fürs Master-Kanban, weil Initiativen pro Firma getaggt sind und das Master-Cockpit die Trennungs-Konvention durchsetzen muss.

**Frame:** nicht "wie trenne ich alles", sondern "was muss isoliert sein, damit der Rest geteilt sein darf".

```
        ┌─────────── shared (synergie) ─────────────┐
        │  AngeloOS-Layer · Hetzner-Infra · Tools   │
        │  Mario's Wissen/Memories (Domain-tagged)  │
        └────────────┬──────────────────────┬───────┘
                     │ Tenant-Context       │
        ┌────────────▼─────────┐  ┌─────────▼──────────┐
        │  STAY AWESOME GmbH   │  │  QUANTBOT (privat) │
        │  • Vault-Segment     │  │  • Vault-Segment   │
        │  • Persona Gaia AI   │  │  • Persona Mario   │
        │  • Customer-Daten    │  │  • Trades-Daten    │
        │  • Buchhaltung/SKR04 │  │  • Wallet/Steuern  │
        └──────────────────────┘  └────────────────────┘
```

**Pflicht-Isolations-Kern:**
1. Secrets-Vault — `/root/.secrets/{stayawesome,quantbot}/`
2. Persona — Tools wissen welcher Tenant aktiv ist
3. Operationelle Daten — Customer-DB ≠ Trades-DB, niemals JOIN-bar
4. Cash/Verträge — eh getrennt seit GmbH

**Migrations-Phasen, nach Risiko-Reduktion:**

```
P1 │ Vault segmentieren        │ DSGVO + Hack-Blast-Radius   │ lokal, reversibel
P2 │ Postgres-Rollen splitten  │ Cross-Tenant-Read verhindern│ lokal, reversibel
P3 │ Memory-Tree taggen        │ Wissens-Kontext-Klarheit    │ lokal, reversibel
P4 │ GitHub-Org Split          │ CI-Tokens, Repo-Sichtbarkeit│ extern, einmalig
P5 │ Hetzner-Projekt Split     │ API-Token-Kompromiss        │ extern, Migration
P6 │ DNS-Account Split         │ DNS-Hijack-Blast-Radius     │ extern, Pflege
P7 │ AngeloOS-Instanzen Split  │ Crew-Cross-Talk             │ groß, dauerhaft
```

**Pick im Chat:** P1–P3 zuerst (lokal, reversibel, reduziert echtes Risiko). P4–P7 erst bei konkretem Trigger (Compliance-Audit, Mitarbeiter-Onboarding, OSS-Release).

## Was offen geblieben ist

Punkte die der Folge-Chat aufgreifen kann:

1. **Welche Schaltzentrale?** — Konzept oder existierendes Cockpit?
2. **Initiative-Inventur** — die ~25 Karten aus den Memories konkret listen, prio-sortiert
3. **Sub-Board-Bridge** — wie genau drillt eine Master-Karte in vk/bd/gt-mq? URL-Konvention oder Embed?
4. **Vault-Audit (P1)** — Inventur was unsegmentiert in `/root/.secrets/` liegt
5. **Mockup-File bauen** — Schritt 1 des Stufenplans, blockiert keine andere Klärung
6. **WIP-Limits-Default** — 4 angenommen, evtl. pro Firma anders sinnvoll
7. **Cross-Firma-Karten** — gibt's Initiativen die zu mehreren Firmen gehören? (z.B. AngeloOS-Tooling das Stay-Awesome bedient)

## Marios Konventionen die hier reinspielen

- **Domain-Namen vor Abstraktionen** — Solartown/QuantBot/Stay Awesome direkt nutzen, keine eigenen Lanes
- **Backlog im Repo, nicht Notion** — also auch Master-Kanban-State im Repo
- **ASCII im Chat, Mermaid in Files** — diese Datei ist File → Mermaid könnte ergänzen
- **Frontend-Stack-Lanes ADR-0008** — Cockpit-UI = Lane A (TS+React+Next.js 16)
- **No time estimates** — Stufenplan oben hat bewusst keine
- **Pläne aktuell halten** — diese Datei sollte gepflegt werden bis Master-Kanban steht
