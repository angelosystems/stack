---
title: Fundraising-CRM (Twenty) auf deploy-Box
slug: fundraising-crm-twenty
status: abandoned
layer: prd
parent_plan: null
scope: Self-hosted OSS-CRM (Twenty) auf der deploy-Box (178.105.36.33) als sauberes Tracking-System für die Stay-Awesome-Fundraising-Runde — Investoren-Pipeline, Kontakte, Aktivitäten, Tickets — hinter TLS + App-Login, registriert im SA-Cockpit.
created: 2026-06-23
review:
  quick: auto
  deep: none
  panel-mode: critique
  panel-focus: [architecture, compliance]
references:
  - /root/stayawesome/manifests/external/documenso.json
  - /opt/docs/konventionen/public-endpoints.md
  - /opt/docs/konventionen/word-hygiene.md
  - /root/.secrets/credentials.md
---

# Fundraising-CRM (Twenty) auf deploy-Box

## Kontext

**Anlass:** Stay Awesome geht in eine Fundraising-Runde. Investoren, Business
Angels und Bankkontakte müssen sauber durch eine Pipeline getrackt werden
(Lead → Erstkontakt → Pitch → Due Diligence → Term Sheet → Closed), mit
Kontakten, Firmen, Aktivitäten/Notizen und einem Ticket-Betrag je Deal. Heute
existiert dafür kein dediziertes System.

**Tool-Entscheidung:** [Twenty](https://twenty.com) — moderner OSS-CRM
(TypeScript/React/NestJS, AGPL), Self-Host via Docker Compose. Bringt das
CRM-Datenmodell nativ mit: People, Companies, **Opportunities mit
Pipeline-Stages + Kanban**, Activities, Custom Fields. Die Fundraising-Runde
wird als eigene Opportunity-Pipeline modelliert. Alternativen (Baserow/NocoDB
als Tabellen-DB, EspoCRM/SuiteCRM/Odoo als PHP/ERP) wurden im Chat verworfen:
Twenty trifft das CRM-Modell out-of-the-box ohne Eigenbau und ohne ERP-Ballast,
und passt auf den TS-Stack.

**Deploy-Ziel:** Box `deploy` (hcloud ID 142707446, 178.105.36.33, NBG1,
Ubuntu 24.04). Gewählt nach Kapazitäts-Inventur am 2026-06-23:

| Box | RAM frei | Läuft schon | Verdikt |
|---|---|---|---|
| speicher | 248 MB (Swap 100% voll) | QuantBot/Polecat node-Schwarm | ❌ dicht, OOM-Risiko |
| werkbank | 29 GB | coder-postgres | ✅ Luft, aber Tenant/Coder-Domäne (fremd) |
| **deploy** | **14 GB** | sa-db (pgvector/pg16) | ✅ SA-Deploy-Target, SA-Kontext |

deploy ist der natürliche Ort: explizit der Stay-Awesome-Deploy-Target, trägt
bereits `sa-db`, hat system-nginx 1.24 (Konvention), Docker 29 + Compose v2.40,
14 GB freien RAM.

**Konventions-Rahmen:**
- Public-Endpoint-Auth ist Pflicht (kein vhost mit Systemzugang ohne
  App-Login/SSO) — siehe `/opt/docs/konventionen/public-endpoints.md`.
- system-nginx + certbot-webroot + sites-available/-enabled (kein Caddy).
- DNS via `cf-dns` (Stay-Awesome-CF-Zonen).
- Betrieb autonom als „Gaia AI".

## Ziele (messbar)

| ID | Success-Kriterium | Verifikation |
|---|---|---|
| G1 | Twenty erreichbar unter `https://crm.stayawesome.app` mit gültigem TLS | `curl -I` → 200/302 + Cert-Check |
| G2 | DNS `crm.stayawesome.app` → deploy-IP (178.105.36.33), proxied via CF | `dig +short crm.stayawesome.app` |
| G3 | Kein offener Zugang: Login-Wand vor jeder Funktion (App-Login, später Authentik-OIDC) | anonymer `curl` auf geschützte Route → Redirect zu Login |
| G4 | Fundraising-Pipeline existiert mit Stages Lead→Erstkontakt→Pitch→DD→Term Sheet→Closed | sichtbar in Twenty-UI, Kanban-Board |
| G5 | Twenty-DB isoliert von sa-db (eigene Postgres-Instanz, keine Schema-Kollision) | `docker ps` zeigt dedizierten twenty-postgres-Container |
| G6 | nginx-vhost folgt system-nginx-Konvention | `ls /etc/nginx/sites-enabled/crm*` auf deploy |
| G7 | App im SA-Cockpit registriert | `manifests/external/crm.json` vorhanden + Cockpit-Tile sichtbar |
| G8 | Admin-Credentials + APP_SECRET in Vault | Eintrag in `/root/.secrets/credentials.md` |
| G9 | Deploy reproduzierbar (compose + env-template getrackt, nicht nur auf der Box) | Artefakte im Repo |

## Nicht-Ziele

- Keine Datenmigration aus einem Alt-CRM (es gibt keins).
- Keine E-Mail-/Kalender-Sync-Integration in dieser Phase (Twenty kann das via
  IMAP/Google später — separates Folgepaket, Marker `crm-mail-sync`).
- Kein Multi-Tenant/öffentliche Registrierung — geschlossene Nutzergruppe (Mario
  + ggf. Co-Founder/Berater).
- Keine eigene Twenty-Fork/Custom-Build — Upstream-Image, Config-only.

## Architektur

```
LTR:  Browser → Cloudflare (proxied, TLS) → deploy:443 nginx
      → 127.0.0.1:3000 twenty-server (Docker) → twenty-postgres + twenty-redis
```

- **Container** (eigene compose, isoliert von sa-db):
  `twenty-server`, `twenty-worker`, `twenty-postgres`, `twenty-redis`.
- **DB-Isolation (G5):** Twenty braucht spezifische Postgres-Extensions. Die
  offizielle Twenty-compose liefert ein eigenes Postgres-Image mit diesen
  Extensions. Wir fahren **dedizierte** twenty-postgres + twenty-redis Container
  neben sa-db statt sa-db zu teilen — verhindert Extension-/Schema-Kollision und
  hält die SA-Stamm-DB unangetastet. Exakte Extension-/Image-Anforderung beim
  Implement aus der Upstream-Doku ziehen (Upstream-first, nicht raten).
- **Reverse-Proxy:** system-nginx vhost `crm.stayawesome.app` → `proxy_pass
  http://127.0.0.1:3000`. TLS via certbot-webroot. Twenty bindet nur auf
  localhost.
- **Storage:** `STORAGE_TYPE=local` mit Docker-Volume (Anhänge klein in dieser
  Phase); S3 (hetzner-nbg1) als Folge-Option.
- **Auth (G3):** MVP = Twenty-eigener App-Login (E-Mail+Passwort,
  Self-Signup AUS). Ziel-Konvention = Authentik-OIDC, sobald Twenty-SSO-Pfad
  verifiziert — als Folgepaket `crm-authentik-oidc`. Der App-Login erfüllt die
  Public-Endpoint-Auth-Pflicht bereits in der MVP-Phase.

## Phasen / Epics

**E1 — Box-Vorbereitung & DNS**
- `cf-dns` A/Proxied-Record `crm.stayawesome.app` → 178.105.36.33.
- Verzeichnis `/opt/twenty` auf deploy, compose + `.env` ablegen.
- Docker-NAT/Netz prüfen (eigenes compose-Netz bekommt Internet — Masquerade-Muster).
- **Done:** `dig +short crm.stayawesome.app` liefert die deploy-IP/CF-Proxy; `/opt/twenty/{docker-compose.yml,.env}` existieren.

**E2 — Twenty-Stack hochfahren**
- Upstream-compose adaptieren (Server+Worker+Postgres+Redis), Ports nur localhost.
- `APP_SECRET` generieren, `SERVER_URL=https://crm.stayawesome.app`, DB-/Redis-URLs.
- `docker compose up`, Health prüfen (server :3000 antwortet).
- **Done:** `docker ps` zeigt 4 Twenty-Container (server/worker/postgres/redis) healthy; `curl -fsS localhost:3000` → 200.

**E3 — nginx + TLS**
- vhost `crm.stayawesome.app` (sites-available/-enabled), certbot-webroot-Cert.
- HTTP→HTTPS-Redirect, WebSocket-Header (Twenty nutzt WS) durchreichen.
- **Done:** `curl -I https://crm.stayawesome.app` → 200/302 mit gültigem Cert; `ls /etc/nginx/sites-enabled/crm*` existiert.

**E4 — Initial-Setup & Pipeline**
- Workspace + Admin-User anlegen, Self-Signup deaktivieren.
- Fundraising-Pipeline mit Stages anlegen (G4), erste Investoren-Felder
  (Ticket-Betrag, Quelle, Owner, Next-Step) als Custom Fields.
- **Done:** Anonymer Zugriff wird zu Login umgeleitet; Kanban-Board mit allen 6 Stages sichtbar.

**E5 — Integration & Doku**
- `manifests/external/crm.json` (Cockpit-Registrierung, lane A).
- Credentials + APP_SECRET in `/root/.secrets/credentials.md`.
- Deploy-Artefakte ins Repo (compose + `.env.example`), Delivery-Report.
- **Done:** Cockpit-Tile „CRM" sichtbar; Vault-Eintrag vorhanden; compose + `.env.example` im Repo committet.

## Risiken / offene Punkte

| # | Risiko | Mitigation |
|---|---|---|
| R1 | Twenty-Postgres-Extension-Anforderung unklar | Upstream-compose/Doku als Quelle, dediziertes offizielles DB-Image nutzen |
| R2 | RAM-Bedarf Twenty (Server+Worker+PG+Redis) auf 15-GB-Box | 14 GB frei → ausreichend; nach Start `docker stats` prüfen. **Eingriffs-Schwelle:** wenn freier RAM < 2 GB → Worker-Replicas auf 1 kappen, sonst Box-Upsize prüfen |
| R3 | Authentik-OIDC-Pfad in Twenty evtl. Enterprise-gated | MVP mit App-Login (erfüllt Auth-Pflicht); OIDC als separates Paket evaluieren |
| R4 | AGPL-Lizenz | reiner Self-Host-Betrieb, keine Distribution → unkritisch |

> **ABANDONED 2026-06-23:** Stay Awesome hat bereits einen HubSpot-Account.
> Self-Host-Twenty wird nicht gebaut; Fundraising-Tracking läuft in HubSpot
> (Pragmatik + natives Gmail-Logging > Souveränität für diese Runde). Dieser PRD
> bleibt als Entscheidungs-Trail erhalten.

## Reviewer-Verdicts

<!-- Quick/Deep-Verdicts werden hier angehängt (Datum + Verdict + Asks). -->

## Reviewer-Verdict — quick (glm-5.1) — 2026-06-23

**Verdict:** `approved-with-notes`

Solider, konformer PRD-Entwurf mit klarer Problemstellung, plausibler Tool-Entscheidung und sauberer Scope-Abgrenzung. Architektur-Entscheidungen sind nachvollziebar begründet, Alternativen erkennbar verworfen. Alle Epics haben mindestens ein überprüfbares Done-Kriterium über die Zieltabelle abgedeckt.

**Findings:**
- [minor] **Done-Kriterien nur auf Epic-Ebene implizit** — Die messbaren Ziele (G1–G9) sind exzellent, aber die Epics selbst haben keine expliziten Done-Kriterien pro Epic. Die Verknüpfung Epic→Ziel ist teilweise implizit (z.B. E3→G1/G6).

**Asks:**
- [ ] Füge pro Epic (E1–E5) ein kurzes, explizites Done-Kriterium hinzu (z.B. 'E2 Done: docker ps zeigt 4 Twenty-Container, curl localhost:3000 → 200'). Die Ziele G1–G9 bleiben unangetastet als übergeordnete Success-Kriterien.
- [ ] R2 (RAM-Bedarf): Ergänze einen konkreten Schwellwert, ab dem man eingreift (z.B. 'wenn freier RAM < 2 GB nach docker stats → Worker-Replicas auf 1 kappen oder Box-Upsize prüfen').
