---
title: Master-Kanban Eingangs-Gate — Zuordnung wird Code, nicht Disziplin
slug: master-kanban-eingangs-gate
status: in-progress
layer: prd
parent_plan: coding-factory-reconciliation-prd.md
scope: Neuzugänge aufs Board bekommen tier/Tags/Parent-Check deterministisch beim Eingang; Reconciliation-Reste (Backfill, Inbox-Filter, Renames) werden abgeschlossen; VK-Spawns verlinken sich selbst; Zuordnungs-Drift landet im Flow-Manager-Digest.
created: 2026-07-09
review:
  quick: auto
  deep: none
references:
  - coding-factory-reconciliation-prd.md
  - kanban-flow-manager-prd.md
  - /opt/docs/konventionen/plan-konvention.md
  - /opt/docs/konventionen/adr/0011-master-kanban-board-konvention.md
---

# Master-Kanban Eingangs-Gate

## Why (Befund 2026-07-09)

Das Reconciliation-PRD (§8, approved-with-notes) hat das Zielmodell definiert
und Stage 1+2 appliziert — aber der **Eingang** blieb ungeregelt. Folge, per
DB verifiziert:

1. **26 von 121 aktiven Karten ohne `tier`** — alle nach dem Backfill vom
   30.06. entstanden. Der planfile-adapter setzt bei Neuanlage weder tier
   noch Tags; der Mess wächst nach.
2. **Firma-Zeile = Repo-Pfad, blind.** Fabrik-PRDs in `/opt/stack/docs/plans`
   landen in der Zeile „Stack", obwohl sie code-fabrik sind. Die Zeile
   „Stack" soll aus Mario-Sicht nur OSS-Weiterentwicklungen zeigen
   (= tier `library`).
3. **`/opt/code-factory` ist nicht angebunden** — dortige PRDs
   (z. B. paperclip-coding-fabrik) sind auf dem Board unsichtbar.
4. **Kein Überprojekt-Check.** Ohne `parent_plan` entsteht kommentarlos eine
   neue Root-Karte; niemand prüft, ob ein Dach schon existiert.
   Cross-Repo-`parent_plan` erzeugt zusätzlich Dup-Initiativen (Adapter
   nimmt das Firma-Präfix des Kindes).
5. **VK-Sessions verlinken nicht.** Auto-Link ist bewusst aus (Entscheid
   12.06.); der manuelle Schritt wird nie gemacht — genau 1 vk_workspace-Link
   im ganzen Board. `vk-delegate` kennt den Slug bereits (`--kanban-slug`),
   wirft ihn aber nach dem Classifier weg.
6. **Wächter prüfen nur Stagnation, nie Zuordnung.** Sage-Steward läuft,
   Flow-Manager mailt Digest — Firma/Parent/tier/Naming prüft niemand.

## Zielbild

```
PRD-Save ──► planfile-adapter ──► Karte MIT tier + firma-Tag + Parent-Check
VK-Spawn ──► vk-delegate      ──► Workspace-Link auf der Karte (Opt-in via Slug)
Drift    ──► Flow-Manager     ──► Digest-Sektion "Zuordnung" (tier-los, parent-los, Inbox)
Regeln   ──► ADR-0011         ──► eine normative Quelle statt Vision-Extract + Code-Defaults
```

Zuordnung passiert am Eingang durch Code; der Digest meldet nur noch Reste.

## Entscheide (löst §6 des Parent-PRDs auf)

- [x] **D1** „Coding Factory" bleibt UI-Dach/Label über `tier` — **kein**
  eigener firma-Wert. (Bestätigt §8.)
- [x] **D2** `tier`-Spalte + `initiative_tag` sind das Modell — bereits
  materialisiert, wird jetzt am Eingang erzwungen. (Bestätigt §8.)
- [x] **D3** Biz-Items ohne Code (`sa-markthalle-buergschaft`,
  `sa-muse-klaerung`) → **Notion** (Menschen-Aufgaben), Karte archivieren
  mit Verweis. Kein separates Ops-Board.
- [x] **D4** firma `mariobrain` bleibt physisch (UI-Label „personal" reicht);
  `mb-master-kanban-build` wird nach stack/code-fabrik umgehängt.
- [x] **D5** Apply läuft **autonom** mit pg_dump-Backup vor jeder
  Schreibstufe + Bericht (Standing-Decision: Mario raus aus Gates).

## Work-Packages

### W1 — Eingangs-Gate im planfile-adapter

- Frontmatter-Feld `tier: library | code-fabrik | product` (optional).
  Ohne Angabe greift der Repo-Default:
  `/opt/stack`, `/opt/code-factory`, `/root/solartown` → `code-fabrik` ·
  `/opt/quantbot`, `/root/stayawesomeOS`, `/root/mario-brain` → `product`.
- Bei Neuanlage schreibt der Adapter `tier` auf die Initiative plus
  `initiative_tag`: firma-Tag (`product` → Firma, sonst `shared`),
  software-Tag aus optionalem Frontmatter-Feld `software:`.
- `/opt/code-factory=stack` in `PLANFILE_REPOS` aufnehmen (Präfix sk,
  Default-tier code-fabrik).
- Root-Karte ohne `parent_plan` (Wert fehlt, nicht bewusstes `null`):
  Tag `triage:parent-check` — kein Hard-Block, das Board bleibt vollständig.
- Cross-Repo-Parent-Fix: liegt der Parent-Pfad außerhalb der Repo-Wurzel,
  wird die Firma aus der Repo-Liste des Parents aufgelöst statt vom Kind
  geerbt; kein Dup mehr.

### W2 — Backfill + deterministische Apply-Reste (Stage 3-7, Teilmenge)

- Backfill der 26 tier-losen Karten nach W1-Default-Regel + firma-Tags.
- Renames: `sa-sa-mews-finance-reporting`, `sa-sa-deploy-stufen`,
  `sa-sa-deployment-platform` → `sa-`-Präfix einfach; `st-angelo-vk-bridge`
  → Capability-Name.
- D3 ausführen: 2 Biz-Karten → Notion-Verweis + Archiv. D4: RETENANT
  `mb-master-kanban-build`.
- Inbox-Filter: Maschinerie-Transienten (Refinery-/Witness-/Rig-Karten,
  `mol-polecat-work`, Merge-Artefakte) fliegen aus `unlinked_item` raus
  (Filter im Detector, nicht manuell).
- Nicht in Scope: VERIFY-Restage mit Evidenz-Prüfung auf Zielmaschinen —
  bleibt beim Parent-PRD (Karten dafür liefert der W5-Digest).

### W3 — ADR-0011 Board-Konvention

`/opt/docs/konventionen/adr/0011-master-kanban-board-konvention.md`:
Spalten-Semantik mit Eintritts-/Austrittskriterien (idea/now/soon/watching/
done, WIP-Limits), tier-Definitionen mit Beispielliste (library = nur
OSS-Weiterentwicklungen: Paperclip, ActivePieces, Documenso, …),
Zeilen=firma, Pflichten der Session beim PRD-Anlegen (parent_plan bewusst
setzen — Dach-Suche vor Neuanlage), Naming (ID/Slug/Titel), VK-Link-Regel.
CLAUDE.md-Kurzverweis ergänzen.

### W4 — vk-delegate verlinkt beim Spawn

Ist `--kanban-slug` gesetzt (bzw. via `prd`-Subcommand bekannt), legt
vk-delegate nach erfolgreichem Spawn den `initiative_link
kind=vk_workspace` selbst an. Kein blinder Auto-Link im Adapter — der
Entscheid vom 12.06. bleibt; der Delegator weiß die Zuordnung ohnehin.

### W5 — Flow-Manager-Digest: Sektion „Zuordnung"

Der laufende Digest bekommt drei Zähler + Item-Listen: Karten mit
tier NULL, Karten mit `triage:parent-check`, echte Inbox-Reste nach
Transienten-Filter. Kein neuer Service, kein neuer Draht — der
mk-verwalter-Anschluss bleibt eigenes Vorhaben
(ponytail: Digest-Reuse als Ceiling; Upgrade-Pfad = mk-verwalter liest
Board via :7780 und kommentiert issue-getrieben).

## Success-Criteria (messbar)

1. `SELECT count(*) FROM portfolio.initiative WHERE tier IS NULL AND
   archived_at IS NULL` → **0** (nach W2, dauerhaft durch W1).
2. Test-PRD in `/opt/code-factory/docs/plans/` erscheint binnen Sync-Lauf
   als Karte mit tier `code-fabrik` + firma-Tag `shared`.
3. Test-PRD mit `parent_plan` auf Datei in anderem Repo erzeugt **keine**
   Dup-Initiative (Regressionstest im Adapter).
4. Root-Karte ohne parent_plan trägt Tag `triage:parent-check`.
5. `vk-delegate --kanban-slug <slug>`-Spawn erzeugt einen
   vk_workspace-Link (Count vorher/nachher).
6. `unlinked_item` enthält 0 Zeilen mit Refinery/Witness/Rig-Mustern.
7. ADR-0011 existiert; plan-konvention.md verweist darauf.
8. Beide Biz-Karten archiviert mit Notion-Verweis im Beschreibungsfeld.

## Step-Validation-Gates (ADR-0009, max 3 Schritte)

| Schritt | Inhalt | Gate |
|---|---|---|
| 1 | W1 Adapter + W2 SQL | go vet + go build beider Binaries, 0 Fehler; Dawn-Sync-Smoke gegen Prod-DB; SC 1-4, 6 |
| 2 | W3 ADR + Doku | Word-Hygiene-Scan grün; Verweis-Kette prüfbar; SC 7 |
| 3 | W4 vk-delegate + W5 Digest | go build; Spawn-Smoke (Dry-Run) + Digest-Dry-Run; SC 5, 8 |

## Limitations & Risiken

- **Tier-Heuristik ist Repo-grob.** Ein library-PRD im stack-Repo braucht
  explizites `tier: library` im Frontmatter. Root-Cause: Inhalt ist nicht
  deterministisch klassifizierbar; Entscheid: explizites Feld schlägt
  Default, Digest fängt Fehlklassifikation.
- **Renames fassen nur IDs auf dem Board an**, keine physischen Pfade/Repos
  (Legacy bleibt bis eigene Migration, wie §8 festhält).
- **Der 06-12-Entscheid „kein Auto-Link" bleibt gewahrt**: W4 verlinkt nur,
  wenn der Aufrufer den Slug explizit mitgibt.
- **Live-DB vor jeder Schreibstufe sichern**: pg_dump -Fc des
  portfolio-Schemas nach /opt/backups (D5).

## Reviewer-Verdict — quick (glm-5.2) — 2026-07-09

**Verdict:** `approved-with-notes`

Detaillierter, datenfundierter Plan mit klarem Problem-Beleg, sauberer Scope-Abgrenzung (W2 'Nicht in Scope'), durchdachten Architektur-Entscheiden und reversiblen Risiko-Mitigationen. Alle Phasen haben überprüfbare Done-Kriterien in Kombination mit messbaren Success-Criteria. Ein paar kleine formale Unschärfen bei den Konventions-Bezeichnungen bleiben.

**Struktur:**
- S5-references warn: nicht gefunden: coding-factory-reconciliation-prd.md, kanban-flow-manager-prd.md, /opt/docs/konventionen/adr/0011-master-kanban-board-konvention.md

**Findings:**
- [minor] **Fehlende explizite Done-Kriterien-Abhängigkeit** — Die Step-Validation-Gates definieren die Kriterien teilweise über Success-Criteria-Verweise (z.B. SC 1-4, 6). Das ist funktional völlig korrekt und praktikabel, weicht aber leicht von der strengen Forderung ab, dass jede Phase ihr eigenes, direkt im Arbeitspaket stehendes Done-Kriterium hat.
- [minor] **Abschnittsbezeichnung 'Work-Packages'** — Die Konvention fordert 'Phasen/Arbeitspakete'. Der Plan nutzt 'Work-Packages' und 'Step-Validation-Gates'. Inhaltlich passend und qualitativ hochwertig umgesetzt, nur ein stilistischer/terminologischer Hinweis.
- [minor] **Reversibilität nur implizit bei DB-Migrationen** — Bei W2 (Renames, Retags, D3/D4) wird im Risiko-Abschnitt das pg_dump-Backup als Sicherheitsnetz genannt. Die strikte Reversibilität (z.B. wie ein Rollback eines fehlerhaften Backfills technisch exekutiert wird) ist damit gegeben, wird aber nicht als eigener Schritt definiert.

**Asks:**
- [ ] Stelle sicher, dass bei der finalen Umsetzung die Success-Criteria (SC 1-8) physisch als Checkliste in den jeweiligen Step-Validation-Gates abgehakt werden, um die formale Konvention lückenlos zu erfüllen.
