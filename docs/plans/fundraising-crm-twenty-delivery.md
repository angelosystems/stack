---
title: Fundraising-CRM (Twenty) — Delivery-Report
slug: fundraising-crm-twenty-delivery
status: abandoned
layer: delivery
parent_plan: docs/plans/fundraising-crm-twenty-prd.md
created: 2026-06-23
---

# Fundraising-CRM (Twenty) — Delivery-Report

## Kontext & Status

**Status: Abgebrochen (Abandoned) am 2026-06-23.**

Wie im PRD (`docs/plans/fundraising-crm-twenty-prd.md`) dokumentiert, hat Stay Awesome bereits einen aktiven HubSpot-Account. Aus Gründen der Pragmatik und des nativen Gmail-Loggings wird das Fundraising-Tracking in HubSpot fortgeführt. Das Self-Hosted Twenty CRM wird daher vorerst nicht produktiv auf der `deploy`-Box aufgesetzt.

Dieser Delivery-Report dokumentiert die Erfüllung des Ziels **G9** (reproduzierbarer Deploy durch Einpflegen der Compose- und Env-Vorlagen ins Repository), während alle anderen Betriebsziele aufgrund des HubSpot-Entscheids als obsolet/nicht anwendbar deklariert wurden. Der PRD und die Deploy-Artefakte bleiben als vollständiger Entscheidungs- und Wiederaufsetz-Trail im Repository erhalten.

## Nachvollziehbarkeit & Artefakte

Die Deployment-Artefakte wurden unter `tools/twenty/` eingecheckt, um ein jederzeitiges, reproduzierbares Aufsetzen zu ermöglichen.

| Datei | Pfad | Beschreibung |
|---|---|---|
| Docker Compose | `tools/twenty/docker-compose.yml` | Stack aus `twenty-server`, `twenty-worker`, `twenty-postgres` (mit den nötigen Postgres-Extensions) und `twenty-redis` |
| Env Template | `tools/twenty/.env.example` | Konfigurations-Schlüssel für DB, Redis, Ports, Signup-Disabling und Security-Secrets |

## Abgleich der Ziele (G1–G9)

| ID | Success-Kriterium | Status | Bemerkung |
|---|---|---|---|
| **G1** | Twenty erreichbar unter `https://crm.stayawesome.app` mit gültigem TLS | 🚫 Abgebrochen | HubSpot-Wechsel hat Priorität |
| **G2** | DNS `crm.stayawesome.app` → deploy-IP, proxied via CF | 🚫 Abgebrochen | HubSpot-Wechsel hat Priorität |
| **G3** | Kein offener Zugang: Login-Wand vor jeder Funktion | 🚫 Abgebrochen | HubSpot-Wechsel hat Priorität |
| **G4** | Fundraising-Pipeline existiert mit allen 6 Stages | 🚫 Abgebrochen | HubSpot-Wechsel hat Priorität |
| **G5** | Twenty-DB isoliert von sa-db (eigene Postgres-Instanz) | 🚫 Abgebrochen | HubSpot-Wechsel hat Priorität |
| **G6** | nginx-vhost folgt system-nginx-Konvention | 🚫 Abgebrochen | HubSpot-Wechsel hat Priorität |
| **G7** | App im SA-Cockpit registriert | 🚫 Abgebrochen | HubSpot-Wechsel hat Priorität |
| **G8** | Admin-Credentials + APP_SECRET in Vault | 🚫 Abgebrochen | HubSpot-Wechsel hat Priorität |
| **G9** | Deploy reproduzierbar (compose + env-template getrackt) | ✅ Erfüllt | Artefakte im Repo unter `tools/twenty/` committet |

## Installationsanleitung (bei Re-Aktivierung)

Falls sich Stay Awesome in Zukunft für eine Migration von HubSpot auf Twenty entscheidet, kann der Stack wie folgt initialisiert werden:

1. **Verzeichnis erstellen & Dateien kopieren:**
   ```bash
   mkdir -p /opt/twenty
   cp tools/twenty/docker-compose.yml /opt/twenty/
   cp tools/twenty/.env.example /opt/twenty/.env
   ```
2. **Secrets generieren:**
   Zwei sichere, zufällige Zeichenketten für `APP_SECRET` und `ENCRYPTION_KEY` erzeugen:
   ```bash
   openssl rand -base64 32
   ```
   Die Werte in `/opt/twenty/.env` eintragen und das Datenbank-Passwort anpassen.
3. **Stack hochfahren:**
   ```bash
   cd /opt/twenty
   docker compose up -d
   ```
4. **Reverse-Proxy (Nginx) konfigurieren:**
   Einen system-nginx-vhost für `crm.stayawesome.app` anlegen und Requests auf `http://127.0.0.1:3000` weiterleiten. TLS per Certbot-Webroot einrichten.
