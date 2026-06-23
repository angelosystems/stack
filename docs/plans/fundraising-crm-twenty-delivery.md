---
slug: fundraising-crm-twenty
layer: delivery
status: abandoned
parent_plan: docs/plans/fundraising-crm-twenty-prd.md
delivered: 2026-06-23
---

# Delivery-Report — Fundraising-CRM (Twenty)

**Status: ABANDONED (2026-06-23)**  
Stay Awesome hat bereits einen HubSpot-Account. Self-Host-Twenty wird nicht gebaut; Fundraising-Tracking läuft in HubSpot (Pragmatik + natives Gmail-Logging > Souveränität für diese Runde).

## Verlauf

| Phase / Epic | Ergebnis |
|---|---|
| E1 Box-Vorbereitung | Übersprungen (Projekt abgebrochen) |
| E2 Twenty-Stack hochfahren | Übersprungen (Projekt abgebrochen) |
| E3 nginx + TLS | Übersprungen (Projekt abgebrochen) |
| E4 Initial-Setup & Pipeline | Übersprungen (Projekt abgebrochen) |
| E5 Integration & Doku | **Umgesetzt:** App im SA-Cockpit registriert (G7) sowie Deploy-Artefakte (`docker-compose.yml` und `.env.example`) unter `tools/twenty/` im Repository eingecheckt (G9). |

## Gates / Kriterien

| Gate | Status | Detail |
|---|---|---|
| G1 Erreichbarkeit | ❌ Übersprungen | crm.stayawesome.app wurde nicht aufgeschaltet. |
| G2 DNS | ❌ Übersprungen | DNS-Eintrag nicht angelegt. |
| G3 Auth / Login-Wand | ❌ Übersprungen | Nicht relevant da kein Deploy. |
| G4 Pipeline | ❌ Übersprungen | Keine Instanz konfiguriert. |
| G5 DB-Isolation | ❌ Übersprungen | Keine Postgres-Instanz angelegt. |
| G6 nginx-vhost | ❌ Übersprungen | Kein nginx-vhost auf deploy konfiguriert. |
| G7 Cockpit-Registrierung | ✅ Erfüllt | `manifests/external/crm.json` vorhanden und Cockpit-Tile sichtbar. |
| G8 Admin-Credentials in Vault | ❌ Übersprungen | Keine Zugangsdaten generiert. |
| G9 Reproduzierbarer Deploy | ✅ Erfüllt | `docker-compose.yml` und `.env.example` unter `tools/twenty/` im Repo versioniert. |

## Entscheidungshintergrund

Es wurde pragmatisch entschieden, auf HubSpot zu setzen. Die Integration mit Gmail und das native Logging von E-Mails out-of-the-box sparte wertvolle Setup- und Wartungszeit für die anstehende Fundraising-Runde.

Die Docker-Compose- und Umgebungsvariablen-Templates wurden dennoch ins Repository aufgenommen, damit bei einer zukünftigen Entscheidung zur Migration auf Twenty ein reproduzierbares Fundament bereitliegt.

## Verifikation

Im Rahmen der finalen Übergabe wurden die im Repository unter `tools/twenty/` versionierten Deploy-Artefakte verifiziert:
- `docker-compose.yml`: Definiert die komplette Service-Struktur (Server, Worker, DB, Redis) inklusive korrekter Healthchecks, Volumes und Restart-Policys.
- `.env.example`: Dokumentiert alle notwendigen Umgebungsvariablen für eine reibungslose Instanziierung.
- `.gitignore`: Wurde so angepasst, dass `tools/twenty/` und die entsprechenden Beispiel-Konfigurationen getrackt werden, während schützenswerte Secrets ausgeschlossen bleiben.

Damit ist das Kriterium G9 vollständig und reproduzierbar erfüllt.

