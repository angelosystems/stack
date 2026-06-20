---
title: CI/CD für Stack-Tooling — Deploy-on-Merge-Reactor
slug: cicd-stack-tooling
status: approved-with-notes
layer: prd
parent_plan: /opt/stack/docs/plans/master-kanban.md
scope: Die rechte Hälfte der Pipeline schließen — CI-Gate vor Merge und automatischer Deploy nach Merge (build→staging-smoke→prod→health→rollback), mit Deploy-Status sichtbar auf der Master-Kanban-Karte. Schluss mit "gemerged, aber läuft nicht".
created: 2026-06-14
review:
  quick: auto
  deep: spec-panel
  panel-mode: critique
  panel-focus: [architecture, compliance]
references:
  - /opt/stack/docs/plans/master-kanban.md
  - /opt/stack/docs/plans/master-kanban-bead-linkage-prd.md
  - /opt/stack/tools/portfolio/adapters/solartown/main.go
---

# CI/CD für Stack-Tooling — Deploy-on-Merge-Reactor

## Problem

Die linke Hälfte der Pipeline ist stark: `PRD (Reviewer-Gate) → Bead
(auto-derived) → Polecat baut → MQ → auto-merge`. Die rechte Hälfte fehlt
komplett. Zwei belegte Symptome (beide am 2026-06-14 real beobachtet):

1. **Kein CD nach Merge.** Der Multi-Rig-Linker + Dispatch-Endpoint waren
   gemerged, aber die laufende Binary war **2 Tage alt** — Code lag in git und
   tat nichts. Erst ein manueller Build+Restart schaltete es scharf.
2. **Kein CI-Gate / kein Staging-Smoke.** Der `--link`-Modus shippte mit
   einem falschen DSN-Modell (`<rig>_clean@5433`, existiert nicht) — sichtbar
   **erst beim Ausführen in prod**. Ein Staging-Smoke hätte das vor prod gefangen.

Folge: „gemerged" ≠ „läuft". Der Mensch ist der Deploy-Schritt, und Fehler
zeigen sich erst in prod.

## Ziel

Software-Company-Grade rechte Hälfte, im Stil des bestehenden Stacks
(ein Host werkstatt, Event-Bus, systemd, mq-auto-merger) — **kein** SaaS-CD,
**kein** k8s draufgestülpt:

```
… main → [CI-Gate] → [Artefakt SHA] → [Deploy staging] → [Smoke] → [Promote prod] → [Health] → [Rollback?] → [Deploy-Event → Karte]
         required      build-once       isoliert           pass?     atomar+restart    /api 200   auto bei Miss   sichtbar im Board
         check                                                                                                     
```

## Nicht-Ziele

- Keine externe CD-Plattform (Argo/Spinnaker/Octopus), kein Kubernetes.
- Kein Eingriff in die linke Hälfte (PRD/Bead/Reactor/MQ bleibt wie sie ist).
- Keine Multi-Host-Orchestrierung in Phase 1 (werkstatt-lokal; QuantBots
  Dublin-Pfad bleibt separat).

## Annahmen (vor Bau verifizieren)

- A1: mq-auto-merger emittiert ein Merge-Event (oder ein GitHub-Webhook ist
  verfügbar), an das sich ein Deploy-Reactor edge-getriggert hängen kann.
- A2: Stack-Tooling-Services sind systemd-Units mit Binary unter
  `/opt/stack/bin/` — Deploy = build + atomarer Swap + `systemctl restart`.
- A3: GitHub Actions Runner haben echtes `go`/`node` (Memory: GH-Runner haben
  echtes npm, lokaler npm ist pnpm-Shim).

## Lösung

### L1 — CI-Gate vor Merge
GitHub Actions pro Repo: `go test ./... && go build && lint` (bzw. node-Äquivalent).
Branch-Protection macht den grünen Check zur **Merge-Voraussetzung**; der
mq-auto-merger merged nur bei grünem Required-Check. **Alternative verworfen:**
Tests erst nach Merge laufen lassen — verworfen, weil dann rote Builds bereits
auf main sind (das Symptom, das wir abstellen wollen).

### L2 — Deploy-on-Merge-Reactor (CD-Kern)
Ein kleiner Service auf werkstatt, der auf das Merge-Event hört (A1) und pro
betroffenem Repo ein deklaratives `deploy.sh` fährt:
`build (SHA-getaggt) → atomarer Swap → systemctl restart → Health-Check
(/api 200 + Feature-Smoke) → Rollback auf vorige Binary bei Fehler →
Deploy-Event emittieren`. **Alternative verworfen:** GitHub-Actions-self-hosted-Runner
als Deployer — funktioniert, aber bricht das edge-getriggerte Event-Bus-Muster
des Stacks und doppelt die Runner-Infra; der Reactor passt zum bestehenden Stil.

### L3 — Deploy-Manifest (deklarativ)
`deploy-manifest.yaml`: `repo → [services] → deploy.sh → health-probe`. Eine
Quelle der Wahrheit, welche Komponente wie deployt. Neue deploybare Komponente =
ein Manifest-Eintrag, kein Reactor-Code.

### L4 — Staging-Smoke vor prod
Pro Komponente eine Staging-Instanz (eigener Port/DB), gegen die der Reactor
zuerst deployt + smoke-testet; nur bei Smoke-pass Promote auf prod. Phase-2 für
Komponenten ohne triviale Staging-Instanz; Phase-1 mindestens für master-kanban.

### L5 — Deploy-Status auf der Karte (Observability)
Vierter Adapter `deploy-adapter`: konsumiert Deploy-Events, legt sie als
`initiative_link`/`initiative_event` auf die Karte. Drawer bekommt eine
🚀-Zeile: `deployed <SHA> → <env> · <healthy|rolled-back> · <ts>`. Damit
siehst du Plan→Bead→Code→Merge→Deploy→Health an **einem** Ort.

## Success-Criteria

- SC1: Merge auf main eines Stack-Tooling-Repos löst ohne Menschen einen Deploy
  aus; die laufende Binary trägt danach den gemergten SHA (verifiziert per
  Versions-Endpoint/`strings`), Latenz vom Merge zum Live-Stand im Minutenbereich.
- SC2: Ein absichtlich gebrochener Build/Test wird vom CI-Gate **vor** Merge
  rot und blockiert den mq-auto-merger.
- SC3: Ein Deploy, dessen Health-Check fehlschlägt, rollt automatisch auf die
  vorige Binary zurück und meldet den Fehlschlag als Event (kein Silent-Miss).
- SC4: Staging-Smoke fängt einen Fehler wie den `<rig>_clean`-DSN-Bug **vor**
  prod (reproduzierbar mit genau diesem Bug als Regressions-Smoke).
- SC5: Jede Initiative mit deploybarer Komponente zeigt im Drawer den aktuellen
  Deploy-Status (SHA/env/health).

## Risiken / offene Fragen

- R-A: Merge-Event-Quelle (A1) — emittiert mq-auto-merger schon eins, oder
  braucht es einen GitHub-Webhook-Eintrag? Vor L2 klären.
- R-B: Deploy-Reactor mutiert prod (restart) — muss selbst abgesichert laufen
  (kein offener Trigger; nur auf verifizierte Merge-Events der eigenen Repos).
- R-C: Build auf werkstatt vs Artefakt-Registry — Phase 1 baut auf dem Host
  (einfach); bei Wachstum SHA-getaggte Artefakte in Object Storage (Hetzner).
- R-D: Rollback-Grenzen — DB-Migrationen sind nicht binär-rückrollbar.
  **Phase-1-Regel:** der Reactor deployt nur Komponenten **ohne offene
  Migration** automatisch; ein Deploy, dessen Diff eine Migration enthält
  (Marker: geänderte `migrations/`-Dateien), wird **geblockt** und als
  „needs-human"-Event gemeldet statt blind gefahren. Voll-Migrations-Strategie
  (expand/contract, Feature-Flags) ist Folge-PRD.

## Phasen (Granularität, keine Zeit)

1. **CI-Gate** (Granularität 2) — Actions build+test+lint + Branch-Protection.
   Done wenn SC2 **und** Branch-Protection auf den Stack-Tooling-Repos aktiv ist
   und der mq-auto-merger den Required-Check respektiert (nicht mehr bei rot merged).
2. **Deploy-Reactor + Manifest + Health/Rollback** (Granularität 4) — L2+L3.
   Done wenn SC1+SC3; Phase-1-Reactor blockt Migrations-Deploys (R-D).
3. **Staging-Smoke** (Granularität 3) — L4, **in Phase 1 nur für master-kanban**
   (serve + solartown-adapter), mit dem `<rig>_clean`-DSN-Bug als Regressions-Smoke.
   Weitere Komponenten in Phase 2. Done wenn SC4.
4. **Deploy-Status auf der Karte** (Granularität 2) — L5, deploy-adapter + Drawer.
   Done wenn SC5.

---

> Architektur-Hebel → Plan-Pipeline. Deep-Tech empfohlen (Reactor mutiert prod +
> Rollback-Semantik). Kein Bead vor Quick-Verdict.

## Reviewer-Verdict — quick (glm-5.1) — 2026-06-15

**Verdict:** `approved-with-notes`

Solider PRD-Entwurf mit klarem Problembezug (zwei belegte Symptome), sauberer Scope-Abgrenzung, begründeten Architekturentscheidungen und überprüfbaren Done-Kriterien pro Phase. Keine Konventionsverstöße, insbesondere keine Zeitschätzungen. Ein paar präzisierende Asks zur Schärfung von Reversibilität und Done-Kriterien.

**Findings:**
- [minor] **Reversibilität nur für stateless Binary-Swaps adressiert** — R-D benennt das Limit (kein Rollback bei DB-Migrationen), aber der Plan gibt keinen Hinweis, wie Phase 1 damit umgeht — z.B. ob Deploys mit offenen Migrationen blockiert werden oder ein Feature-Flag erwartet wird.
- [minor] **Done-Kriterium Phase 1 indirekt statt explizit** — Phase 1 verweist auf SC2 (absichtlich gebrochener Build blockiert Merge), aber das Done-Kriterium für Branch-Protection-Setup selbst ist implizit. Klarer wäre: 'Branch-Protection aktiv und mq-auto-merger respektiert Required-Check'.
- [minor] **Staging-Smoke-Scope für Phase 1 nur implizit** — L4 sagt 'Phase-1 mindestens für master-kanban', aber Phase 3-Definition listet nur den DSN-Regressions-Smoke. Es ist nicht klar, ob master-kanban der einzige zu smoke-testende Service in Phase 1 ist.

**Asks:**
- [ ] Präzisiere in R-D oder L2, wie der Reactor mit Deploy-Pfaden umgeht, die nicht binär rückrollbar sind (Migrationen) — mindestens ein klares 'Phase 1 = nur Services ohne Migrations-Pflicht' oder ein Block-Kriterium.
- [ ] Schärfe das Done-Kriterium für Phase 1 so, dass nicht nur SC2 sondern auch die Branch-Protection-Konfiguration als eigenständiges Done-Item explizit genannt wird.
- [ ] Mache explizit, welche Services in Phase 1 Staging-Smoke bekommen (L4 sagt 'mindestens master-kanban' — ist das der einzige in Phase 1?).
