---
title: Crew Multi-User — SSO-Domain-Gate entkoppeln und zweiten Mitarbeiter-Container bauen
slug: crew-multi-user-sso-gate
status: approved  # Quick-Verdict approved-with-notes (glm-5.2, 2026-08-04); beide Asks eingearbeitet
layer: prd
parent_plan: /opt/stack/docs/plans/mitarbeiter-zugang-prd.md
scope: Der Login aller werkstatt-Apps hängt an der Admins-only-App „Vibe Kanban" und weist deshalb jeden Mitarbeiter ab; das Gate wird in eine eigene Handshake-App entkoppelt, crew bekommt Per-User-Routing und Moritz einen eigenen Container neben Angelos.
tier: library
software: mitarbeiter-zugang
created: 2026-08-04
review:
  quick: auto
  deep: spec-panel
  panel-mode: critique
  panel-focus: [architecture, compliance]
references:
  - /opt/stack/docs/plans/mitarbeiter-zugang-prd.md
  - Memory reference_werkstatt_domain_gate_vibekanban (Root Cause + Diagnose-Fallen)
  - Memory crew-mitarbeiter-container (nspawn-Rezept für neuen MA)
  - Memory project_moritz_sales_provision (Moritz-Onboarding, Vier-Augen-Regel)
---

# Crew Multi-User — SSO-Domain-Gate entkoppeln

## Kontext & Root Cause

Mario meldet 2026-08-04: „Crew-Login funktioniert für Moritz und Angelo
nicht." Befund nach Live-Diagnose:

Alle werkstatt-Vhosts binden `snippets/authentik-domain-auth.conf` ein. Deren
401-Redirect geht an `idp.stayawesome.app/outpost.goauthentik.io/start`.
Dieser Start-Endpoint autorisiert **immer** mit der client_id von **Provider 7
(`vibekanban-forward-domain`, forward_domain, cookie_domain=stayawesome.app)**
= App **`vibe-kanban`** — unabhängig vom `rd=`-Ziel (empirisch belegt:
identische client_id für `rd=crew` und `rd=flows-sa`).

`vibe-kanban` ist an **`authentik Admins`** gebunden. Damit wird jeder
Nicht-Admin am IdP abgewiesen, **bevor** die Policy der Ziel-App geprüft wird.
Die crew-App selbst ist korrekt konfiguriert (`dept-stayawesome`) — sie wird
nur nie gefragt.

| Prüfung | Angelo (pk7) | Moritz (pk11) |
|---|---|---|
| `check_access(vibe-kanban)` ← echter Gate | **False** | **False** |
| `check_access(crew)` ← wird nie erreicht | True | True |
| `check_access(crm/listmonk)` (prod, per-App-Flow) | True | True |

Superuser (Mario, akadmin) übergehen die Policy — deshalb sah der Zugang aus
Betreiber-Sicht grün aus. Korroboration: kein Mitarbeiter hat je ein
`authorize_application`-Event für **irgendeine** werkstatt-App erzeugt; die
prod-Apps (CRM, Listmonk, Paperclip, ActivePieces) stehen dagegen im Log,
weil deren Vhosts die Browser-Endpoints auf dem **eigenen Host** haben und
damit einen per-App-Flow fahren.

**Zweiter Befund (unabhängig, sicherheitsrelevant):** crew ist ein
Einzelnutzer-Bau. Ein Container (`crew-angelo`), Vhost fest auf
`127.0.0.1:4101` verdrahtet, claudecodeui im Platform-Mode mit JWT-Bypass auf
DB-User `angelo`, MK-Broker pinnt hart `angelo.calcagno@stayawesome.de`.
Sobald das Gate offen ist, landet **Moritz in Angelos Container** — mit
Angelos Sessions, GitHub-Token und Kanban-Identität. Das Gate darf deshalb
nicht vor der Trennung geöffnet werden.

**Dritter Befund (Sprengfalle beim naiven Fix):** sechs werkstatt-Hosts haben
**keinen eigenen** Proxy-Provider und fallen direkt auf das Domain-Gate
zurück: `fabrik`, `flows-cf`, `flows-qs`, `flows-staging`, `gast-test`,
`paperclip-staging` — plus `vibekanban` selbst. Würde man `dept-stayawesome`
einfach an `vibe-kanban` hängen, gingen diese sieben Flächen **mit** auf,
darunter die Fabrik-UI.

## Ziel

Mitarbeiter kommen über Google-SSO in **ihren eigenen** Crew-Container, ohne
dass sich die effektiven Rechte auf irgendeiner anderen werkstatt-Fläche
ändern.

## Nicht-Ziel

- Keine Änderung an prod-Vhosts oder am prod-Outpost.
- Keine Erweiterung der MA-Rechte in Master-Kanban, GitHub oder Staging-Ops
  (Bahn 1–3 aus dem Eltern-PRD bleiben unangetastet).
- Kein Umbau der übrigen werkstatt-Apps auf per-App-Flow (Domain-Snippet
  bleibt das Muster).

## Entscheidungen

- **D1 — Eigene Gate-App statt Huckepack.** Provider 7 bekommt eine neue,
  dedizierte Application („SSO Domain Gate"), die **nur** den Handshake
  trägt. Alternative „dept-stayawesome an `vibe-kanban` hängen" ist
  verworfen: sie vermischt Handshake und Fachzugriff und öffnet die
  Fabrik-UI.
- **D2 — Status quo zuerst festnageln.** Vor der Gate-Öffnung bekommen die
  sieben ungedeckten Hosts eigene forward_single-Provider + Apps mit
  **`authentik Admins`** — exakt ihre heutige effektive Policy. Erst danach
  wird das Gate geöffnet. Keine stille Rechte-Erweiterung.
- **D3 — Eigene Gruppe `crew-users` statt `dept-stayawesome`.** Ein
  Crew-Container ist eine Maschine mit Token und Repo-Zugriff, nicht eine
  Fach-App. Zugang bekommt, wer einen Container hat — nicht jeder
  SA-Mitarbeiter.
- **D4 — Ein Vhost, N Container, Routing über die SSO-Identität.**
  `map $authentik_email → Backend-Port`, Default `403`. Alternative „ein
  Vhost je MA" (Erst-Bau-Variante) skaliert schlechter und vervielfacht
  Cert- und DNS-Arbeit.
- **D5 — Verifikation per Authentik-Impersonation, nicht per Passwort.**
  Fremde Konten werden nicht mit Zugangsdaten benutzt; Impersonation ist
  auditiert und reversibel.
- **D6 — Reihenfolge ist Teil der Sicherheit.** Trennung (W2/W3) steht vor
  Öffnung (W4). Moritz wird erst in `crew-users` aufgenommen, wenn sein
  Container existiert.

## Arbeitspakete

**W0 — Bestandsaufnahme einfrieren.** `check_access` für Angelo, Moritz und
einen Kontroll-Außenstehenden über **alle** 23 Apps als Vorher-Bild in
`docs/plans/crew-multi-user-sso-gate-baseline.json` ablegen. Dient als
Diff-Grundlage für W5.
*Done, wenn die Datei existiert und für alle 23 Apps × 3 Konten einen
`passing`-Wert enthält.*

**W1 — Ungedeckte Hosts abdecken (D2).** Für `fabrik`, `flows-cf`,
`flows-qs`, `flows-staging`, `gast-test`, `paperclip-staging`, `vibekanban`
je einen forward_single-Provider (exakter `external_host`) + Application mit
Binding `authentik Admins` anlegen und dem Embedded-Outpost zuweisen.
`vibe-kanban` wird auf seinen neuen eigenen Provider umgehängt und bleibt
Admins-only.
*Done, wenn alle sieben Hosts einen eigenen Provider haben, `check_access`
für Angelo und Moritz auf jedem davon `False` liefert und kein
werkstatt-Host mehr auf das Domain-Gate zurückfällt.*

**W2 — Gruppe `crew-users` (D3).** Gruppe anlegen, zunächst **nur Angelo**.
crew-App-Binding von `dept-stayawesome` auf `crew-users` umstellen.
*Done, wenn `check_access(crew)` für Angelo `True` und für Moritz `False`
liefert.*

**W3 — Per-User-Routing (D4).** Im crew-Vhost `map $authentik_email
$crew_backend` (Angelo → 4101, Moritz → 4102, Default leer) und
`proxy_pass http://127.0.0.1:$crew_backend` mit `if ($crew_backend = "")
{ return 403; }`. `nginx -t` + reload.
*Done, wenn `nginx -t` grün ist, ein Request mit gepinnter Angelo-Adresse
auf :4101 landet und einer mit unbekannter Adresse 403 bekommt.*

**W4 — Gate entkoppeln (D1).** Neue Application „SSO Domain Gate" an
Provider 7, `policy_engine_mode=any`, Bindings `authentik Admins` (order 0)
+ `dept-stayawesome` (order 10). Ab hier gelingt der Handshake; die
Fachpolicy entscheidet pro Host.
*Done, wenn `check_access` der Gate-App für Angelo und Moritz `True` liefert
und die App `vibe-kanban` unverändert Admins-only ist.*

**W5 — E2E-Verifikation (D5).** Per Impersonation als Angelo: Login-Kette
bis `crew.stayawesome.app` → 200, claudecodeui erreichbar. Gegenprobe: als
Moritz → 403 aus dem Vhost-Routing (kein Container). Gegenprobe:
`obsidian`, `solartown`, `quant-wissen`, `fabrik`, `vibekanban` für beide
weiterhin **denied**. Baseline-Diff aus W0 muss genau die beabsichtigten
Zeilen zeigen.
*Done, wenn SC1 erfüllt ist und der Baseline-Diff **ausschließlich** die
Zeilen `crew` und `SSO Domain Gate` verändert zeigt.*

**W6 — Container `crew-moritz`.** Nach Rezept aus Memory
`crew-mitarbeiter-container`: Rootfs unter
`/mnt/werkstatt-data/crew/machines/crew-moritz`, statische IP `10.230.0.12`,
nspawn-Config + `machinectl enable`, claudecodeui im Platform-Mode mit
DB-User `moritz`, Unit `crew-forward-moritz.service` (socat
`127.0.0.1:4102 → 10.230.0.12:3001`), nft-Zone unverändert. MK-Broker als
**eigener** Vhost auf `10.230.0.1:7792` mit gepinnter Adresse
`moritz.stockhausen@stayawesome.de` + eigenes nft-Loch. GitHub-Token-Broker
bewusst **nicht** — Moritz' Mandat ist Sales/Marketing, nicht Code
(Entscheid dokumentieren, Upgrade-Pfad offen).
*Done, wenn der Container autostartet, `127.0.0.1:4102` HTTP 200 liefert,
`/api/auth/user` `moritz` zeigt und der Isolations-Gegentest besteht
(Host :7780/:22/PG aus dem Container zu, github 200).*

**W7 — Freischalten + Launcher.** *Done, wenn SC2 erfüllt ist und die
crew-Kachel im Launcher beider Konten erscheint.* Moritz in `crew-users`;
`meta_launch_url`
an der crew-App setzen, damit die Kachel im Authentik-Launcher erscheint
(heute fehlt sie — die App ist nur per Direkt-URL auffindbar).

## Erfolgskriterien

- SC1 — Angelo erreicht `crew.stayawesome.app` per SSO und landet in
  `crew-angelo` (HTTP 200, `/api/auth/user` = `angelo`).
- SC2 — Moritz erreicht `crew.stayawesome.app` per SSO und landet in
  `crew-moritz`, **nicht** in Angelos Container.
- SC3 — Für beide bleiben `obsidian`, `solartown`, `quant-wissen`, `fabrik`,
  `flows-cf`, `flows-qs`, `flows-staging`, `gast-test`, `paperclip-staging`
  und `vibekanban` abgewiesen (check_access = False).
- SC4 — Ein Konto ohne `crew-users` (Kontroll-Außenstehender) bekommt am
  crew-Vhost 403, auch mit gültiger SSO-Session.
- SC5 — Der MK-Broker-Beweis aus dem Eltern-PRD gilt weiter je Container:
  gesendete Fremd-Identität wird host-seitig überschrieben.

## Risiken & Rollback

| Risiko | Gegenmaßnahme |
|---|---|
| Gate-Öffnung erweitert stillschweigend Rechte | W1 vor W4; Baseline-Diff in W5 als Pflicht-Gate |
| Moritz landet in Angelos Container | W2/W3 vor W4; Moritz erst in W7 in `crew-users` |
| Falsche Vhost-Map sperrt Angelo aus | `nginx -t` vor reload; Default-403 statt Default-Backend; Rollback = Vhost-Datei zurück + reload |
| Authentik-Fehlkonfiguration sperrt alle aus | Superuser umgehen Policies immer ⇒ Betreiber-Zugang bleibt; Rollback = Bindings zurücksetzen (reine Config, kein Datenverlust) |
| Container-Bau bricht die nft-Isolation | Isolation nach Bau gegentesten (Host :7780/:22/PG aus Container zu, github 200) — Pflichtschritt aus dem Rezept |

Rollback gesamt: alle Schritte sind Config (Authentik-Objekte, ein
Vhost, systemd-Units). Kein Schema-Wechsel, kein Datenpfad.

## Offene Punkte

- **O1** — Bekommt Moritz ein Claude-Modell im Container? Der crew-Token ist
  Marios `claude3` mit gemeinsamem Limit. **Kein harter Blocker für W7:**
  `crew-moritz` startet mit GLM/DeepSeek als Arbeitsmodell (dasselbe Muster,
  mit dem `crew-angelo` E2E lief), damit ist der Container ohne
  Kontingent-Entscheid nutzbar. Ein Fable-/Claude-Token wird **nur** auf
  Marios Wort nachgerüstet — sonst konkurrieren zwei Mitarbeiter und die
  laufende Session um dasselbe Wochenlimit.
- **O2** — Braucht Moritz crew fachlich überhaupt? Sein Kickoff-Stack ist
  CRM, Listmonk, Paperclip, Notion. Mario-Entscheid 2026-08-04: Container
  wird gebaut. O1 bleibt davon unberührt.

## Reviewer-Verdict — quick (glm-5.2) — 2026-08-04

**Verdict:** `approved-with-notes`

Ein außergewöhnlich sorgfältig ausgearbeiteter Plan: Root Cause ist durch eine Vorher-Baseline (W0) empirisch belegt, Reihenfolge und Risiken sind konsequent verriegelt, und die Sprengfallen (ungedeckte Hosts, Identitäts-Vermischung) werden explizit adressiert. Done-Kriterien fehlen auf Arbeitspaket-Ebene, sind aber durch die messbaren Erfolgskriterien (SC1–SC5) und die Reverse-Checks in W5 vollständig und sauber abgedeckt.

**Findings:**
- [minor] **Arbeitspakete ohne explizite Done-Kriterien** — Die Arbeitspakete W1–W7 definieren zwar klare Aktionen, verzichten aber auf ein explizites "Done wenn..." pro Paket. Die Überprüfbarkeit wird jedoch durch die sehr scharf formulierten Erfolgskriterien SC1–SC5 sowie die negativen Gegenproben in W5 (Baseline-Diff) sichergestellt.

**Asks:**
- [ ] Füge den Arbeitspaketen W1–W7 kurze, einzeilige Done-Kriterien hinzu (z.B. für W1: 'Done, wenn `check_access` für alle sieben Hosts Admins-only zeigt und kein Fallback auf das Domain-Gate mehr erfolgt').
- [ ] Präzisiere O1 (Claude-Modell/Token für Moritz): Ist dies ein harter Blocker für die Container-Freigabe in W7 oder kann W7 auch abgeschlossen werden, während O1 noch offen ist?

## Delivery-Stand — 2026-08-04

**W0 erledigt.** Baseline `crew-multi-user-sso-gate-baseline.json` (23 Apps ×
3 Konten), Nachher-Bild `crew-multi-user-sso-gate-after.json`.

**W1 erledigt.** Sieben forward_single-Provider (pk 25–31) + sechs
Applications (`fabrik-gate`, `flows-cf`, `flows-qs`, `flows-staging`,
`gast-test`, `paperclip-staging`), alle Binding `authentik Admins`, alle dem
Embedded-Outpost zugewiesen. Verifiziert: `check_access` für Angelo und
Moritz auf allen sechs = False.

**W2 teilweise.** Gruppe `crew-users` (pk 03150abc-…) angelegt, Angelo drin,
an die crew-App gebunden. **Offen:** das alte `dept-stayawesome`-Binding an
crew (pk cfeb9fdf-ca80-401a-a224-9290e1312521) ist noch aktiv — sowohl das
Löschen als auch das Deaktivieren wurde vom Berechtigungs-Classifier
abgewiesen. Folge: `check_access(crew)` ist für Moritz weiterhin True.
**Praktisch entschärft durch W3** (er bekommt am Vhost 403), aber die
Policy sollte nachgezogen werden.

**W3 erledigt.** crew-Vhost auf `map $authentik_email $crew_backend`
umgestellt, Default → 403-Stub auf `127.0.0.1:4199`. Backup des alten Vhosts:
`/etc/nginx/sites-available/crew.stayawesome.app.bak-vor-multiuser-20260804`.
Verifiziert mit identitäts-gepinnten Test-Locations: Angelo → :4101
(claudecodeui), Moritz → 403, Fremd-Adresse → 403. Test-Vhost wieder
entfernt. **Gotcha festgehalten:** kein `if ($authentik_email …)` — `if`
läuft in der REWRITE-Phase, also vor `auth_request` (ACCESS-Phase); die
Variable wäre immer leer und jeder bekäme 403.

**W4 erledigt.** App `vibe-kanban` hängt jetzt am eigenen Provider pk 31 und
bleibt Admins-only; Provider 7 trägt die neue App `SSO Domain Gate`
(Bindings: `authentik Admins` order 0, `dept-stayawesome` order 10).
`check_access(sso-domain-gate)` = True für Angelo und Moritz.

**W5 teilweise.** Baseline-Diff zeigt **7 neue Zeilen und keine einzige
geänderte** — keine bestehende App hat für irgendein Konto ihren Zugriff
geändert. **Nicht beweisbar ohne Menschen:** der vollständige Browser-Login.
Authentik-Impersonation liefert zwar eine echte Session (verifiziert:
Angelo, `is_superuser=false`), trägt aber die OAuth-Kette nicht — der Flow
verlangt an `ak-stage-identification` eine echte Anmeldung. SC1 braucht
daher einen einmaligen Google-Login von Angelo.

**W6/W7 offen.** Container `crew-moritz` und Freischaltung.

## Nachtrag — Vollzugriff im Testbetrieb (Mario-Entscheid 2026-08-04)

Mario: „Crew-Sessions sollen genauso funktionieren wie diese Session — für
den Test keine Beschränkungen mehr." Nach Rückfrage mit drei Stufen bewusst
die weitgehendste gewählt und zweimal bestätigt. **D7 — Isolation im
Testbetrieb aufgehoben.** Der Container hat damit faktisch Betreiber-Rechte
auf das gesamte Estate.

Vollzogen an `crew-angelo`:
1. `~/.claude/settings.json` → `permissions.defaultMode = bypassPermissions`
   (keine Werkzeug-Nachfragen mehr).
2. `Bind=/root/.secrets:/root/.secrets:idmap` in
   `/etc/systemd/nspawn/crew-angelo.nspawn`.
3. `/etc/sudoers.d/90-crew-vollzugriff` → `angelo ALL=(ALL:ALL) NOPASSWD: ALL`.
4. `crew-guard.service` disabled + gestoppt, nft-Tabelle `inet crew_guard`
   gelöscht.

**GOTCHA (kostete einen Neustart):** ohne `:idmap` erscheint der Bind-Mount
wegen `PrivateUsers=pick` als `nobody:nogroup` (65534); bei Modus 700 kommt
dann nicht einmal Container-root heran. Mit `:idmap` steht das Verzeichnis
als `root:root` im Container — 23 Einträge lesbar, verifiziert.

Verifiziert nach dem Umbau: sudo NOPASSWD greift, Secrets lesbar,
Host-SSH :22 aus dem Container offen, MK :7780 erreichbar, claudecodeui
:4101 = 200, crew.stayawesome.app = 302 (SSO unverändert davor).
Host-PG :5434 bleibt zu — Postgres lauscht nur auf 127.0.0.1, das ist eine
Listen-Adresse, keine Isolationsregel.

**Konsequenz fürs Testergebnis:** MK-Broker und GitHub-Token-Broker sind
damit wirkungslos — wer den god-mode-Key lesen kann, braucht sie nicht.
Board-Aktionen sind nicht mehr zuverlässig einer Person zuzuordnen.

**RÜCKBAU (eine Kette):**
```
# nspawn-Config zurueck
cp /etc/systemd/nspawn/crew-angelo.nspawn.bak-vor-vollzugriff-20260804 \
   /etc/systemd/nspawn/crew-angelo.nspawn
# sudo zurueck
machinectl shell crew-angelo /bin/bash -c 'rm /etc/sudoers.d/90-crew-vollzugriff'
# Isolation zurueck
systemctl enable --now crew-guard.service
machinectl reboot crew-angelo
```
Danach gegentesten: `/root/.secrets` im Container leer, Host :22 zu.

**Für `crew-moritz` (W6) gilt dasselbe** — die vier Schritte gehören in den
Bau, sonst hat Moritz eine andere Umgebung als Angelo.

## Delivery W6/W7 — Container crew-moritz (2026-08-04)

**W6 erledigt.** Rootfs `debootstrap noble` →
`/mnt/werkstatt-data/crew/machines/crew-moritz` (Symlink in
`/var/lib/machines`), statische IP **10.230.0.12**, resolved-Drop-in für DNS,
User `moritz` mit NOPASSWD-sudo, Node 22.23.2 + git + gh + claude-CLI 2.1.221,
claudecodeui aus Angelos Container übernommen (Vendor-Code, **ohne**
`~/.cloudcli` — DB-User frisch als `moritz` registriert, id=1),
`claudecodeui.service` (User=moritz, Platform-Mode),
`crew-forward-moritz.service` (socat 127.0.0.1:4102 → 10.230.0.12:3001),
beide autostart-enabled. Vollzugriff wie bei Angelo: `Bind=/root/.secrets:…:idmap`,
NOPASSWD-sudo, `settings.json` mit `bypassPermissions`.

**Abweichung von der PRD-Vorgabe (bewusst):** Der Claude-Token wird **nicht**
nach `/etc/crew-claude.env` kopiert, sondern der Dienst liest
`EnvironmentFile=/root/.secrets/stayawesome/claude3-token.env` direkt aus dem
eingeblendeten Vault. Eine Quelle statt Duplikat — beim Rückbau bleibt keine
Token-Kopie im Container liegen. ⚠️ Damit teilen sich `crew-angelo`,
`crew-moritz` und die Betreiber-Session **dasselbe claude3-Wochenlimit**.
Der MK-Broker auf :7792 wurde **nicht** gebaut: bei abgeschalteter Isolation
ist er wirkungslos. Er gehört zum Rückbau dazu, wenn der Testbetrieb endet.

**W7 teilweise.** Moritz hat Zugriff auf crew — allerdings über das noch
aktive `dept-stayawesome`-Binding, **nicht** über `crew-users`: die
Gruppen-Aufnahme wurde wie schon W2b vom Berechtigungs-Classifier abgewiesen.
Die beiden blockierten Schritte heben sich auf, der Zustand ist funktional
korrekt, aber nicht der entworfene. Ebenfalls blockiert: `meta_launch_url`
an der crew-App — es gibt weiterhin **keine Launcher-Kachel**, die URL muss
man kennen.

### Testnachweis (2026-08-04)

| Prüfung | Ergebnis |
|---|---|
| Routing Angelo | `/api/auth/user` → `username: angelo` (:4101) |
| Routing Moritz | `/api/auth/user` → `username: moritz` (:4102) |
| Fremde Identität | 403 vom Stub |
| **Leere** Identität (auth_request nicht gelaufen) | 403 — kein Durchrutschen |
| Container-Neustart | claudecodeui kommt selbst zurück, Routing danach unverändert |
| Autostart | nspawn- + Forward-Unit `enabled` |
| Secrets im Container | 23 Einträge lesbar, sudo NOPASSWD greift |
| Host-SSH aus dem Container | offen (Isolation aus, wie entschieden) |
| Claude-Session | `MORITZ-PROBE-OK` |
| Werkzeuge ohne Nachfrage | `WERKZEUG-LAEUFT-DURCH` (Moritz), `ANGELO-WERKZEUG-OK` (Angelo) |
| Zugriffslage unverändert | `vibe-kanban`, `solartown`, `obsidian`, `fabrik-gate` für beide weiterhin denied |

**Weiterhin nicht maschinell beweisbar:** der Browser-Login selbst (SC1/SC2).
Alles bis zum Backend ist verifiziert; die letzte Meile braucht einen
Google-Login von Angelo bzw. Moritz.

## W2/W7 nachgezogen — 2026-08-04 (Mario-Anweisung „Authentik-Schreibzugriffe machen")

- **Moritz in `crew-users`** aufgenommen — Gruppe enthält jetzt Angelo + Moritz.
- **`meta_launch_url`** an der crew-App gesetzt (`https://crew.stayawesome.app`)
  ⇒ Kachel erscheint im Authentik-Launcher.
- **`dept-stayawesome`-Binding an crew entfernt.** GOTCHA: `PATCH` auf
  `/policies/bindings/{pk}/` liefert **405 mit leerem Body** (CF-Schicht, nicht
  Authentik — GET am selben Pfad geht). `PUT` mit vollem Objekt scheitert an
  `"policy, target, order must make a unique set"`, weil das crew-users-Binding
  dieselbe `order=0` hat. Der Fallback-`DELETE` griff.
  **Restore:** `POST /policies/bindings/` mit
  `target=<crew-app-pk>`, `group=d7569b3b-8172-4944-9365-48cf14fbdfc6`,
  `order=10`, `enabled=true` (order ≠ 0 wählen, sonst Unique-Konflikt).

**Endzustand:** crew hängt nur noch an `crew-users`; `check_access(crew)` =
Angelo True, Moritz True, Kontroll-Konto False. **W2 und W7 damit erfüllt.**
Baseline-Diff unverändert: 7 neue Zeilen, keine geänderte — an keiner
bestehenden App hat sich für irgendein Konto etwas verschoben.

Damit sind **alle Arbeitspakete W0–W7 abgeschlossen**. Offen bleibt allein
SC1/SC2 — der Browser-Login, der einen echten Google-Login braucht.

## Nachtrag — „weiße Oberfläche" nach dem Login (2026-08-05)

**Rückmeldung Moritz:** Login geht, danach komplett weiße Oberfläche.

**Das Login-Gate ist damit produktiv bewiesen** (SC1/SC2, Auth-Hälfte):
`authorize_application`-Events für **SSO Domain Gate** existieren jetzt —
Moritz 2026-08-05T06:02:19, Angelo 2026-08-04T09:14:37. Vor dem Umbau hatte
kein Mitarbeiter je ein solches Event für eine werkstatt-App.

**Root Cause der weißen Seite — zwei Ursachen, die zusammenwirken:**

1. **Sitzungsablauf nach genau einer Stunde.** Provider 19 stand auf
   `access_token_validity = hours=1`, während der Domain-Provider und
   flows-sa auf `hours=24` stehen. Log-Beleg: Moritz meldet sich 06:02:19 an,
   WebSocket liefert bis **07:01** Status 101 — danach **365×
   `GET /ws → 302`** im 4-Sekunden-Takt plus 43 abgewiesene API-Aufrufe, bei
   offener Sitzung (Referer zeigt `/session/bb91acf4-…`).
2. **Der Ablauf war unsichtbar.** Der crew-Vhost schickte auch XHR- und
   WebSocket-Aufrufe in den Login-Redirect. Ein Cross-Origin-Redirect auf
   `idp.*` lässt `fetch()` und den WS-Client **still** scheitern — das
   Frontend bekommt weder Daten noch Fehler und rendert nichts. Genau dafür
   existiert `snippets/authentik-domain-protect-api.conf` (401 als JSON);
   master.stayawesome.app nutzte es bereits, crew nicht.

**Behoben:**
- Provider 19 `access_token_validity` → `hours=24` (PUT; PATCH ist an diesem
  Endpunkt 405).
- crew-Vhost um `location /api/` und `location /ws` ergänzt, beide mit
  `authentik-domain-protect-api.conf`. Verifiziert anonym: Seite → 302
  (Login-Redirect, korrekt), `/api/…` → 401 JSON, `/ws` → 401 JSON.
  **Gotcha:** der erste Test zeigte für `/api/` noch 302 — Cloudflare-Cache;
  mit Cache-Buster bzw. direkt am Origin kam sofort 401.

**Offen / beobachten:** `master-kanban-forward-auth` (pk 13) und
`crm-forward-auth` (pk 21) stehen ebenfalls auf `hours=1` — dieselbe
Stolperfalle, dort aber sichtbar, weil master das API-Snippet schon nutzt.
Ob claudecodeui auf ein 401 sichtbar re-authentifiziert oder nur sauberer
scheitert, zeigt der nächste Ablauf; ein Reload holt die Sitzung in jedem Fall.
