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
  deterministisch klassifizierbar; Entscheid (per Panel-Ask 1 präzisiert):
  Default-Klassifikationen tragen `tier-source=default` und laufen als
  eigener Digest-Zähler — Fehlklassifikation ist damit sichtbares
  Triage-Material, kein stilles Restrisiko. Explizites Feld schlägt
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

## Reviewer-Verdict — deep-tech (spec-panel critique) — 2026-07-09T17:25:41Z

- **Depth:** deep-tech (spec-panel critique)
- **Verdict:** advisory
- **Plan-Commit:** 6d5631f

### Findings

# /sc:spec-panel — Critique Mode · Focus: requirements, architecture

**Panel:** Wiegers (Lead Requirements), Adzic, Cockburn · Fowler (Lead Architecture), Nygard, Newman, Hohpe

**Gesamturteil vorab:** Starkes, datenfundiertes PRD — der Befund ist per DB belegt, Scope-Grenzen sind explizit, Entscheide dokumentiert. Der Panel-Hauptbefund ist ein **Konsistenz-Widerspruch**: Die Limitations-Sektion verspricht „Digest fängt Fehlklassifikation", aber der W5-Digest wie spezifiziert kann genau das nicht sehen. Dazu fehlen mehrere Lebenszyklus- und Fehlpfad-Definitionen an den neuen Schreibwegen.

```
quality_assessment:
  overall_score:        7.8/10
  requirements_quality: 7.5/10
  architecture_clarity: 7.6/10
  testability_score:    8.2/10
  consistency_score:    6.5/10   # Limitations ↔ W5-Widerspruch
```

---

## === REQUIREMENTS ANALYSIS ===

**KARL WIEGERS — Requirements Quality Assessment:**

❌ **CRITICAL: Limitations-Sektion und W5 widersprechen sich — Fehlklassifikation ist für den Digest unsichtbar.**
Die Limitation sagt: „explizites Feld schlägt Default, **Digest fängt Fehlklassifikation**." W5 zählt aber nur drei Dinge: tier NULL, `triage:parent-check`, Inbox-Reste. Ein library-PRD in `/opt/stack`, das den Default `code-fabrik` erbt, hat einen **falschen, aber gesetzten** tier — kein Zähler schlägt an. Das deklarierte Sicherheitsnetz für die selbst eingestandene Heuristik-Schwäche existiert nicht.
📝 RECOMMENDATION: `tier_source: default | explicit` auf der Initiative persistieren; W5 um Zähler „tier per Default gesetzt" mit Item-Liste ergänzen. Alternativ die Limitation ehrlich umformulieren: „Fehlklassifikation ist akzeptiertes Restrisiko, nur NULL wird gemeldet."
🎯 PRIORITY: High — der Plan validiert sonst gegen ein Netz, das nicht gespannt ist.
📊 QUALITY IMPACT: +30 % Konsistenz, +25 % operative Sichtbarkeit

⚠️ **MAJOR: Verhalten an den Rändern des Eingangs-Gates unspezifiziert.**
Zwei Trust-Boundary-Fälle fehlen: (a) PRD in einem Repo, das in `PLANFILE_REPOS` steht, aber keinen Default-tier hat (jedes künftig angebundene Repo!) — NULL? Fehler? (b) `tier: quatsch` im Frontmatter — Reject, Warn, Fallback? SC 1 behauptet „dauerhaft 0 NULL", hängt aber genau an Fall (a).
📝 RECOMMENDATION: Regel ins PRD: unbekannter Repo-Default → Tag `triage:tier-check` + Digest; ungültiger tier-Wert → Sync-Warnung mit handlungsleitender Meldung (ACI-Regel 4 der eigenen Guardrails). Beides als Adapter-Testfall.
🎯 PRIORITY: High — betrifft die Dauerhaftigkeit von SC 1.
📊 QUALITY IMPACT: +35 % Robustheit, +20 % Testbarkeit

📌 MINOR: SC 2 „binnen Sync-Lauf" referenziert eine Kadenz, die nirgends im Dokument steht. Ein Satz mit Verweis auf den Dawn-Sync-Timer genügt.

**GOJKO ADZIC — Specification by Example:**

⚠️ **MAJOR: `triage:parent-check` hat keine Exit-Bedingung — der Zähler konvergiert nie.**
Given: Karte mit Tag. When: Session trägt `parent_plan` nach (oder setzt bewusst `null`). Then: …? Niemand entfernt das Tag. Der W5-Zähler wächst monoton, der Digest wird zur Tapete.
📝 RECOMMENDATION: Adapter entfernt das Tag deterministisch beim Re-Sync, sobald der Key im Frontmatter existiert. Als Given/When/Then-Regressionstest neben SC 4 stellen — SC 4 testet nur das Setzen, nie das Lösen.
🎯 PRIORITY: High — ohne Exit-Pfad ist das Triage-Konzept nach vier Wochen wertlos.
📊 QUALITY IMPACT: +40 % Digest-Signalqualität

⚠️ **MAJOR: SC 6 ist zirkulär definiert.**
„0 Zeilen mit Refinery/Witness/Rig-Mustern" — die Muster sind nirgends enumeriert. Der Test misst dann nur, was der Filter ohnehin implementiert. Gefährlicher ist der umgekehrte Fall: ein zu breites Muster filtert eine **echte** Karte still aus der Inbox — dafür gibt es kein Kriterium.
📝 RECOMMENDATION: Musterliste explizit in ADR-0011 (oder Adapter-Config) festschreiben; SC 6 ergänzen um eine Negativ-Probe: „bekannte echte unlinked-Karte überlebt den Filter."
🎯 PRIORITY: Medium
📊 QUALITY IMPACT: +30 % Validierungs-Abdeckung

📌 MINOR: SC 5 misst Link-Count vorher/nachher, prüft aber keine Idempotenz (Spawn-Retry, Doppel-Aufruf) — siehe Hohpes Befund zur Unique-Semantik.

**ALISTAIR COCKBURN — Goals & Actors:**

⚠️ **MAJOR: Die Triage-Schleife hat keinen benannten Akteur.**
Der Titel verspricht „Zuordnung wird Code, nicht Disziplin" — aber genau der Parent-Check bleibt Disziplin: Digest listet, und dann? Wer sucht das Dach, in welchem Zug, mit welchem Zielzustand? Das war exakt der Failure-Mode des Ist-Zustands (Befund 5: „der manuelle Schritt wird nie gemacht").
📝 RECOMMENDATION: Resolutions-Pfad im PRD benennen. Kleiner Code-Schritt mit großem Hebel: Digest-Item liefert Dach-**Kandidaten** gleich mit (Slug-Präfix-/Firma-Match) — dann ist die Restdisziplin ein Ein-Wort-Entscheid statt einer Suche.
🎯 PRIORITY: Medium — Scope-schonend als Digest-Anreicherung machbar.
📊 QUALITY IMPACT: +25 % Wahrscheinlichkeit, dass Triage tatsächlich passiert

📌 MINOR: D3 sagt „→ Notion", W2 sagt „Notion-Verweis + Archiv" — wer **erzeugt** die Notion-Aufgaben? Primärer Akteur (Gaia? Session?) ungenannt.

---

## === ARCHITECTURE ANALYSIS ===

**MARTIN FOWLER — Interface & Configuration Design:**

⚠️ **MAJOR: Repo-Metadaten auf zwei Quellen verteilt.**
`PLANFILE_REPOS` trägt Pfad→Firma+Präfix; die Default-tier-Zuordnung lebt als zweite Liste im Adapter (W1). Jedes neue Repo muss künftig an zwei Stellen konsistent gepflegt werden — das ist genau die Drift-Klasse, die dieses PRD auf Karten-Ebene bekämpft, auf Config-Ebene neu eingebaut.
📝 RECOMMENDATION: Ein Repo-Registry-Eintrag pro Repo: `(path, firma, prefix, default_tier)`. Eine Struktur, eine Quelle; ADR-0011 verweist darauf statt Listen zu duplizieren.
🎯 PRIORITY: Medium — jetzt billig, nach drei weiteren Repos teuer.
📊 QUALITY IMPACT: +30 % Wartbarkeit

📌 MINOR: Cross-Repo-Parent-Fix — Fallback fehlt, wenn der Parent-Pfad in **keiner** Repo-Liste auflösbar ist (Tippfehler, nicht angebundenes Repo). Empfehlung: dann `triage:parent-check` statt stillem Erben vom Kind; sonst kehrt der Dup-Bug durch die Hintertür zurück.

**MICHAEL NYGARD — Failure Modes & Operations:**

⚠️ **MAJOR: W4-Fehlpfad undefiniert — und der entstehende Drift ist unmessbar.**
Spawn gelingt, Link-Insert scheitert (DB weg, Constraint, Netz): Was passiert? Spawn abbrechen wäre falsch; stilles Verschlucken erzeugt exakt die Lücke, die Befund 5 beklagt — und **kein** W5-Zähler misst „Spawn mit Slug, aber ohne Workspace-Link".
📝 RECOMMENDATION: Link-Fehler non-fatal, aber laut: Fehlermeldung im vk-delegate-Output + W5-Zähler „VK-Spawns mit Slug ohne Link" (Abgleich vk-Workspaces ↔ `initiative_link`).
🎯 PRIORITY: High — sonst wiederholt W4 den Ist-Zustand mit besserem Gewissen.
📊 QUALITY IMPACT: +35 % operative Verlässlichkeit von SC 5

⚠️ **MAJOR: Backup ohne geprobten Restore ist Hoffnung, kein Mechanismus.**
D5 sichert per `pg_dump -Fc`, aber der Rückweg ist unspezifiziert: Restore-Kommando, Schema-Scope, und — kritisch — der Dawn-Sync-Timer schreibt während eines Restores munter weiter. Der Quick-Reviewer hat das angerissen; hier die operative Fassung.
📝 RECOMMENDATION: Restore-Prozedur als Dreizeiler ins PRD (Timer stoppen → `pg_restore` Schema-scoped → Timer an) und in Gate 1 einmal dry-runnen.
🎯 PRIORITY: Medium
📊 QUALITY IMPACT: +40 % tatsächliche (statt behaupteter) Reversibilität

📌 MINOR: Gate 1 bündelt W1-Adapter und W2-Backfill, nennt aber die Reihenfolge nicht. Backfill **vor** Adapter-Deploy öffnet das NULL-Fenster erneut (Karten zwischen Backfill und Deploy). Ein Satz genügt: „Adapter deployen, dann Backfill."

**SAM NEWMAN — Evolution & Compatibility:**

⚠️ **MAJOR: Renames ohne Referenz-Sweep.**
`sa-sa-*` → `sa-*` und `st-angelo-vk-bridge` → Capability-Name ändern Board-IDs/Slugs, auf die andere Artefakte zeigen können: `parent_plan` anderer PRDs, bestehende `initiative_link`s, und jeder künftige `vk-delegate --kanban-slug <alter-slug>`-Aufruf aus Muscle-Memory oder Doku. Das PRD prüft nirgends auf dangling references.
📝 RECOMMENDATION: Vor jedem Rename ein SELECT über alle Vorkommen (links, parents, tags); SC ergänzen: „0 dangling references nach W2-Renames." Bei `--kanban-slug` mit unbekanntem Slug: handlungsleitender Fehler statt stillem No-Op.
🎯 PRIORITY: High — Datenintegritäts-Risiko in genau der Schreibstufe, die autonom läuft.
📊 QUALITY IMPACT: +30 % Datenintegrität

📌 MINOR: Wo lebt die tier-Enum kanonisch — DB-CHECK-Constraint, Adapter-Code oder ADR-0011? Ein vierter tier-Wert muss später an **einer** Stelle ergänzt werden. Empfehlung: DB-Constraint als Wahrheit, ADR dokumentiert sie.

**GREGOR HOHPE — Integration & Delivery Guarantees:**

⚠️ **MAJOR: Zwei Writer auf `initiative_link` ohne Eindeutigkeits-Semantik.**
Adapter und vk-delegate schreiben künftig beide Links. Weder Unique-Constraint noch Upsert-Verhalten sind spezifiziert — ein Spawn-Retry oder Doppel-Aufruf produziert Dup-Links, und SC 5 („Count vorher/nachher") würde das sogar als Erfolg zählen.
📝 RECOMMENDATION: Unique-Constraint auf `(initiative_id, kind, target)` + `INSERT … ON CONFLICT DO NOTHING`; SC 5 um den Idempotenz-Fall erweitern (zweiter Aufruf → Count unverändert).
🎯 PRIORITY: Medium — billig jetzt, unangenehm als Datenbereinigung später.
📊 QUALITY IMPACT: +25 % Integrations-Robustheit

---

## === SYNTHESIS — Top-Asks priorisiert ===

Das PRD kann `approved-with-notes` behalten; die Asks sind Edits am PRD + kleine Scope-Ergänzungen in W1/W4/W5, kein Re-Design.

1. **Fehlklassifikation sichtbar machen (W5 ↔ Limitations auflösen).** `tier_source: default|explicit` persistieren und als vierten Digest-Zähler aufnehmen — oder die Limitations-Aussage streichen. Aktuell verspricht das PRD ein Sicherheitsnetz, das die eigene Spezifikation nicht hergibt. *(Wiegers ❌)*

2. **Lebenszyklen der neuen Marker spezifizieren.** (a) `triage:parent-check` wird beim Re-Sync entfernt, sobald der Key existiert; (b) `initiative_link` bekommt Unique-Constraint + idempotenten Insert; (c) W4-Linkfehler ist non-fatal, aber laut, mit Digest-Zähler „Spawn mit Slug ohne Link". Ohne (a) konvergiert der Digest nie, ohne (b)/(c) baut W4 den Ist-Zustand nur leiser nach. *(Adzic, Hohpe, Nygard)*

3. **Referenz-Sweep vor den W2-Renames.** SELECT über parent_plan/links/Slug-Vorkommen vor jedem Rename, SC „0 dangling references" ergänzen — die Renames laufen autonom in einer Schreibstufe, das ist die falsche Stelle für Überraschungen. *(Newman)*

4. **Gate-Ränder definieren.** Unbekannter Repo-Default → `triage:tier-check`; ungültiger `tier:`-Wert → Sync-Warnung mit Fix-Hinweis. Beides als Adapter-Testfall, sonst ist SC 1 „dauerhaft" nicht haltbar. *(Wiegers)*

5. **Repo-Registry konsolidieren.** Eine Struktur `(path, firma, prefix, default_tier)` statt PLANFILE_REPOS plus Code-interner tier-Liste — sonst entsteht auf Config-Ebene die Drift neu, die das PRD auf Karten-Ebene beseitigt. *(Fowler)*

### Asks

See Findings above.

## Panel-Reaktion — 2026-07-09

Umgesetzt in derselben Session (Eskalations-Pfad: Panel → nachschärfen → Quick erneut):

- **Ask 1 (Wiegers ❌, Fehlklassifikation unsichtbar):** UMGESETZT — Tag
  `tier-source=default` bei Default-Klassifikation (Neuanlage), explizites
  `tier:` räumt ab; vierter Digest-Zähler „tier nur per Repo-Default".
  Limitations-Bullet entsprechend präzisiert.
- **Ask 2a (Adzic, parent-check-Exit):** WAR IMPLEMENTIERT, jetzt auch
  spezifiziert: Re-Sync entfernt das Tag, sobald der Key existiert UND der
  deklarierte Parent auflösbar ist; deklariert-aber-kaputt (Fowler-Minor)
  setzt es neu.
- **Ask 2b (Hohpe, Link-Idempotenz):** WAR ERFÜLLT — PK
  `(initiative_id, kind, ref)` + `ON CONFLICT DO NOTHING`; in ADR-0011
  dokumentiert.
- **Ask 2c (Nygard, W4-Fehlpfad):** Teil 1 umgesetzt (Link-Miss non-fatal
  + lauter stderr-Hinweis). Digest-Abgleich „VK-Workspaces ↔ Links" =
  Folge-Arbeit (braucht VK-sqlite-Zugriff im serve; ponytail im Delivery).
- **Ask 3 (Newman, Referenz-Sweep):** UMGESETZT — Sweep- +
  Verifikations-SELECTs (0-rows-Erwartung) im Freigabe-SQL
  `eingangs-gate-renames.sql`; `--kanban-slug` mit unbekanntem Slug liefert
  bereits handlungsleitenden Fehler (cmdLink).
- **Ask 4 (Wiegers, Gate-Ränder):** UMGESETZT — ungültiger `tier:` → laute
  Sync-Warnung + Default; kein auflösbarer Default → tier bleibt NULL +
  `triage:tier-check` + Digest-Zähler.
- **Ask 5 (Fowler, Repo-Registry):** UMGESETZT — `PLANFILE_REPOS` versteht
  `pfad=firma[:tier]`; eine Quelle für Pfad/Firma/Default-tier,
  firmaDefaultTier bleibt Fallback für die 6 Alt-Firmen.
- **Nygard-Restore:** Prozedur: `systemctl stop master-kanban-planfile
  master-kanban-solartown` → `pg_restore -h 127.0.0.1 -p 5434 -U mario
  -d mario_brain --clean --schema=portfolio <dump>` → Services wieder
  starten. (Dry-Run des Restores auf Staging-DB steht aus.)
- **Cockburn (Dach-Kandidaten im Digest), Adzic-Negativ-Probe als
  Testfall:** Folge-Arbeit; Musterliste ist in ADR-0011 kanonisiert.

## Reviewer-Verdict — quick (glm-5.2) — 2026-07-09

**Verdict:** `approved-with-notes`

Das PRD löst ein klar belegtes, strukturelles Problem (Zuordnungs-Drift) und hat eine explizite Scope-Abgrenzung (W2: 'Nicht in Scope'). Alle Arbeitspakete verfügen über überprüfbare Done-Kriterien in Form von messbaren Success-Criteria (SC 1-8) und konkreten Step-Validation-Gates. Architekturentscheidungen sind begründet, Alternatien erkennbar verworfen, und offene Fragen sowie Risiken wurden durch ein tiefgreifendes Panel-Review bereits vollständig adressiert und aufgelöst.

**Struktur:**
- S5-references warn: nicht gefunden: coding-factory-reconciliation-prd.md, kanban-flow-manager-prd.md

**Asks:**
- [ ] Stelle bei der finalen Umsetzung sicher, dass die Success-Criteria physisch als Checkliste in den jeweiligen Step-Validation-Gates abgehakt werden, um die formale Konvention lückenlos zu erfüllen.
