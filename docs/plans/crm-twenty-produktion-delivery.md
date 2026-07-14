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

## Offene Punkte

- [ ] **AP-Flow-Smoke** (W4-Restpunkt): ActivePieces-Connection auf Twenty
  (API-Key liegt im Vault), erster Flow schreibt Testkontakt.
- [ ] **HubSpot-Import** (O2): wartet auf Connector-Autorisierung durch Mario.
- [ ] **W5 Google-Login/Mail-Sync** (O1): GCP-OAuth-Client = Mario-Klick;
  Callback-URLs im PRD-W5.
- [ ] Alt-Volumes + alte Board-Karten (`sk-fundraising-crm-twenty`,
  Coolify-MENSCH-Karte) aufräumen/schließen — Mario-Klick.
- [ ] SMTP für Invite-/System-Mails (EMAIL_* Env) — optional, Invite-Link geht auch so.
