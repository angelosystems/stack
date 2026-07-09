---
title: Vaultwarden Team-Roll-Up & Cloud-Cutover
slug: vaultwarden-team-rollout
status: draft
layer: prd
parent_plan: /opt/stack/docs/plans/secrets-topologie-prd.md
scope: Vollständiger Cutover der Stay-Awesome-Passwortverwaltung von Bitwarden-Cloud (aktuelles Live-System) auf die self-hosted Vaultwarden-Instanz (vault.stayawesome.app, Box 178.105.55.184) — inkl. Version-Update, Reparatur des Invite-Mail-Pfads, Re-Baseline der Org-Daten, Team-Onboarding, Client-Umstellung, Self-Service-Migration persönlicher Tresore und geordneter Cloud-Stilllegung.
created: 2026-07-01
review:
  quick: auto
  deep: spec-panel
  panel-mode: critique
  panel-focus: [architecture, compliance]
references:
  - /opt/stack/docs/plans/secrets-topologie-prd.md
  - reference_stayawesome_bitwarden (Memory)
  - reference_vault_box (Memory)
---

# Vaultwarden Team-Roll-Up & Cloud-Cutover

## Kontext & Motivation

Die parent-PRD `secrets-topologie` legt fest: **Vaultwarden ist die Mensch-Schnittstelle
für interaktive/private Secrets** (im Gegensatz zu OpenBao für maschinen-injizierbare).
Dieses PRD führt den dazu gehörenden operativen Schritt aus: den tatsächlichen Umzug der
gesamten Team-Passwortverwaltung von der Bitwarden-Cloud auf die eigene Vaultwarden-Box.

Der Umzug wurde im Mai 2026 begonnen, aber **nie abgeschlossen** — und als „fertig"
fehldokumentiert. Dieses PRD korrigiert das und bringt den Cutover sauber zu Ende.

## Verifizierter IST-Stand (erhoben 2026-06-30/07-01 auf werkstatt + Box)

```
Bitwarden-Cloud (vault.bitwarden.com)   →  LIVE System of Record: ganze Firma arbeitet hier,
                                            Org "Stay Awesome GmbH", 23+ Sammlungen, aktiv
Self-hosted (vault.stayawesome.app)     →  Container healthy (2026.3.1 / web 1.35.8), Backups täglich
                                            ABER: eingefrorener Snapshot vom 2026-05-02 (320 Items,
                                            alle in einem ~23-Min-Import-Fenster, seither KEIN Write)
Invite-Mail-Pipeline                    →  KAPUTT: Login-Notification-Mails gehen raus, Org-Invite-
                                            Mails NICHT (Reinvite HTTP 200, Status blieb 0, keine Mail).
                                            Mutmaßliche Wurzel des Mai-Stalls.
SSO-Gate (oauth2-proxy, Google Workspace) → schützt die Web-Oberfläche; /api + /identity sind
                                            freigeschaltet → Apps/Extensions gehen dran vorbei.
Zugang Mario                            →  2026-06-30 confirmed (status=2) via Status-Flip + bw confirm
                                            (am kaputten Mail-Pfad vorbei).
```

## Strategische Entscheidungen

### E1 — Cutover-Modell: Parallelbetrieb, kein Big-Bang
Moderne Bitwarden-Clients halten mehrere Konten. Jedes Teammitglied fügt das Self-hosted-Konto
**zusätzlich** hinzu und kann zwischen Cloud und Self-hosted umschalten. Die Cloud bleibt als
Fallback aktiv, bis alle verifiziert auf Self-hosted sind. Erst dann Stilllegung.

### E2 — Kein transparentes Rerouting
Es gibt keinen zentralen „Umschalter". Bitwarden-Clients fragen per Default `bitwarden.com`.
Der Umzug erfolgt pro Client über **eine Einstellung** (Server/Region → `https://vault.stayawesome.app`).
Zentral orchestriert werden Daten, Accounts und Confirmation; die Client-Umstellung ist ein
angeleiteter Ein-Klick-Schritt pro Person.

### E3 — Zwei Datenklassen, zwei Migrationswege
```
Org "Stay Awesome" (Sammlungen)   →  ZENTRAL migrierbar   →  Owner-Export/Import
"Mein Tresor" (private Einträge)  →  NUR der User selbst   →  Zero-Knowledge, self-service
```
Persönliche Tresore sind mit dem User-eigenen Schlüssel verschlüsselt — kein Admin (auch nicht
der Owner) kann sie sehen, exportieren oder migrieren. Phase für persönliche Daten ist zwingend
Self-Service.

---

## Constraints (D-Rules)

- **D1 — Zero-Knowledge persönlicher Tresore:** Kein Admin-Handling von Klartext persönlicher
  Einträge. Persönliche Migration ausschließlich self-service durch den jeweiligen User.
- **D2 — Cloud-Kündigung erst nach grüner Verifikation:** Das Bitwarden-Cloud-Abo (aktuelles
  Live-System) wird NICHT gekündigt, bevor die Verifikations-Checkliste (W8) vollständig grün ist.
- **D3 — Parallelbetrieb als Netz:** Kein Big-Bang. Cloud bleibt Fallback bis zum verifizierten
  Abschluss aller Mitglieder.
- **D4 — Backup vor jeder Mutation:** Frisches Backup vor Version-Update (W1) und vor Re-Baseline
  (W3). Verifiziert über `restore.sh --list`/`smoketest.py`.
- **D5 — Auth-Posture nicht senken:** Die Web-Oberfläche bleibt hinter SSO oder gleichwertiger
  App-Auth. Clients nutzen ausschließlich die freigeschalteten `/api`+`/identity`-Pfade. Kein
  offener vhost ohne Auth (globale Konvention).

---

## Arbeitspakete

Konvention: ⚒ Arbeit 1-5, ◆ Wert 1-5, ⚠ Gate, ◎ Eleganz 1-5.

### W1 — Version-Update & Backup-Baseline   ⚒2 ◆3 ◎3
Vaultwarden auf aktuellen Stand bringen und Ausgangs-Backup sichern.
- `bash /opt/vault/scripts/backup.sh` — frisches pg_dump + data-tar → Object Storage.
- `docker compose pull vaultwarden && docker compose up -d vaultwarden` — Daten-Volume bleibt.
- Image von `:latest` auf einen **festen Version-Tag pinnen** (kein Auto-Pull-Überraschungsrisiko).
- `python3 /opt/vault/scripts/smoketest.py` — Login, Cipher-Count, API-Version.
- ⚠ **Gate:** smoketest grün, Version gepinnt, Backup in Object Storage verifiziert.

### W2 — Invite-Mail-Pipeline reparieren   ⚒3 ◆5 ◎3
Der harte Blocker. Ohne funktionierende Invite-Mails scheitert jedes Self-Service-Onboarding.
- Diagnose des Gmail-SMTP-Invite-Pfads (App-Passwort-Gültigkeit, `send_invite`-Template,
  Bounce am konfigurierten Gmail-Absenderkonto).
- Reparatur ODER dokumentierte Entscheidung für den validierten skript-basierten Onboarding-Pfad
  (registrieren → einladen → `status=1` → `bw confirm`), der ohne Mail funktioniert.
- ⚠ **Gate:** Eine echte Org-Invite-Mail wird nachweislich end-to-end an ein Test-Postfach
  zugestellt — ODER der mail-freie Onboarding-Pfad ist skriptiert und an einem Test-Account bewiesen.

### W3 — Re-Baseline aus der Live-Cloud   ⚒3 ◆5 ◎4
Den 2 Monate alten Snapshot durch den aktuellen Cloud-Stand ersetzen.
- Owner-Export der Org "Stay Awesome" aus der Bitwarden-Cloud (Mario, `.json`).
- Import in die self-hosted Org; Abgleich Sammlungen + Item-Count; Dubletten-Handling.
- ⚠ **Gate:** self-hosted Org-Item-Count deckt den Cloud-Stand ab (Delta dokumentiert),
  jüngstes Item-Datum ist aktuell (nicht mehr 2026-05-02).

### W4 — Struktur: Sammlungen, Rollen, Gruppen   ⚒2 ◆4 ◎4
Zugriffsmatrix definieren, bevor Menschen reinkommen.
- Zugriffsmatrix: welche Rolle/Gruppe sieht welche Sammlung (z. B. Finanzen nur Finance).
- Rollen (Owner/Admin/User/Manager) + Gruppen statt Einzel-Grants (skaliert besser).
- ⚠ **Gate:** Matrix dokumentiert + angewandt; ein Test-User in Gruppe X sieht ausschließlich
  die vorgesehenen Sammlungen.

### W5 — Team-Onboarding   ⚒3 ◆5 ◎4
Accounts einladen/anlegen und confirmen.
- Team-Mitglieder einladen (über W2-Pfad); jeder setzt eigenes Master-Passwort.
- Confirmation skriptgesteuert per `bw confirm org-member` (Owner-Krypto, wrappt Org-Key).
- Klärung SSO-Voraussetzung: wer kein @stayawesome.de-Workspace-Konto hat, braucht eins ODER
  das Gate wird für den Login-Pfad angepasst (siehe D5).
- ⚠ **Gate:** alle Mitglieder `status=confirmed` mit gewrapptem Org-Key; Collection-Zugriff
  entspricht der W4-Matrix.

### W6 — Client-Cutover (Parallelbetrieb)   ⚒2 ◆4 ◎3
Jeder richtet seinen Client auf die Box.
- Anleitung (3-Klick, mit Screenshots): Client → Region/Server → Self-hosted →
  `https://vault.stayawesome.app` → einloggen. Self-hosted-Konto **zusätzlich** zur Cloud.
- ⚠ **Gate:** jedes Mitglied ist im eigenen Client auf Self-hosted eingeloggt und sieht die
  vorgesehenen Sammlungen.

### W7 — Persönliche Tresore (Self-Service)   ⚒2 ◆3 ◎3
Private Einträge migrieren — nur der User kann das (D1).
- Anleitung pro Person: Cloud-Export (`.json`, unverschlüsselt oder passwortgeschützt — NICHT
  kontobeschränkt) → Import in "Mein Tresor" self-hosted → Export-Datei sicher löschen.
- Empfehlung: Arbeits-Logins aus "Mein Tresor" in passende Org-Sammlungen verschieben (geteilt,
  auffindbar, überlebt Personalwechsel).
- ⚠ **Gate:** jede Person bestätigt persönliche Migration abgeschlossen ODER bewusst ausgelassen.

### W8 — Umschalten & Cloud-Decommission   ⚒2 ◆5 ◎4
Geordnete Stilllegung — der irreversible Schritt zuletzt.
- Stichtag: alle arbeiten nur noch self-hosted; Cloud eingefroren/read-only.
- Verifikations-Checkliste durchgehen (siehe Erfolgs-Kriterien).
- ⚠ **Gate:** Checkliste vollständig grün → **erst dann** Cloud-Abo kündigen (D2).

---

## Erfolgs-Kriterien (messbar)

- [ ] Vaultwarden läuft auf festem Version-Tag; smoketest grün; frisches Backup vor Cutover in Object Storage.
- [ ] Test-Invite-Mail nachweislich zugestellt ODER mail-freier Onboarding-Pfad skriptiert + validiert.
- [ ] Self-hosted Org-Item-Count deckt den Cloud-Stand ab (Delta dokumentiert); jüngstes Item aktuell.
- [ ] Zugriffsmatrix angewandt; Test-User sieht nur die für seine Gruppe vorgesehenen Sammlungen.
- [ ] Alle Team-Accounts `status=confirmed` mit gewrapptem Org-Key.
- [ ] Jedes Mitglied im eigenen Client auf `vault.stayawesome.app` eingeloggt und arbeitsfähig.
- [ ] Persönliche Tresor-Migration je Person bestätigt oder bewusst ausgelassen.
- [ ] Cloud-Abo erst nach grüner Verifikations-Checkliste gekündigt (D2 eingehalten).

---

## Limitations & Abgrenzungen

- **Persönliche Tresore nicht zentral migrierbar** (Zero-Knowledge) — ausschließlich self-service.
  Verantwortung und Ausführung liegen beim jeweiligen User.
- **Kein transparentes Rerouting** — der Client-Server-Wechsel ist pro Client manuell (ein Schritt).
- **SSO-Gate begrenzt die Web-Oberfläche** auf @stayawesome.de-Workspace-Konten; Nicht-Workspace-
  Mitglieder brauchen ein Konto oder eine Gate-Anpassung.
- **Single-Point-of-Failure Box `178.105.55.184`** (wie in der parent-PRD) — verschlüsselte
  Backups sind Pflicht; Restore-Pfad (`restore.sh`) vor Cutover einmal getestet.
- **Invite-Mail-Reparatur** kann tieferliegende Gmail-SMTP-Grenzen aufdecken (App-Passwort-
  Rotation, Sende-Limits) — Fallback ist der mail-freie Onboarding-Pfad.
- **QuantBot / Solartown / Mario-privat** bleiben unberührt (Trennung durch Ausschluss, parent-PRD).

---

## Reviewer-Verdict — quick (manuelle Struktur-Vorprüfung) — 2026-07-01

> **Hinweis zur Herkunft:** Dies ist KEIN automatischer Crew-R1-R5-Lauf. Die Crew
> `solartown/crew/plan_reviewer` war nicht erreichbar (`gt`: „not in a Gas Town
> workspace"; Crew nicht gestartet), und der laufbare `plan-reviewer.js check-all`
> arbeitet im Impl/Delivery-Modus (Mode B) → liefert bei einem Pre-Code-PRD den
> erwartbaren Null-Befund `rejected: delivery missing`. Untenstehendes ist eine
> manuelle Vorab-Struktur-Prüfung der pre-code sinnvollen Checks. Der maschinelle
> Quick-Verdict ist vor Bead-Generierung nachzuholen (Crew-Reviewer reparieren
> oder PRD committen → planfile-sync enqueued Mode-A-Event).

- **Verdict (vorläufig):** approved-with-notes
- **R1 Success-Criteria:** ✓ 8 messbare Kriterien (Checkbox-Liste, verifizierbar)
- **R2 Rules:** ✓ D1–D5 definiert und in Gates referenziert (D1→W7, D2/D3→W8, D4→W1/W3, D5→W5) — Abhaken erfolgt bei Implementation
- **R3 Artifacts:** n/a (PRD vor Implementation, keine Delivery)
- **R4 Limitations:** ✓ 6 Limitations mit Root-Cause/Decision dokumentiert
- **R5 Git-Events:** n/a (PRD vor Implementation)
- **Notes:**
  1. `review.deep: spec-panel` im Frontmatter gesetzt (Firma-weite Credentials = compliance-sensibel) — Deep-Tech-Panel vor Execution empfohlen.
  2. **W2 (Invite-Mail-Fix) ist der Critical-Path-Unbekannte** — Gate muss vor W5 grün sein, sonst blockiert es das gesamte Onboarding.
  3. Maschinellen Crew-Quick-Verdict nachziehen, bevor Beads generiert werden (Konvention: „Kein PRD ohne Quick-Verdict").
</content>
</invoke>
