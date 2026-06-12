---
slug: documenso-werkstatt-migration
layer: delivery
status: delivered-p4
parent_plan: docs/plans/documenso-werkstatt-migration-prd.md
delivered: 2026-06-11
---

# Delivery-Report — Documenso-Migration auf werkstatt

**Umgesetzt 2026-06-11** (Mario-Go im Chat, Quick-Review approved-with-notes, glm-5.1).

## Was lief

| Phase | Ergebnis |
|---|---|
| P1 Inventur | Stack 6 Wochen alt („latest", envelope-era); SMTP zeigte auf smtp.gmail.com (Gaia) — **nicht** mailpit; Ursache Mail-Hänger: distribute/resend enqueuen in dieser Build **keine** Mail-Jobs (BackgroundJob enthielt nur Crons), Fehler wurden nicht geloggt. DB 17 MB, Upload-Transport database. |
| P2 Aufbau | `/opt/documenso/` (compose, Container `documenso`, 127.0.0.1:3500, Netz `sa-db_default`), Cert via certbot DNS-01 (SAN: sign.stayawesome.app + .dev), nginx-vhost nach werkstatt-Konvention. OIDC: Provider pk 3 + App `documenso` in Authentik, ENVs gesetzt. |
| P3 Daten | App auf sa-sign gestoppt → pg_dump (1,2 MB) → restore in sa-db `stayawesome_signing` (Rolle `documenso`). Frischer `latest`-Pull wendete 157 Migrationen an (de-facto Versions-Update — Nicht-Ziel bewusst überstimmt, da Mail-Pfad der Alt-Build defekt). |
| P4 Cutover | cf-dns: beide A-Records auf 178.104.255.22 (Achtung: `add-app --force` ERGÄNZT statt ersetzt → delete + add nötig). |
| P5 Quarantäne | Läuft: sa-sign-App Exited, Postgres/Caddy up (Rollback-Daten intakt). P6 (Decom VM 128570894 + Object-Storage-Backup) nach 7 Tagen offen. |

## Gates

| Gate | Status |
|---|---|
| G1 OIDC-Button | ✅ „Stay Awesome Login" auf /signin |
| G2 OIDC-Flow | ✅ authorize → 302 in Authentik-Flow; JIT-Klick-Test durch Mario offen |
| G3 Dokumente | ✅ beide Envelopes (doc 6 Test, doc 7 Preiss) gelistet, Signier-Seite rendert (HTTP 200) |
| G4 DNS | ✅ beide Records → werkstatt, CF-proxied |
| G5 Decom | ⏳ Quarantäne aktiv, Decom nach 7d |
| G6 nginx-Konvention | ✅ sites-available/sign.stayawesome.app |
| G7 Encryption-Key | ✅ Keys 1:1 aus Vault (`sa-sign-documenso-secrets.txt`), Alt-Dokument rendert |

## Bonus-Ergebnis

**Mail-Versand funktioniert wieder:** Signing-Request an k.preiss@garbe.de = `sendStatus SENT` (Dokument 7, Vereinbarung Preiss-Austritt). Befund: Resend an den Dokument-**Owner** wirft weiterhin einen (ungeloggten) Fehler — Owner signiert direkt in der App, kein Blocker.

## Offene Punkte

1. **P6 Decommission** nach Quarantäne: Final-Backup nach `hetzner-nbg1`, `hcloud server delete 128570894`, Manifest/Memory nachziehen.
2. **JIT-Login-Test** durch Mario (erster Klick auf „Stay Awesome Login").
3. v1-API kann ohne S3 keine Dokumente anlegen/downloaden → v2-beta nutzen (multipart `payload` + `file`).
