---
title: Master-Kanban MCP-Copilot — KI pro Karte & pro Board
slug: master-kanban-mcp-copilot
status: approved-with-notes
layer: prd
parent_plan: /opt/stack/docs/plans/master-kanban.md
scope: KI ins Board bringen — ein konversationeller Agent, der den Karten-/Board-Kontext liest und durch die bestehenden Board-Endpoints handelt; das Gespräch lebt als Karten-Events. Über einen master-kanban-MCP, der zur einen Aktionsfläche für alle Clients (Drawer-Chat, Sessions) wird.
created: 2026-06-15
review:
  quick: auto
  deep: spec-panel
  panel-mode: critique
  panel-focus: [architecture, requirements]
references:
  - /opt/stack/docs/plans/master-kanban.md
  - /opt/stack/docs/plans/master-kanban-dispatch-prd.md
  - /opt/stack/docs/plans/master-kanban-bead-linkage-prd.md
---

# Master-Kanban MCP-Copilot — KI pro Karte & pro Board

## Problem

KI ist heute nur auf der **Bau-Seite** des Boards (Polecats setzen approved
PRDs autonom um). Auf der **Denk-/Lese-/Entscheide-Seite** fehlt sie: wer am
Board arbeitet, kann nicht mit einer KI sprechen, die den Karten- bzw.
Board-Kontext kennt und durch das Board handeln kann. Man wechselt für jede
Frage („was blockiert das? was sollte ich heute angehen?") in eine separate
Session und kopiert Kontext von Hand.

## Ziel

Ein **einziger konversationeller Agent**, zwei Zoom-Stufen:
- **pro Karte** — Scope = diese Initiative (Plan-Baum + Beads + Events + PRD-Text)
- **pro Board** — Scope = alle Initiativen + Capacity + Backlog (Triage)

Er **liest** den Kontext (der schon auf der Karte aggregiert ist), **handelt**
durch die bereits live geschalteten Board-Endpoints (dispatch/move/comment/
create-bead), und das **Gespräch persistiert als Karten-Events** (rendert eh im
Drawer). Kein neuer Chat-Store, kein neues LLM-Setup, keine neue Abstraktion.

## Nicht-Ziele

- **Kein Hermes/Notion.** Hermes ist die Schaltzentrale (persönliches Cockpit,
  Notion-SoT) — eine andere Oberfläche. Dieses PRD ist Master-Kanban-eigen.
- **Kein neuer autonomer Agent.** Die autonome Umsetzung bleibt die
  PRD→Bead→Polecat-Pipeline. Dies ist der **interaktive** Copilot daneben.
- **Kein eigenes LLM-Hosting** — nutzt one-api (:4000) / gt-llm-sidecar (:4100).
- Keine Generierung außerhalb des Board-Kontexts (kein allgemeiner Chatbot).

## Lösung

### L1 — `master-kanban-mcp` (die eine Aktionsfläche)
Ein MCP-Server (gleiches Muster wie das self-hosted github-mcp), der das Board
als MCP exponiert:
- **Resources** (read): `initiative/<id>` (inkl. Plan-Baum, Beads, vk, PRs,
  Events), `plan-file/<ref>` (PRD-Text), `board` (alle Initiativen + Capacity +
  Backlog).
- **Tools** (act): `dispatch`, `move-stage`, `comment`, `create-bead`,
  `promote-stage` — **dünne Wrapper auf die existierenden `/api/*`-Endpoints**,
  nicht neue Logik. Gleiche Auth, gleiche Event-Emission, gleiche Routing-Regeln.

**Alternative verworfen:** der Drawer-Chat ruft LLM + Endpoints direkt (ohne
MCP). Verworfen, weil dann jeder Client (Drawer, Sessions, künftige) die
Tool-/Auth-Logik dupliziert. MCP = eine Fläche, viele Clients.

**Reversibilität:** Die MCP-Tools sind dünne Wrapper auf die `/api/*`-Endpoints
— bewährt sich das MCP-Muster nicht, kann der Drawer-Chat direkt gegen dieselben
Endpoints gehen. Rückbau = die eine Wrapper-Schicht entfernen, kein tief
verankerter Umbau; die Endpoints (die Wahrheit) bleiben unberührt.

### L2 — Per-Karte Drawer-Chat (dünner Client)
Im bestehenden Detail-Drawer ein Chat-Block. Lädt die Karte als MCP-Kontext,
spricht mit dem Agenten (Claude/GLM via one-api), der über die MCP-Tools handeln
kann. Kein eigener State — siehe L4.

### L3 — Per-Board Chat (gleicher Agent, weiter Scope)
Header-Chat mit `board`-Resource als Kontext. Triage: „was ist stale?",
„woran heute arbeiten?". Identischer Agent, nur anderer MCP-Scope — fällt aus
L1 von selbst, kein Extra-Bau.

### L4 — Gespräch = Karten-Events
Jede Chat-Nachricht (User wie KI) wird als `initiative_event`
(`kind='ai_message'`/`'ai_action'`) geschrieben — rendert in der bestehenden
Event-Historie. Die KI ist damit ein **Actor im Event-Log** wie der
solartown-adapter. Kein separater Chat-Store; das Board wird selbst-dokumentierend.

### L5 — Aktions-Sicherheit (Propose → Confirm)
Lese-/Zusammenfass-Antworten laufen frei. **Mutierende** Tools (besonders
`dispatch`, das echte Compute auslöst) werden von der KI **vorgeschlagen** und
mit einem Klick im Drawer bestätigt (KI entwirft, Mensch committet). Für
Live-Geld-Karten (firma=quantbot Trading-Path) gilt die bestehende Regel:
nur PRD→Deep-Tech-Pfad, KI darf nicht in Live-Code dispatchen, nur PRD-Lane
vorschlagen.

## Success-Criteria

- SC1: Im Karten-Drawer eine Frage stellen → Antwort ist mit dem realen
  Karten-Kontext gegroundet (referenziert Beads/Events/PRD der Karte, nicht
  Halluzination); verifizierbar an einer Karte mit bekanntem Inhalt.
- SC2: Die KI kann eine mutierende Aktion (z.B. `move-stage`) **vorschlagen**;
  nach Klick-Bestätigung wird sie über den bestehenden Endpoint ausgeführt und
  erscheint als Event auf der Karte.
- SC3: Jede Chat-Nachricht + jede KI-Aktion liegt als `initiative_event` vor
  und rendert in der Drawer-Historie (kein separater Store).
- SC4: Der Board-Chat beantwortet eine Triage-Frage über mehrere Initiativen
  (z.B. „welche 3 Karten sind am längsten ohne Aktivität").
- SC5: Mutierende MCP-Tools sind auth-gegated (gleicher Pfad wie `/api/dispatch`);
  ein unautorisierter Call wird abgewiesen.
- SC6: Eine `dispatch`-Aktion auf eine quantbot-Live-Karte wird von der KI
  **nicht** als Hacker-Lane angeboten, sondern nur als PRD-Lane (Live-Geld-Regel).

## Risiken / offene Fragen

- R-A: LLM-Cost-Runaway bei großem Board-Kontext — Kontext-Budget pro Gespräch,
  Board-Resource zusammengefasst statt voll (Top-N / nur offene Initiativen).
- R-B: Halluzinierte Aktionen — gemildert durch L5 (Propose→Confirm); reine
  Lese-Antworten bleiben unbestätigt, dürfen aber nichts mutieren.
- R-C: Welches Modell? one-api gibt Claude und GLM-5.1 — Default GLM-5.1 (Cost ~0)
  für Lese/Triage, Claude für Aktions-Planung? Vor L2 entscheiden.
- R-D: Event-Tabellen-Wachstum durch Chat-Nachrichten — Retention/Kompaktierung
  für `ai_message`-Events (rolling), Aktions-Events permanent.

## Phasen (Granularität, keine Zeit)

1. **`master-kanban-mcp` (read-only Resources + 1 mutierendes Tool)** (Gran. 3) —
   Done wenn SC1 (Grounding) + SC5 (Auth) über das MCP erfüllt sind.
2. **Drawer-Chat + Chat-als-Events** (Gran. 3) — L2+L4.
   Done wenn SC2+SC3 als **konkreter E2E-Flow** demonstriert: User tippt in einer
   Test-Karte „verschieb das nach soon" → KI schlägt `move-stage(soon)` vor →
   Drawer-Klick „Ausführen" ruft `/api/move` → Karte steht auf `soon` → ein
   `ai_action`-Event erscheint in der Historie (plus die zwei Chat-Nachrichten
   als `ai_message`-Events).
3. **Board-Chat (Triage-Scope)** (Gran. 2) — L3.
   Done wenn SC4.
4. **Aktions-Sicherheit härten** (Gran. 2) — L5 vollständig inkl. Live-Geld-Regel.
   Done wenn SC6.

---

> Architektur-Hebel → Plan-Pipeline. Deep-Tech empfohlen (MCP-Aktionsfläche +
> Auth + Live-Geld-Aktions-Guards). Kein Bead vor Quick-Verdict.

## Reviewer-Verdict — quick (glm-5.1) — 2026-06-15

**Verdict:** `approved-with-notes`

Das PRD ist klar strukturiert, das Problem ist sauber vom Lösungs-Pitch getrennt, und die Phasen haben überprüfbare Done-Kriterien (gemappt an SCs). Alternativen werden explizit verworfen, Risiken ehrlich gelistet und Zeitschätzungen sind korrekt als Granularität maskiert. Verbesserungspotenzial liegt vor allem bei der Schärfung einzelner Done-Kriterien und der Reversibilität von Architekturentscheidungen.

**Findings:**
- [minor] **Phase-2-Done-Kriterium referenziert SC ohne klaren Test-Case** — SC2 fordert, dass die KI eine Aktion vorschlägt, diese nach Klick ausgeführt wird und als Event erscheint. Das Done-Kriterium von Phase 2 nennt SC2, aber es fehlt die Spezifikation, was der positive Beweis ist (z.B. konkreter End-to-End-Flow für mindestens eine Aktion wie move-stage).
- [minor] **Reversibilität der MCP-Entscheidung nicht adressiert** — Die Wahl, das Board als MCP zu exponieren, wird gut begründet und die Alternative verworfen. Es fehlt aber eine kurze Notiz zur Reversibilität: Was passiert, wenn sich das MCP-Muster nicht bewährt — kann der Drawer-Chat auf direkte Endpoint-Calls zurückfallen, oder ist das MCP dann bereits zu tief im System verankert?

**Asks:**
- [ ] Schärfe das Done-Kriterium für Phase 2 auf einen konkreten, demonstrierbaren Flow (welche Aktion, welche Karte, welcher Bestätigungsweg), sodass SC2 eindeutig verifizierbar ist.
- [ ] Füge bei L1 oder in den Risiken einen Satz zur Reversibilität des MCP-Musters hinzu — mindestens ein Gedanken, wie aufwändig ein Rückbau oder Fallback wäre.

## Reviewer-Verdict — deep-tech (spec-panel critique, focus: architecture/requirements) — 2026-06-15

- **Verdict:** `approved-with-notes`
- **Methode:** /sc:spec-panel critique inline (gt-plan-Wrapper scheitert als root). Panel: Fowler/Newman/Hohpe/Nygard (architecture), Wiegers/Adzic/Cockburn (requirements).
- **Gesamt:** Richtige Richtung (Reuse der Endpoints, Chat=Events, eine MCP-Fläche). Die Findings sind Vor-Implementation-Schärfungen; #1 ist eine echte Architektur-Lücke, die vor Phase 1 zu schließen ist.

**MUST-FIX vor Phase-1-Beads (3):**

1. **[Newman/Fowler — MAJOR] Die agentische Orchestrierungs-Komponente fehlt.** MCP liefert Tools + Resources, aber *wer* fährt die LLM↔Tool-Schleife (LLM-Call → Tool-Call ausführen → Antwort)? Der Browser kann es nicht (keine Keys client-seitig). Diese Komponente (im `serve`-Backend? eigener `agent-runner`?) ist das Herz des Features und ist nicht benannt. Dazu: das **Konversations-Memory** — der Agent muss die `ai_message`-Events pro Karte als Verlauf rücklesen; Thread-Isolation bei parallelen Tabs/Sessions spezifizieren (sonst interleaven zwei Gespräche auf derselben Karte).
2. **[Hohpe — MAJOR] `ai_*`-Events vs. bestehende Event-Consumer + Idempotenz.** Der solartown-adapter/Stage-Proposer konsumiert `initiative_event`s. `ai_message`/`ai_action` dürfen diese Consumer **nicht** triggern/verfälschen — Event-Namespacing + Consumer-Filter festlegen. Und: AI-Aktionen brauchen einen Idempotency-Key (doppelte `dispatch`-Bestätigung = zwei Workspaces; `dispatch` löst echtes Compute aus).
3. **[Nygard — MAJOR] Confirm-UI muss die konkrete Aktion + Params zeigen (Diff), nicht nur einen Button** — sonst Rubber-Stamping statt echtem Gate. Fehlgeschlagene AI-Aktionen (Endpoint 5xx) müssen ebenfalls Events werden (kein Silent-Fail), und LLM-Ausfall (one-api down/Timeout) muss im Drawer graceful degradieren.

**NOTES (vor finalem Greenlight):**

- **[Wiegers] Interaktivitäts-NFR fehlt** — kein Latenz-/Streaming-Ziel. Ein Chat mit 30s bis zum ersten Token ist unbenutzbar; Token-Streaming bzw. First-Token-Ziel spezifizieren. SC1-Grounding-Probe konkret benennen (faktische Frage mit prüfbarer Antwort, z.B. „wie viele Beads sind closed" gegen die echten Daten).
- **[Cockburn] Actor-Attribution der AI-Aktionen** — welcher `actor`-String steht im Event (`mcp-copilot`)? Für Stay-Awesome-Karten greift die Gaia-Persona-Konvention. Nötig für die „self-documenting"-Behauptung (man muss KI- von Mensch-Aktionen unterscheiden können).
- **[Adzic] Worked Example für den gefährlichen Pfad** — der propose→confirm-Beispielflow deckt nur `move-stage` (harmlos) ab; gerade `dispatch` auf eine Live-Karte (das riskante) braucht den durchgespielten Beispielflow inkl. der Live-Geld-Verweigerung (SC6).
- **[Fowler] „kein Extra-Bau" für den Board-Chat ist optimistisch** — der `board`-Scope (alle Initiativen) braucht eigene Kontext-Verdichtung (R-A), das ist nicht gratis aus L1.
