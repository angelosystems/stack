---
title: Mitarbeiter-Zugang — Claude-Code-Sessions im Browser hinter Google-SSO
slug: mitarbeiter-zugang
status: in-progress  # Mario-Go 2026-07-14 (O1-O3 = Empfehlungen); Session-inline-Bau, bewusst nicht approved (Decomposer scharf, kein Fabrik-Bau)
layer: prd
parent_plan: null  # bewusst: Dach-Suche 2026-07-14 — Vorgänger sk-tenant-workspaces ist done, Code-Factory-Vision ist Kontext (references), kein Karten-Dach; eigenständige library-Initiative
scope: Mitarbeiter bekommen eine Web-Surface für eigene Claude-Code-Sessions (Session-Liste, Resume, Modell-Auswahl) auf einer isolierten Box hinter Authentik/Google-SSO — plus Master-Kanban-Zugang, der hart auf die Stay-Awesome-Zeile begrenzt ist.
tier: library
software: mitarbeiter-zugang
created: 2026-07-14
review:
  quick: auto
  deep: spec-panel
  panel-mode: critique
  panel-focus: [architecture, compliance]
references:
  - /opt/docs/code-factory/20-vision.md
  - Vorgänger-Karte sk-tenant-workspaces (done; PRD-File war Phantom — dieses PRD ist der reale Nachfolger)
  - /opt/docs/konventionen/adr/0011-master-kanban-board-konvention.md
  - Memory reference_stayawesome_sso (Authentik + Google-Federation, Migrations-Rezept)
  - Memory project_tenant_workspaces_platform (Kern-Einsicht + Coder-Historie)
---

# Mitarbeiter-Zugang

## Kontext & Root Cause

Mitarbeiter (erster Nutzer: Angelo Calcagno, `dept-stayawesome`) sollen für
Stay Awesome Applikationen und Workflows mitbauen — konkret am Finanztool
(`stayawesomeOS/apps/fin`) und am Master Kanban — und zwar über eigene
Claude-Code-Sessions im Browser: Session-Liste, Sessions fortsetzen, parallele
Sessions, Modell-Auswahl pro Session (Default Fable). Vorbild ist die
Review-Management-Oberfläche: Cockpit-artig, hinter unserem SSO.

**Warum das heute nicht geht:**

1. **Alle bestehenden Session-Surfaces sind Mario-Single-User.** Vibe Kanban
   (werkstatt :54682) hängt auf der forward_domain-SSO-Schiene — die kennt
   keine App-getrennten Rechte, Binding ist „authentik Admins". Und werkstatt
   trägt QuantBot, Vault-Referenzen und Marios Secrets: Mitarbeiter-Shells
   dort sind ausgeschlossen.
2. **Kern-Einsicht aus dem Coder-Anlauf (2026-06, gilt weiter):** Eine
   Claude-Code-Session ist eine Shell — es gibt kein internes
   Permission-Modell. Der Isolations-Layer ist zwangsläufig
   Maschinen-/Container-Grenze plus gescopte Zugänge, nie eine
   Session-Einstellung. Der Coder-Weg selbst ist tot (Box umgewidmet,
   Neubewertung 2026-07-09: Karte sk-tenant-workspaces done).
3. **Master Kanban kennt Identität, aber keine Autorisierung nach Firma.**
   Das Backend liest `X-Auth-Request-Email` (Actor-Attribution, main.go),
   filtert aber nichts: Wer die App sieht, sieht alle Firmen-Zeilen
   (QuantBot, mario-brain, code-factory …). Der Vhost ist deshalb heute
   Admins-only — Mitarbeiter sind komplett ausgesperrt statt gescoped.

## Ziel

Ein Mitarbeiter meldet sich mit seinem Google-Workspace-Konto an, landet auf
seiner eigenen Session-Oberfläche, arbeitet mit Claude Code (Modellwahl,
Default Fable) an freigegebenen Repos — und sieht im Master Kanban
ausschließlich die Stay-Awesome-Welt. Von seiner Umgebung aus existiert kein
Pfad zu QuantBot, mario-brain, Vault oder werkstatt.

## OSS-Basis (Scan 2026-07-14, Adopt > Fork > Neubau)

Websuche über die Kandidatenlandschaft (Details als Rohdaten im
Vault-Kandidatenvergleich; Kern-URLs unten):

| Kandidat | Lizenz | Zustand | Befund |
|---|---|---|---|
| **siteboon/claudecodeui** | AGPL-3.0 ⚠ | sehr aktiv (v1.36.1, 2026-07-08) | **Pick.** Web-Cockpit mit Session-Liste/Resume/parallelen Sessions + Modell-Auswahl pro Session; Node nativ, kein Docker. Lücke: kein Multi-User — löst unsere Topologie (eine Instanz je Mitarbeiter, eigenes `$HOME`, Authentik davor) |
| Coder v2 | AGPL-3.0 ⚠ | aktiv | Beste Isolation + OIDC, aber kein Claude-Session-Cockpit (nur Terminal), schwergewichtig; bereits ein gescheiterter Anlauf im Haus |
| vibe-kanban (unser Fork) | Apache-2.0 | Upstream eingefroren (Bloop-Aus 2026-04) | bleibt Task-Orchestrator der Fabrik; ist kein Chat-Session-Cockpit (Follow-up = neuer Task-Attempt, kein Live-Session-Chat) |
| omnara | Apache-2.0 | archiviert 2026-02 | raus |
| slopus/happy | MIT | aktiv | Einzelentwickler-Handy-Client, falsche Form |
| opcode (ex Claudia) | AGPL-3.0 | ruhig | Desktop-App, kein Server |
| saadnvd1/agent-os | unklar ⚠ | aktiv | Fallback nach Lizenz-Klärung (Next.js-Multi-Session-Cockpit) |
| vultuk/claude-code-web | MIT | stale (2025-10) | konservativer MIT-Fallback, keine Modell-Auswahl je Session |
| Anthropic Claude Code Web | — | Beta | Anthropic-Cloud, nicht self-hosted |
| Anthropic apps gateway | — | verfügbar | keine Surface, aber self-hosted Policy/RBAC/Spend-Layer — Merkposten für Ausbaustufe |

**Entscheid:** Adopt `siteboon/claudecodeui`, unmodifiziert deployt (Konsum,
kein Fork). Eigenleistung ist reine Deployment-Topologie (systemd-Instanz je
Mitarbeiter + SSO-Routing) — keine App-Logik. Neubau auf dem Claude Agent SDK
wäre erst gerechtfertigt, wenn AGPL hart ausscheidet UND die Fallbacks
scheitern (O3). AGPL-Einordnung: rein interner Betrieb hinter SSO, keine
Distribution nach außen; solange wir nicht modifizieren, entsteht keine
Source-Offer-Pflicht — wird in D6 festgeschrieben.

Kern-URLs: github.com/siteboon/claudecodeui · github.com/saadnvd1/agent-os ·
github.com/vultuk/claude-code-web · code.claude.com/docs/en/claude-apps-gateway

## Architektur

```
Mitarbeiter-Browser
   │  Google-Login (Authentik-Federation, live)
   ▼
idp.stayawesome.app ── App "crew" ── Policy: Gruppe dept-stayawesome
   │  forward_single (Muster cap/finance, NICHT forward_domain)
   ▼
crew.stayawesome.app  (nginx auf der CREW-BOX)
   │  map $authentik_email → Upstream-Port des Mitarbeiters
   ├──► claudecodeui@angelo  (systemd-Template, User angelo, $HOME /home/angelo)
   ├──► claudecodeui@<ma2>   (weitere Mitarbeiter = 1 Zeile in der Port-Map)
   │
   ├── /home/<ma>/work/stayawesomeOS   (Clone via GitHub, eigener Account/Deploy-Key)
   ├── /home/<ma>/work/master-kanban   (Clone via GitHub)
   └── /home/<ma>/.claude              (eigenes Abo-Login, eigene Session-History)

master.stayawesome.app ── Policy erweitert um dept-stayawesome
   └── MK-Backend: Rolle "mitarbeiter" → nur firma=stayawesome (Lesen+Schreiben gescoped)
```

Tragende Entscheidungen:

- **Eigene Box** („crew", Arbeitsname): kein Mario-Secret, kein Vault-Mount,
  kein SSH-Key Richtung werkstatt/mario-prod/speicher — Isolation by
  construction statt by policy. Pro Mitarbeiter ein Linux-User; die
  claudecodeui-Instanz läuft als dieser User, sieht nur dessen `$HOME`.
- **Ein Surface-Prozess je Mitarbeiter** statt geteilter Instanz: löst die
  `~/.claude`-Single-User-Annahme des Tools, gibt saubere Session-Trennung
  und macht „wer sieht was" trivial (nginx-Map statt App-Logik, Poka-yoke).
- **Repo-Zugriff über GitHub, nicht über Box-zu-Box:** Mitarbeiter clonen
  `angelosystems/stayawesomeOS` + `angelosystems/master-kanban` mit eigenen
  GitHub-Zugängen (Collaborator, Write auf Feature-Branches;
  Branch-Protection auf main bleibt). Der Weg in Staging/Prod bleibt
  ausschließlich die Fabrik (Test-Gate → Merger → sa-staging-drain) — die
  Crew-Box hat keinerlei Deploy-Rechte.
- **Master-Kanban-Scoping im Backend, nicht in der UI:** neue Rolle
  `mitarbeiter` (Mapping E-Mail→Rolle+Firmen in einer kleinen Tabelle bzw.
  Config im master-kanban-Repo). GET-Endpunkte filtern auf
  `firma=stayawesome` (Zeile stayawesome + library-Karten mit Tag
  firma=stayawesome), Schreib-Endpunkte lehnen alles außerhalb ab. Der
  Vhost bekommt zusätzlich dept-stayawesome ins Policy-Binding —
  Defense-in-depth: SSO regelt „wer kommt rein", das Backend „wer sieht was".
- **Modell-Auswahl:** claudecodeui bietet Modellwahl je Session nativ; Fable
  ist Default. Authentifizierung gegen Anthropic je Mitarbeiter über ein
  eigenes Claude-Abo-Konto (Muster claude1/claude2/claude3 existiert samt
  Limit-Watcher `claude-abo-watch`) — geteilte Konten kollidieren beim
  Session-Limit (claude2-Erfahrung: Fabrik + Chat rissen gemeinsam das
  Limit). → O2.

## Work Packages

### W0 — Container-Fundament auf werkstatt (Granularität 3)
**O1 ENTSCHIEDEN (Mario, 2026-07-14): keine eigene Box — Mitarbeiter-Zugang
ist Teil der Code Factory und läuft auf werkstatt, Isolation per
systemd-nspawn-Container je Mitarbeiter** (nativ, kein Docker; erfüllt die
Kern-Einsicht „Maschinen-/Container-Grenze"). Pro MA ein Container
`crew-<name>` (Ubuntu-24.04-Rootfs auf dem 300-GB-Volume
`/mnt/werkstatt-data/crew/`), eigener Netz-Namespace (veth+NAT):
werkstatt-localhost (MK :7780 Header-Trust, PG :5434/:5435, VK, WA-Bridge)
und Host-Dateisystem sind von innen unerreichbar; nur der
claudecodeui-Port wird auf 127.0.0.1:41xx des Hosts geforwardet.
Im Container: User <name>, Node 22, claude-CLI, git. Inventur-Nachweis:
Container enthält keinerlei Host-Secrets/-Mounts.
Done: SC4-Inventur dokumentiert im Delivery-Abschnitt.

### W1 — SSO-Kette (Granularität 3)
DNS `crew.stayawesome.app` (CF-proxied) → nginx auf der Crew-Box →
Authentik-App `crew` mit forward_single gegen den prod-Outpost
(Muster cap/finance; ausdrücklich NICHT die werkstatt-forward_domain-Schiene),
Policy-Binding Gruppe `dept-stayawesome` (existiert, Angelo ist Mitglied).
nginx-Map `$authentik_email → 127.0.0.1:<port des MA>`; unbekannte E-Mail →
403. Additive IdP-Objekte nach dem abgesegneten Staging-Muster.
Done: SC1-Login-Beweis.

### W2 — Surface: claudecodeui je Mitarbeiter (Granularität 4)
claudecodeui als systemd-Template-Unit `claudecodeui@<user>` (native Node,
kein Docker; No-Docker-Konvention), Port aus der Map, WebSocket-Durchleitung
im Vhost. Interne Auth des Tools bleibt zusätzlich aktiv (Token im
User-Env) — zweite Schicht hinter dem SSO. Smoke: Session anlegen, schließen,
im Cockpit wiederfinden, fortsetzen; zwei parallele Sessions; Modellwahl
sichtbar. Done: SC1 + SC2.

### W3 — Anthropic-Zugang je Mitarbeiter (Granularität 2)
Je Mitarbeiter ein Claude-Abo-Konto (O2), Login via `claude setup-token` im
jeweiligen `$HOME` (Gotcha aus der Fabrik: Token-Ernte headless braucht
Terminal-Emulation, nie Regex auf Roh-Log). Konto in `claude-abo-watch`
aufnehmen — der Watcher ist multi-account-nativ (überwacht heute 4 Konten);
Aufwand je neuem Konto = ein setup-token-File im Vault + ein Eintrag, kein
Umbau. Bekannte Grenze: setup-Tokens können die usage-API nicht, der Watcher
misst über den 1-Token-Ping (gilt für MA-Konten genauso). Done: Session
unter dem MA-Konto beweist `billingType=subscription_included`.

### W4 — Master-Kanban-Firma-Scoping (Granularität 5, Repo /opt/master-kanban)
Rolle `mitarbeiter` + E-Mail-Mapping; Lese-Filter + Schreib-Wächter auf
`firma=stayawesome` in allen betroffenen Endpunkten (initiatives, move,
dispatch, events, MCP-Pfade); Admins unverändert. Tests nach dem Muster der
bestehenden auth_tests: Mitarbeiter-Mail sieht 0 Karten fremder Firmen,
Schreibversuch auf fremde Karte → 403. Vhost-Policy um dept-stayawesome
erweitern erst NACH grünem Backend-Test (Reihenfolge ist der Wächter).

**Rollback-Pfad (dreistufig, schnellste zuerst):**
1. Authentik-Policy-Binding dept-stayawesome von der App `master-kanban`
   entfernen → Zustand von heute (Admins-only) ist in Sekunden wieder da,
   ohne Deploy.
2. Scoping-Toggle: leeres E-Mail→Rolle-Mapping = niemand hat die Rolle
   `mitarbeiter`, alle Nicht-Admins bekommen 403 wie bisher — Verhalten für
   Admins ist in beiden Zuständen identisch (Regressionstest in SC3).
3. Git-Revert des W4-Commits als letzte Stufe (additiver Layer, kein
   Schema-Umbau → revert ist konfliktarm).

Done: SC3.

### W5 — Arbeitsfluss-Anschluss (Granularität 3)
GitHub-Zugänge (Collaborator auf stayawesomeOS + master-kanban,
Branch-Protection verifiziert), Kurz-Leitfaden für Mitarbeiter im
stayawesomeOS-Repo (Session starten → Branch → PR → Fabrik übernimmt),
Erst-Durchstich mit Angelo an einem echten fin-Thema. Done: SC2-Beweis
(PR aus einer Crew-Session, gemergt über das normale Gate).

## Entscheidungsbedarf — was vor welchem WP entschieden sein muss

| Punkt | Status | blockiert |
|---|---|---|
| O1 Host | **BLOCKER** (Server-Go nötig) | W0 und damit alles Weitere |
| O2 Anthropic-Zugang | **BLOCKER** (Abo-Anlage = Kosten) | W3 |
| O3 AGPL-Akzeptanz | PENDING (Empfehlung: intern akzeptieren) | W2 |
| O4 Erst-Zuschnitt | PENDING (Empfehlung unten) | W5 |

Erst wenn O1-O3 entschieden sind, wird `status:` auf approved gestellt —
der Decomposer zerlegt approved-PRDs automatisch in Beads.

## Entscheidungspunkte (Mario)

- **O1 Host:** (a) **neue Box `crew`** — sauber, empfohlen; braucht dein
  Server-Go. (b) staging-Box mitnutzen — spart Kosten, mischt aber
  Mitarbeiter-Shells mit sa-pg + Staging-Apps (Blast-Radius, spricht gegen
  Leitplanke 7 der Vision).
- **O2 Anthropic-Zugang:** (a) **eigenes Claude-Abo je Mitarbeiter** —
  empfohlen (Fable verfügbar, Limits getrennt, Muster existiert).
  (b) geteiltes Konto — kollidiert bei Limits. (c) Console-API mit
  Spend-Guard — kein Flat, nur als Ausweich.
- **O3 AGPL-Akzeptanz:** claudecodeui intern betreiben = empfohlen (keine
  Distribution, unmodifizierter Konsum). Falls AGPL hart raus: AgentOS nach
  Lizenz-Klärung, dann vultuk/claude-code-web, Eigenbau zuletzt.
- **O4 Erst-Zuschnitt:** Angelo als Erst-Nutzer; Repo-Freigabe genau
  stayawesomeOS + master-kanban. Weitere Mitarbeiter/Repos = je eine Zeile
  in Map + GitHub, kein Umbau.

## Success Criteria

- **SC1:** Angelo meldet sich unter crew.stayawesome.app mit Google an und
  sieht ausschließlich seine eigene Session-Liste; eine zweite Identität
  außerhalb dept-stayawesome bekommt 403 (Beweis: Header-/Curl-Probe).
- **SC2:** Aus einer Crew-Session entsteht ein Branch + PR an
  stayawesomeOS (fin), der über das normale Fabrik-Gate gemergt wird — ohne
  dass die Crew-Box Deploy- oder Prod-Zugänge besitzt.
- **SC3:** Master-Kanban-API liefert für eine dept-stayawesome-Mail
  ausschließlich stayawesome-Karten; Schreibversuch auf eine Karte anderer
  Firmen → 403; für Admin-Mails ändert sich nichts (Regressionstest).
- **SC4:** Isolations-Inventur der Crew-Box dokumentiert: keine
  Mario-Secrets, keine Keys Richtung werkstatt/mario-prod/speicher/vault,
  keine QuantBot-/mario-brain-Checkouts. Word-Hygiene grün, Commits lokal,
  Push nur mit Mario-Wort.
- **SC5:** Modellwahl je Session nachgewiesen (Fable als Default, ein
  Wechsel auf ein zweites Modell in einer echten Session).

## Rules

- **D1 Territoriums-Sperre:** QuantBot, mario-brain, Vault, werkstatt und
  speicher sind für die Crew-Box tabu — kein Key, kein Mount, kein Clone.
  Verstoß ist ein Abbruchkriterium, kein Finding.
- **D2 Kein neuer Server ohne Mario-Go:** W0 startet erst nach O1-Entscheid.
- **D3 IdP-Objekte additiv** nach dem abgesegneten Muster (Provider/App/
  Binding klonen, bestehende Apps unberührt); die forward_domain-Schiene der
  werkstatt-Apps wird nicht angefasst.
- **D4 Push-Disziplin:** Commits lokal; Push/Deploy nur mit Mario-Wort.
- **D5 Scoping im Backend:** Die Firma-Begrenzung im Master Kanban wird im
  Backend erzwungen; UI-Ausblenden allein gilt nicht als erfüllt.
- **D6 AGPL-Registereintrag:** claudecodeui wird als AGPL-Komponente im
  Sonderweg-Register (ADR-Reihe) dokumentiert: unmodifizierter interner
  Betrieb; jede künftige Modifikation löst eine bewusste
  Lizenz-Neubewertung aus.
- **D7 Kein Docker für eigene Betriebs-Teile** (No-Docker-Konvention);
  claudecodeui läuft nativ unter systemd.

## Non-Goals

- Kein Zugang zu anderen Master-Kanban-Zeilen (QuantBot, mariobrain,
  angeloos, code-factory) — auch nicht lesend.
- Kein Deploy-/Prod-Recht für Mitarbeiter; Staging/Prod bleibt Fabrik-Weg.
- Kein Secrets-Broker-Vollausbau (das E3-Erbe st-4ugcp bleibt eigener
  Entscheid; die Crew-Box braucht ihn nicht, weil dort keine geteilten
  Secrets liegen).
- Kein VS-Code-im-Browser (code-server) in dieser Stufe — claudecodeui
  deckt den Session-Fluss ab; ein Editor-Ausbau wäre eine eigene, kleine
  Folgestufe auf derselben Topologie.
- Keine Änderung an Vibe Kanban oder der Fabrik-Lane — Mitarbeiter-Sessions
  sind zusätzlich, die Fabrik bleibt wie sie ist.

## Limitations & Risiken

- **L1 AGPL:** bewusst akzeptiert für internen, unmodifizierten Betrieb
  (D6). Exit: AgentOS/vultuk/Eigenbau-Leiter in O3.
- **L2 Upstream-Abhängigkeit claudecodeui:** sehr aktives Solo-Ökosystem;
  wir konsumieren Releases, pinnen Versionen und smoken vor Update.
- **L3 Kosten:** eigene Box + je-MA-Abo sind laufende Ausgaben; bewusster
  Preis der Isolation (O1/O2 machen ihn explizit).
- **L4 MK-Scoping-Tiefe:** Der erste Wurf scoped auf Firma-Ebene; feinere
  Rechte (nur bestimmte Karten, nur Kommentieren) sind Ausbaustufe.
- **L5 Fable-Verfügbarkeit** hängt am Abo-Typ des MA-Kontos; wenn Fable dort
  nicht verfügbar ist, greift das Downgrade-Routing (Opus 4.8,
  Betriebsmodus-Block).

## Reviewer-Verdict — quick (glm-5.2) — 2026-07-14

**Verdict:** `approved-with-notes`

Strukturell sehr starkes PRD mit klarer Problembelegung, expliziter Scope-Abgrenzung (Non-Goals), begründeten Architektur-Entscheidungen und überprüfbaren Done-Kriterien je Arbeitspaket. Keine Zeitschätzungen, keine Konventions-Brüche. Ein paar gezielte Nachfragen zu Reversibilität und Offenheitsgrad der Risiken.

**Findings:**
- [minor] **Offene Fragen nicht explizit als Sektion** — Das PRD listet Entscheidungspunkte (O1–O4), aber einige davon sind noch unentschieden (O1 markiert als »braucht dein Server-Go«). Diese sind ehrlich im Text, aber nicht als eigene »Open Questions«-Sektion gebündelt — wer den Plan scannt, könnte sie übersehen.
- [minor] **Reversibilität der Master-Kanban-Änderung nicht adressiert** — W4 fügt dem Master-Kanban-Backend eine neue Rolle und Filter-Logik hinzu. Falls das Scoping unerwartete Nebeneffekte hat, ist der Rollback-Pfad (Feature-Flag? Config-Toggle? Git-Revert?) nicht beschrieben. Bei einem kritischen Tool wie dem MK-Bord sollte das explizit sein.
- [minor] **claude-abo-watch-Skalierung unklar** — W3 nimmt neue Konten in claude-abo-watch auf, aber es wird nicht beschrieben, ob das Watch-Tool mehrere Mitarbeiter-Konten parallel überwachen kann oder ob das pro-Konto-Konfiguration erfordert, die Aufwand oder Fehlerquellen erzeugt.

**Asks:**
- [x] Füge eine kurze »Open Questions«- oder »Entscheidungsbedarf«-Sektion hinzu, die O1 (Server-Go) und eventuell O2/O3 als_BLOCKER_ oder _PENDING_ markiert, damit klar ist, was vor Start von W0/W3 entschieden werden muss. *(eingearbeitet: Sektion „Entscheidungsbedarf" mit Blocker-Tabelle + Decomposer-Hinweis)*
- [x] Beschreibe den Rollback-Pfad für W4: Was passiert, wenn das MK-Scoping im Prod Probleme macht? Gibt es einen Toggle, oder ist es ein Revert des Commits? *(eingearbeitet: dreistufiger Rollback in W4 — Policy-Binding raus / leeres Rollen-Mapping / git revert)*
- [x] Kläre in W3, ob claude-abo-watch nativ mehrere Konten unterstützt oder ob pro Konto Konfigurationsaufwand entsteht — das beeinflusst die Skalierbarkeit des Ansatzes bei weiteren Mitarbeitern (O4). *(eingearbeitet: multi-account-nativ, 4 Konten heute; je Konto ein Vault-Token-File + Eintrag; usage-API-Grenze der setup-Tokens dokumentiert)*

## Delivery — Session-inline-Bau 2026-07-14

Gebaut in der laufenden Session (Mario-Go „setz es um", O1 revidiert:
werkstatt+Container statt eigene Box). Fable orchestriert, W4 via Opus-Subagent.

**W0 Container-Fundament (werkstatt, LIVE):**
- nspawn-Container `crew-angelo` (Ubuntu 24.04, Rootfs auf
  `/mnt/werkstatt-data/crew/machines/`), User `angelo`, Autostart enabled
  (`machinectl enable`).
- Netz-Zone `crew` (Bridge `vz-crew`, 10.230.0.0/24, Container .11,
  NAT via networkd). DNS über resolved (1.1.1.1/9.9.9.9).
- **Isolation (SC4) bewiesen** via `nft` Tabelle `crew_guard`
  (`/etc/nftables.d/crew-guard.nft`, reboot-fest über `crew-guard.service`):
  neu-initiierter Container→Host/Docker-Bridges/Tluster/Tailscale gesperrt,
  established (Port-Forward-Antwort) frei, Internet+HTTPS offen. Getestet:
  Host-Dienste :7780/:22/PG/:54682/:8765 aus dem Container ALLE zu,
  172.x/100.64 zu, github.com → 200.

**W2 Surface (LIVE im Container):**
- Node 22 + claude-CLI 2.1.209 + `siteboon/claudecodeui` (Commit 038d960),
  Production-Build, `claudecodeui.service` (User angelo, :3001) enabled+aktiv.
- Host-Forward `crew-forward-angelo.service` (socat, DynamicUser)
  127.0.0.1:4101 → 10.230.0.11:3001; Smoke `curl :4101` → 200.

**W1 SSO-Kette (Backend fertig, Vhost STAGED):**
- Authentik (idp, via API): Provider `crew-forward-auth` pk 19
  (forward_single, external_host crew.stayawesome.app — exakter Host
  überschreibt Domain-Catch-all pk 7), Application `crew`, Policy-Binding
  Gruppe `dept-stayawesome`, Provider 19 im Embedded-Outpost.
  Per-App-Matching verifiziert: `/start?rd=crew` → OAuth-Client der crew-App.
- nginx-Vhost `sites-available/crew.stayawesome.app` geschrieben
  (Domain-Forward-Auth-Muster wie vibekanban, Backend :4101, WebSocket),
  `nginx -t` grün. **Bewusst NICHT enabled** (Cert fehlt bis DNS).

**W4 Master-Kanban-Firma-Scoping (committet in /opt/master-kanban, NICHT deployt):**
- Zentraler Wächter `mitarbeiter_scope.go`; Env `MK_MITARBEITER_SCOPE`
  (`email=firma[,…]`, leer = Verhalten heute = Rollback-Stufe 2). Alle
  karten-mutierenden Endpunkte + `/api/create` gescoped; Lese-Filter +
  404/403. 69 Tests grün (`go test ./...`), inkl. Regression Admin
  unverändert. Commits a3752a6 + 0babce8 (lokal, ungepusht). Binary gebaut
  nach `/opt/stack/bin/master-kanban.new` (Scope-Feature verifiziert).

**Board:** Karte `cf-mitarbeiter-zugang` (idea), session-geclaimt.

### Offene Go-Live-Freigaben (nach außen wirkend / prod-Restart — bewusst gebündelt)

1. **DNS:** A-Record `crew.stayawesome.app` → 178.104.255.22 (proxied) —
   Classifier-gated. Danach Cert (DNS-01 oder webroot), dann Vhost
   `ln -s … sites-enabled/ && nginx -t && systemctl reload nginx`.
2. **MK-Deploy:** `mv /opt/stack/bin/master-kanban{.new,}` + Drop-in
   `Environment=MK_MITARBEITER_SCOPE=angelo.calcagno@stayawesome.de=stayawesome`
   an `master-kanban-serve.service` + Restart (Reihenfolge-Wächter: erst
   Backend grün, DANN Schritt 3). Deploy-Mirror /opt/stack ↔ Quelle
   /opt/master-kanban beachten; /opt/master-kanban-Push (2 Commits).
3. **Vhost-Policy schon gesetzt** (dept-stayawesome an App crew) — greift
   erst mit DNS+Cert. Master-Kanban-Vhost-Policy NICHT verändert (crew ist
   eigener Vhost; MK-Zugang für Mitarbeiter kommt über das Backend-Scoping,
   Policy-Erweiterung an master.stayawesome.app ist separater Schritt wenn
   Angelo auch das Board-UI sehen soll).
4. **W3 Anthropic-Abo für Angelo:** eigenes Claude-Konto anlegen, im
   Container `su - angelo -c 'claude setup-token'`, Konto in
   `claude-abo-watch` (multi-account-nativ). = Marios Handgriff (Kauf).
5. **/opt/stack-Push:** PRD-Commits (Mario-Wort).
