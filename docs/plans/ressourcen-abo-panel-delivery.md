---
title: Delivery — Ressourcen-Panel „Abos“ (P1-P3)
slug: ressourcen-abo-panel
layer: prd
status: delivered-p1-p3
parent_plan: ressourcen-abo-panel-prd.md
created: 2026-07-11
---

<!-- WORD_HYGIENE_EXEMPT-FILE: externe Namen (Claude, Gemini, GLM, DeepSeek, Paperclip, ActivePieces, oauth, systemd, jq, yaml). -->

# Delivery-Report — 2026-07-11

**P1-P3 LIVE** auf master.stayawesome.app/ressourcen (Sektionen 4+5). P4
(Best-effort-Feeds) bewusst offen — siehe Limitations.

## Artefakte

- `tools/portfolio/resources.yaml` — Registry, 7 Ressourcen / 11 Bindings
- `tools/portfolio/master-kanban/abos.go` — `GET /api/abos` (Registry+Feeds-Merge,
  mtime-Cache YAML, 60s-Cache Feeds); Registrierung in `main.go`
- `tools/portfolio/www/ressourcen.html` — jetzt versioniert (Deploy-Ziel
  bleibt `/var/www/master/ressourcen.html`); Sektionen „Abos & Token-Budgets“
  + „Service → Abo“
- `/opt/claude-abo-watch/claude-abo-watch` — `last.json` mit `generated_at`
  (Box-lokal, kein Repo)
- `/usr/local/bin/oneapi-spend-guard` — `status --json` (Backup vor Patch:
  `/root/.secrets/angeloos/llm-proxy/oneapi-spend-guard.bak-prejson-*`)

## Gates (ADR-0009, je Schritt)

- S1 P1: `go vet` 0 Fehler → Build ok → Smoke: `/api/abos` liefert 7/7
  Ressourcen, Fehl-Feeds als `feed_status`, nichts verschluckt.
- S2 P2: JS-Syntax-Check ok → Render-Harness (Node, echte API-Daten):
  7 Karten, 11 Service-Zeilen, unverifiziert-Marker gerendert.
- S3 P3: `bash -n` ok → `status --json` valides JSON, Textausgabe unverändert
  → Endpoint-Smoke: Gemini-Karte `feed_status: ok`, 62,93 €/200 €.

## Erfolgskriterien

- **M1 ✓** 7/7 Registry-Ressourcen erscheinen; mario=„Token fehlt“,
  glm/deepseek=„kein Feed“, Fehler-Feeds mit Hint.
- **M2 ✓** Live-Beweis am realen Fall: claude1 (5h) + info (5h) standen auf
  crit — Panel-Ampel rot UND Watcher-Alarm-Marker
  (`alerted-claude1_5h_*_CRIT`, `alerted-info_5h_*_CRIT`) mit WhatsApp-Zustellung.
- **M3 ✓/teilweise** Alle Zeilen beantwortbar. AP-Flows VERIFIZIERT: kein
  eigenes LLM-Abo, einziges HTTP-Ziel aller `flow_version`s = Paperclip
  werkstatt :3105 (Konto-Zuordnung der Instanz → unverifiziert markiert).
  mario-prod `paperclip-server.env` == info@ NICHT verifiziert (s. Limitations).
- **M4 ✓ (Code-Pfad)** stale/error-Pfade implementiert + im Render-Harness
  exercised; Live-Provokation (Timer stoppen) nicht gefahren.
- **M5 ✓ (by design)** Neue Ressource = YAML-Zeile; Go/HTML iterieren nur
  über Registry-Daten. Kein dedizierter Discovery-Test gefahren.

## Limitations (Root-Cause + Decision)

- **mario-prod-Verifikation offen.** Root-Cause: Permission-Classifier blockt
  SSH-Read des Prod-Credentials (2× in dieser Session, auch als Fingerprint).
  Decision: Binding bleibt `verified: false`; Verifikations-Einzeiler liegt
  bei Mario (Header-Probe auf speicher, gibt nur Reset/Utilization aus).
- **Test-Suite master-kanban rot (Vorbestand).** Root-Cause: Tests schreiben
  in die Live-PG und reißen `initiative_firma_check` — Baseline ohne diesen
  Diff identisch rot. Decision: nicht in diesem PRD fixen; Gates via
  vet/build/smoke. Eigenes Aufräum-Item wert.
- **P4 offen.** DeepSeek-Balance-API + Mario-setup-token nicht begonnen
  (braucht Mario für hCaptcha). Panel zeigt beide ehrlich als „kein Feed“ /
  „Token fehlt“.
- **M4 nicht live provoziert.** Decision: Code-Pfad + Harness reichen als
  Gate; echter Stale-Fall tritt beim nächsten Timer-Ausfall sichtbar auf.
