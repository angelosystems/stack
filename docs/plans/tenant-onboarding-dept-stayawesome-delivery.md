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
