---
title: CRM-Produktion (Twenty) — Delivery-Report
slug: crm-twenty-produktion-delivery
status: in-progress
layer: delivery
parent_plan: docs/plans/crm-twenty-produktion-prd.md
created: 2026-07-14
---

# CRM-Produktion (Twenty) — Delivery-Report

**Stand 2026-07-14, Session-Inline-Bau (Session 2c0aad00…).** MVP-Ziele
G1-G10 erfüllt und auf der Zielbox verifiziert; zwei Folge-Punkte offen
(AP-Flow-Smoke, HubSpot-Import), W5 (Google-Login/Mail-Sync) als Folgepaket.

## Ziel-Abgleich

| ID | Kriterium | Status | Beweis |
|---|---|---|---|
| G1 | https://crm.stayawesome.app mit gültigem TLS | ✅ | `curl -I` → 200, LE-Cert (ohne `-k`) |
| G2 | DNS → 178.105.36.33, CF-proxied | ✅ | CF-PATCH `proxied:true` success (Record existierte aus 07-13-Anlauf) |
| G3 | Login-Wand, Self-Signup AUS | ✅ | `SIGN_UP_DISABLED=true` im Container verifiziert; anonym `/rest/*` → 403 |
| G4 | Drei Domänen abgebildet | ✅ | Objects `property`+`investor` (je 4-5 Felder), Opportunity `pipeline`-Select (FUNDRAISING/HOTEL_B2B), stage 15 Optionen gemergt; `/rest/properties` + `/rest/investors` → 200 |
| G5 | DB isoliert von sa-pg | ✅ | Container `crm-db` (postgres:16), Volume `crm_db16-data` |
| G6 | nginx-vhost Konvention + WebSocket | ✅ | `sites-enabled/crm.stayawesome.app`, Upgrade-Header |
| G7 | API agenten-bereit | ✅ | Key `gaia-agents` (Admin-Rolle, 10y): ohne 403 / mit 200; Schreibprobe Company create+read+delete |
| G8 | Secrets im Vault | ✅ | `crm.env` + `crm-admin.json` (Login, API-Key) auf werkstatt |
| G9 | Deploy reproduzierbar | ✅ | Recipe (v2.20.0 gepinnt) + `schema-setup.sh` (idempotent) im Repo, gepusht |
| G10 | Tägliches Backup | ✅ | `crm-backup.timer` enabled; Erst-Lauf: Dump 191 KB in `hetzner-nbg1:angelosystems-data/backups/crm/` |

**Workspace:** „Stay Awesome" (ACTIVE), Admin `gaia.ai@stayawesome.de`.
Invite-Link (public, für Mario/Team): `https://crm.stayawesome.app/invite/a4c0eb6c-bf37-4694-8fcb-b9fdb6c54267`

## Entscheide aus dem Lauf (Mario 2026-07-14)

- O1: Google-OAuth ja („könnten wir machen"), Präferenz eigentlich Authentik —
  Authentik-OIDC ist Enterprise-gated (Organization $19/User/Monat), daher
  Google-OAuth als freier Weg; GCP-Klick offen.
- O2: HubSpot-Übernahme JA; Zugriffsweg = HubSpot-Connector (Mario autorisiert
  in claude.ai) → Import-Folgepaket.
- O3: keine Team-Trennung — alle sehen alles (D4 bestätigt).
- Signup-Erstanlage: „Flip mit vhost-Pause" (ausgeführt, wieder verschlossen).

## Gotchas (fürs nächste Mal)

1. **Auth-/Core-GraphQL liegt auf `/metadata`**, nicht `/graphql`
   (signUp/signIn/createApiKey/getRoles); Introspection ist aus —
   Mutation-Namen via `dist/engine/core-modules/*/\*.resolver.js` greppen.
2. `signUpInNewWorkspace(input:{displayName})` — Arg heißt `input`;
   danach `activateWorkspace(data:{displayName})` mit Workspace-Token
   (`getLoginTokenFromCredentials` → `getAuthTokensFromLoginToken`).
3. **API-Key braucht `roleId`** (`createApiKey(input:{name,expiresAt,roleId})`,
   expiresAt als String); Rollen via `getRoles` (nur mit Workspace-Token).
4. Metadata-REST: Liste liegt direkt in `.data` (Array), nicht `.data.objects`.
5. **Bash: `${5:-{}}` schließt am ersten `}`** — hängt ein Extra-`}` an.
6. Alt-Anlauf 07-13 lief mit Spilo-DB-Image; Volumes `crm_db-data` +
   `crm_server-local-data` sind verwaiste Reste (waren leer verifiziert) —
   Aufräum-Klick offen. Neues DB-Volume heißt `crm_db16-data`.
7. psql `-c "a;b"` = eine Transaktion — Fehler in b rollt a zurück
   (event_claims-INSERT-Falle).

## Nachtrag 2026-07-14 (später am Tag): Authentik-Perimeter + Google-SSO

Mario-Anweisungen aus dem Review des Live-Stands: (a) Authentik MUSS vor die
UI, (b) kein Twenty-Passwort-Login — Google-SSO.

- **Authentik-ForwardAuth LIVE:** Provider `crm-forward-auth` (pk 21, Klon der
  listmonk-Config), Application `crm`, Policy-Binding Gruppe „authentik Admins"
  (Mario-Pick), Provider im Embedded Outpost. vhost umgebaut (Backup
  `crm.stayawesome.app.bak-pre-authentik`): UI-Location mit `auth_request`,
  **API-Pfade `/rest|/graphql|/metadata|/healthz|/client-config|/files`
  ausgenommen** (Bearer-Auth). Beweise: UI anonym → 302 auf
  `outpost.goauthentik.io/start`; `/rest` anonym 403 / mit Key 200; healthz 200.
- **Google-OAuth in Twenty aktiv** (`client-config.authProviders.google=true`),
  Creds = bestehender GCP-Client (google-oauth-client.json, Projekt 7557…).
  **Wartet auf Mario-GCP-Klick:** Redirect-URI
  `https://crm.stayawesome.app/auth/google/redirect` am Client ergänzen
  (für W5-Mail-Sync gleich `…/auth/google-apis/get-access-token` mit).
- **Passwort-Login-Abschaltung** (`AUTH_PASSWORD_ENABLED=false`) erst NACH
  verifiziertem Google-Login — sonst Aussperr-Risiko (gaia-Admin loggt via
  Google ein, API-Key bleibt unabhängig gültig).
- Invite-Link liegt jetzt HINTER dem Admins-Gate — Team-Onboarding erfordert
  erst Gruppen-Hebung im IdP.

## Nachtrag 2 (2026-07-14): Login final + Gotchas

- **Google-Login E2E bestätigt** (Mario, Inkognito): Authentik-Gate → Google →
  Workspace lädt. **Twenty-Passwort-Login abgeschaltet**
  (`AUTH_PASSWORD_ENABLED=false`; client-config `password:false, google:true`).
  API-Key bleibt unabhängig gültig (rest 200) — Admin-Automation läuft weiter
  über den Key (getRoles/Metadata/Invites via `/metadata` + Bearer, kein
  Passwort-Login mehr nötig).
- **Redirect-URIs** am OAuth-Client `755700364983-lku7…` registriert (Mario
  manuell): `…/auth/google/redirect` + `…/auth/google-apis/get-access-token`.
  Google akzeptiert (authorize 302).
- **Workspace-Mitgliedschaft:** Google-Login authentifiziert nur — der User
  muss Workspace-Mitglied sein, sonst „User does not have access to this
  workspace". Fix: `sendInvitations(emails:[…], roleId:<Admin>)` via
  `/metadata` + Bearer (Arg heißt `emails` direkt, NICHT `sendInviteLinkInput`).
  Mario (mario.gemuenden@stayawesome.de) als Admin eingeladen. Invite-Hash
  `a4c0eb6c-…`. ⚠️ gaias TWENTY-Passwort = crm-admin.json (NICHT das
  Google-Passwort aus gaia-credentials.json).
- **GOTCHA Blank-Workspace:** Nach mehreren Domain-Zustandswechseln hatte Marios
  normales Chrome-Profil einen stale Cache/Service-Worker → Hülle lädt, Daten
  nicht (alle API-Calls trotzdem 200). Server war sauber (kein CSP/WS/CF-Transform,
  Schema valide). Fix: Inkognito / „Cache leeren und hart neu laden" / Website-Daten
  löschen. Erste Diagnose-Anlaufstelle bei Twenty-Blank = Browser-Cache, nicht Server.

## Offene Punkte

- [ ] **AP-Flow-Smoke** (W4-Restpunkt): ActivePieces-Connection auf Twenty
  (API-Key liegt im Vault), erster Flow schreibt Testkontakt.
- [x] **HubSpot-Import** (O2): LIVE — siehe Nachtrag 3.
- [ ] **W5 Google-Login/Mail-Sync** (O1): GCP-OAuth-Client = Mario-Klick;
  Callback-URLs im PRD-W5.
- [ ] Alt-Volumes + alte Board-Karten (`sk-fundraising-crm-twenty`,
  Coolify-MENSCH-Karte) aufräumen/schließen — Mario-Klick.
- [ ] SMTP für Invite-/System-Mails (EMAIL_* Env) — optional, Invite-Link geht auch so.

## Nachtrag 3 (2026-07-14): HubSpot-Fundraising-Import LIVE

**Zugriffsweg (O2 gelöst):** HubSpot-MCP als **claude.ai-Connector** (nicht als
lokaler CLI-MCP). Grund: HubSpots gehosteter MCP (`mcp.hubspot.com`) hat
**keine Dynamic Client Registration** (`registration_endpoint:null`) und ist ein
Confidential Client — der lokale CLI-Weg braucht eine vorregistrierte
`client_id`+`client_secret` (MCP-Auth-App), der Private-App-Weg war für Mario
gesperrt. Der Connector nutzt Anthropics registrierten Client → nur „Zulassen"
in HubSpot. **Wichtig:** Connector muss im CLI-Konto `claude3@stayawesome.de`
angelegt sein, damit `mcp__claude_ai_HubSpot__*` in der CLI erscheinen.

**Scope:** zwei Fundraising-Pipelines (`39495489` Fundraising Series A +
`default` Fundraising Mario). Immobilien-/Firmensales-/Bürgschaft-Pipelines
bewusst NICHT (Mario-Pick). Firmensales gescopt = nur 1 Platzhalter-Deal.

**Importiert & verifiziert (crm.stayawesome.app):**
| Objekt | Menge | Mapping |
|---|---|---|
| Opportunities | 321 | pipeline=FUNDRAISING, stage gemappt (LOST/ERSTKONTAKT/PITCH/VERHANDLUNG), amount, closeDate, pointOfContact + company verlinkt |
| People (Investoren) | 221 | name/email/phone/jobTitle, companyId |
| Companies (Fonds) | 137 | name, domainName |
| Notizen | 291 | Twenty-Notes (bodyV2.markdown), an Opportunity + Person gehängt |

**Provenance-Tags (Mario-Ask 07-14):** neue Felder `quelle` (SELECT, Default-Wert
gesetzt =`HUBSPOT`, oranger Chip) + `hubspotId` (TEXT, Original-Objekt-ID) auf
person/company/opportunity — alle 679 Records getaggt. Damit: UI-Filter „alles
aus HubSpot" + idempotenter Re-Sync über die hubspotId.

**Gotchas (fürs nächste Mal):**
1. **Cloudflare blockt python-urllib** (HTTP 403, „error code 1010" = Browser-
   Signatur) — `User-Agent`-Header setzen (curl kam durch). Betrifft jeden
   nicht-Browser-Client gegen die CF-proxied Domain.
2. **Twenty Rate-Limit = 100 req / 60 s** → Throttle ~85/min (0,7 s Gap) +
   429-Retry mit Backoff. ~680 Creates + 291 Notes = mehrere Minuten.
3. **Twenty auto-erzeugt Companies aus Personen-E-Mail-Domains** — erzeugte 41
   „Unbenannte Firma [hash]" (Rauschen, gelöscht) + 22 echt-benannte Arbeitgeber
   (behalten). Companies danach 163.
4. **DE-Telefon-Lokalformate** (`0561 95379-600`) → `INVALID_PHONE_NUMBER`
   (libphonenumber) → Fallback: Person ohne `phones` anlegen (8 Fälle).
5. **noteTarget-Write** = Morph-Relation via `targetOpportunityId` /
   `targetPersonId` (NICHT `opportunityId`). Note-Body akzeptiert
   `bodyV2:{markdown:…}` (→ blocknote auto-konvertiert).
6. **query_crm_data**-Output = TSV in `.results[0].content`; groß → landet als
   File, per jq/Python parsen. `SELECT DEAL.x FROM NOTE …` liefert Cross-Object-
   Verknüpfung mit.

**Import-Artefakte** (Session 2c0aad00 scratchpad): `import_to_twenty.py`,
`import_notes.py`, `tag_and_cleanup.py` + Ledger `company_map/person_map/opp_map/
note_map.json` (hs_id→twenty_id, für Re-Sync).

**Noch offen:** Twenty-Demo-Seed (Notion/Stripe/Ivan Zhao — 5 Comp/5 People/6 Opp)
separat wegräumen.

## Nachtrag 4 (2026-07-14): Immobilien-Import + zwei Betriebs-Gotchas

**Immobilien-Erbe importiert** (W0 des Folge-PRDs `immo-akquise-automat` im
fin-Repo, quick approved-with-notes): 225 Deals aus 3 HubSpot-Pipelines →
Opportunities (IMMO_KAUF 3 / IMMO_INVESTOREN 23 / IMMO_DEALFLOW 199), 97 neue
People, 136 neue Companies — Dedup gegen den Fundraising-Import über die
gemeinsamen hs_id→twenty_id-Ledger (12 Kontakte, 1 Firma wiederverwendet).
Neues Feld `hubspotStage` (TEXT) auf Opportunity = verlustfreie Original-Stage;
Tags (`quelle`, `hubspotId`) diesmal direkt beim Anlegen. 14 Deal-Notizen
angehängt.

**GOTCHA 7 — SELECT-Options-PATCH:** Beim Mergen von Options via
`PATCH /rest/metadata/fields/<id>` MÜSSEN die bestehenden Options-Objekte ihre
`id`-Felder behalten. Ohne ids legt Twenty die Optionen neu an und **nullt die
Feldwerte aller Bestands-Records** (passiert mit `pipeline`: 321
Fundraising-Opportunities standen auf null; per Ledger repariert).
`schema-setup.sh` macht es richtig (`$cur + [neue]`, cur unangetastet) — nie
„aufräumen" und ids strippen.

**GOTCHA 8 — der `gaia-agents`-Key hat MEHRERE Schreiber:** Parallel zum
Import legte ein anderer Prozess (Local-B2B-Sales-Strecke, Welle A) ~56 People
+ Begleit-Companies über denselben API-Key an (`createdBy` ist damit NICHT
eindeutig einem Job zuzuordnen). Die 41 „Unbenannte Firma"-Löschungen aus
Nachtrag 3 stammten von DIESEM Prozess (Freemail-Domains), nicht vom
HubSpot-Import — Löschung folgenlos, aber: vor Massen-Löschungen immer gegen
die eigenen Ledger prüfen, nicht gegen `createdBy`.
