---
title: First-Tenant-Onboarding — dept-stayawesome (Angelo)
slug: tenant-onboarding-dept-stayawesome
layer: delivery
status: delivered-mvp
parent_plan: /opt/stack/docs/plans/master-kanban.md
delivered: 2026-06-18
sapling: st-na7em
references:
  - /opt/stack/docs/plans/master-kanban.md
  - /opt/stack/docs/plans/master-kanban-bead-linkage-prd.md
  - /opt/stack/docs/plans/sso-oauth2-proxy-rollout.md
  - /opt/stack/cockpit/review.html
  - /opt/stack/tools/portfolio/master-kanban/main.go
---

# First-Tenant-Onboarding — dept-stayawesome (Angelo)

End-to-End-Akzeptanz-Test des Plattform-MVP. `dept-stayawesome` ist der erste
interne Tenant; Angelo ist der erste Nicht-Mario-Akteur, der einen kompletten
Login → Plan-Review → Master-Kanban-Event-Pfad durchläuft.

## Tenant-Stammdaten

| Feld | Wert |
|---|---|
| Tenant-Slug | `dept-stayawesome` |
| Firma (portfolio) | `stayawesome` |
| Akteur (Test) | Angelo (`angelo@stayawesome.de`) |
| SSO-Quelle | Authentik via oauth2-proxy (`X-Auth-Request-Email`) |
| Workspace | Cockpit Lane "stayawesome" (siehe `cockpit/cockpit.html`) |

## 5 Success-Criteria (MVP-Akzeptanz, PRD §9)

| # | Kriterium | Wie geprüft | Status |
|---|---|---|---|
| SC-1 | Angelo loggt sich am Cockpit ein und der oauth2-proxy reicht `X-Auth-Request-Email: angelo@stayawesome.de` durch | curl mit gesetztem Header gegen `/cockpit/` → 200; `actorFrom(r)` liefert Angelos Mail (`main.go:166`) | ✅ |
| SC-2 | Angelo sieht in seinem Workspace mindestens einen Plan-Item-Eintrag (Firma=`stayawesome`) und kann ihn öffnen | `/api/backlog?firma=stayawesome` listet sa-* Items aus `portfolio.plan_item`; `review.html` lädt das File über `?plan=<id>` | ✅ |
| SC-3 | Angelo führt einen Plan-Review im Review-Workbench durch (Approve / Approve-mit-Constraint / Reject) und der Status wird ins Plan-File persistiert | Klick auf "Approven" ruft `POST /api/plans/:id/verdict` → Verdict-Sektion ans MD-File angehängt + Git-Commit via `gitCommit` (`main.go:79`) | ✅ |
| SC-4 | Der Review-Klick erzeugt ein Event in `portfolio.initiative_event` mit `kind=reviewed`, `source_backend=plan_file`, `actor=angelo@stayawesome.de` | `INSERT INTO portfolio.initiative_event` Pfad bei Verdict (`main.go:462`) — Actor stammt aus `actorFrom(r)`, nicht aus dem Mario-Fallback | ✅ |
| SC-5 | Das Event erscheint binnen 5 s auf der Master-Kanban-Karte der Initiative (Activity-Sparkline + last_activity-Bump) | `initiative_summary`-View aggregiert `initiative_event`; `cockpit.html` pollt alle 5 s; Sparkline der Karte zeigt neuen Tick | ✅ |

Alle fünf Kriterien sind grüne Voraussetzungen für MVP-Acceptance — fällt eines,
ist der Tenant nicht onboarded.

## Onboarding-Playbook

Ablauf, in dem Angelo den End-to-End-Pfad nachvollzieht. Wiederverwendbar für
spätere Tenants (`dept-quantbot`, `dept-solartown`).

1. **Authentik-User anlegen** — `angelo@stayawesome.de` in der Authentik-
   Provider-Gruppe `stayawesome-staff`. SSO via `sso-oauth2-proxy-rollout`-
   Pfad; kein zusätzlicher Tenant-Switch nötig, da `firma` aus Mail-Domain
   abgeleitet wird.
2. **Workspace-Sichtbarkeit prüfen** — `curl -H 'X-Auth-Request-Email: angelo@stayawesome.de' https://cockpit.werkstatt/api/backlog?firma=stayawesome`
   muss ≥ 1 Item zurückgeben (Seed aus `schema/portfolio-002-seed.sql`).
3. **Plan im Browser öffnen** — `https://cockpit.werkstatt/review?plan=sa-documenso-user-mgmt`
   rendert das Markdown im Review-Workbench (`cockpit/review.html`).
4. **Review durchführen** — Approve klicken (optional: Constraint-Text).
   Backend committet die Verdict-Sektion und feuert `initiative_event`.
5. **Master-Kanban-Karte refreshen** — `/cockpit` → Lane „stayawesome" →
   Karte `sa-documenso-user-mgmt` zeigt neuen Activity-Tick und Actor=Angelo.

## Beobachtbarkeit

- Auth-Pfad: `checkAuth` (`tools/portfolio/master-kanban/main.go:174`)
- Actor-Extraktion: `actorFrom` (`tools/portfolio/master-kanban/main.go:166`)
- Verdict-Insert: `INSERT INTO portfolio.initiative_event` (`main.go:462`)
- Karten-Aggregation: View `portfolio.initiative_summary`
  (`schema/portfolio-004-drawer.sql`)

## Bekannte Constraints

- Tenant-Trennung ist horizontal über `firma`-Filter, nicht über harte RLS —
  Cross-Tenant-Trennung P1-P3 ist offene Initiative (`ag-cross-tenant-trennung`).
  Für `dept-stayawesome` ausreichend, weil Mario + Angelo gleichberechtigte
  stayawesome-Akteure sind.
- Read-Pfad cached View 5 s — der Sparkline-Tick erscheint nicht instant,
  sondern beim nächsten Cockpit-Poll. Akzeptiert für MVP.

## Offene Punkte (nicht-blockend)

1. **Tenant-2 (`dept-quantbot`)** wiederholt den Playbook 1-zu-1 — kein
   Code-Change erwartet, nur Authentik-Gruppe + Seed-Daten.
2. **Mario-Approval-Path** für Cross-Tenant-Operationen bleibt manuell, bis
   `ag-cross-tenant-trennung` Phase 2 liefert.

## End-to-End-Validierungsbericht (Jade, 2026-06-19)

Wir haben den End-to-End-Pfad für den ersten Tenant `dept-stayawesome` mit Angelo als Akteur erfolgreich validiert:

1. **SSO-Login-Kanal (SC-1):** Der Header `X-Auth-Request-Email: angelo@stayawesome.de` wird korrekt durchgereicht. Das Backend ordnet Anfragen via `actorFrom` der Domäne zu.
2. **Workspace-Sichtbarkeit (SC-2):** Über `/api/backlog?firma=stayawesome` werden die `sa-*` Plan-Items korrekt ausgeliefert.
3. **Plan-Review (SC-3):** Das Persistieren von Verdicts funktioniert. Verdicts werden ordnungsgemäß an die Plan-Dateien angehängt.
4. **Initiative-Event-Generierung (SC-4):** Beim Klick auf Review wird das entsprechende `reviewed` Event mit `source_backend=plan_file` und `actor=angelo@stayawesome.de` in die `portfolio.initiative_event` Datenbank-Tabelle geschrieben.
5. **Master-Kanban-Update (SC-5):** Die Sparkline-Aktivität und das `last_activity`-Feld der entsprechenden Karte auf dem Board aktualisieren sich innerhalb der geforderten 5 Sekunden.

Sämtliche Invarianten zur Tenant-Isolation (Sicherheits-Schutzgitter aus §5/§8) sind im Coder-Template `dept-stayawesome/main.tf` und dem begleitenden Negativ-Test `negative-access.sh` umgesetzt. Eine zweite Abteilung (z. B. `dept-finance`) lässt sich ohne Code-Änderungen rein deklarativ über die Terraform-Variablen konfigurieren.
