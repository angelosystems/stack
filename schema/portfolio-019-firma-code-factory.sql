-- portfolio-019 — firma-Konsolidierung: solartown + stack -> code-factory
-- PRD: master-kanban-firma-code-factory (approved-with-notes, 2026-07-10).
-- Die Fabrik bekommt EINE Programm-Zeile; Komponenten werden software:-Tags.
--
-- REIHENFOLGE-RIEGEL: eingangs-gate-renames.sql (F1) MUSS vorher gelaufen sein
-- (deren Fall 5 schreibt firma='stack' und Fall 4 rennt st-angelo-vk-bridge um,
-- dessen software-Tag hier auf die NEUE id zeigt). Guard in Schritt 0.
--
-- BEFUND Constraint-Drift (Delivery WP0): Die Live-DB-Constraints enthielten
-- 'stack' bereits, die Schema-Files 001/006 nicht — die DB war den Files
-- voraus. Diese Migration setzt den kanonischen Endstand in DB UND File-Kette.
--
-- Idempotent: Zweitlauf = no-op gruen. Vorher: pg_dump --schema=portfolio.
\set ON_ERROR_STOP on

BEGIN;

-- 0) Riegel: F1 gelaufen? (mb-master-kanban-build darf nicht mehr existieren)
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM portfolio.initiative WHERE id='mb-master-kanban-build') THEN
    RAISE EXCEPTION 'F1 (eingangs-gate-renames.sql) zuerst ausfuehren — PRD-Riegel';
  END IF;
END $$;

-- 1) Constraints auf Uebergangs-Superset (alt + neu), damit Schritt 2 durchgeht
ALTER TABLE portfolio.initiative    DROP CONSTRAINT IF EXISTS initiative_firma_check;
ALTER TABLE portfolio.initiative    ADD CONSTRAINT initiative_firma_check
  CHECK (firma IN ('stayawesome','quantbot','mariobrain','angeloos','solartown','stack','code-factory'));
ALTER TABLE portfolio.unlinked_item DROP CONSTRAINT IF EXISTS unlinked_item_firma_check;
ALTER TABLE portfolio.unlinked_item ADD CONSTRAINT unlinked_item_firma_check
  CHECK (firma IN ('stayawesome','quantbot','mariobrain','angeloos','solartown','stack','code-factory'));

-- 2a) WP0-Ausnahmen: echte Firmen-Arbeit von der Fabrik-Zeile umhaengen
UPDATE portfolio.initiative SET firma='quantbot'
 WHERE id IN ('st-quantbot-paperclip-rollout','sk-paperclip-stack')
   AND firma IN ('solartown','stack');
UPDATE portfolio.initiative SET firma='stayawesome'
 WHERE id IN ('sk-documenso-werkstatt-migration','sk-fundraising-crm-twenty')
   AND firma IN ('solartown','stack');
UPDATE portfolio.initiative SET firma='angeloos'
 WHERE id='sk-quant-stayawesome-entkopplung'
   AND firma IN ('solartown','stack');

-- 2b) Konsolidierung: Rest der beiden Fabrik-Zeilen -> code-factory
-- (sage_escalation ist eine VIEW ueber initiative_link+initiative — erbt firma
--  aus dem initiative-UPDATE, Befund F1-Lauf 2026-07-11)
UPDATE portfolio.initiative      SET firma='code-factory' WHERE firma IN ('solartown','stack');
UPDATE portfolio.unlinked_item   SET firma='code-factory' WHERE firma IN ('solartown','stack');

-- 2c) software:-Tags je Komponente (WP0-Liste, Delivery Gate 0)
INSERT INTO portfolio.initiative_tag (initiative_id, kind, value) VALUES
  ('st-agent-eval-harness','software','solartown'),
  ('st-dolt-decommission','software','solartown'),
  ('st-dolt-permanent-decommission','software','solartown'),
  ('st-end-to-end-ingest','software','solartown'),
  ('st-pipeline-production-hardening','software','solartown'),
  ('st-plan-lane-execution-fix','software','solartown'),
  ('st-reactor-fixes','software','solartown'),
  ('st-read-only-postgres','software','solartown'),
  ('st-sage-advisor','software','solartown'),
  ('st-sage-advisor-opus-reactor','software','solartown'),
  ('st-smoke-cascade','software','solartown'),
  ('st-solartown-polecat-lane-rebuild','software','solartown'),
  ('st-solartown-production-lane','software','solartown'),
  ('st-staging-mode-c','software','solartown'),
  ('st-town-resilience-hardening','software','solartown'),
  ('st-v15-haertung','software','solartown'),
  ('st-weft-migration','software','solartown'),
  ('st-angelo-vk-dispatch','software','vibe-kanban'),
  ('st-vk-hacker-lane-restauration','software','vibe-kanban'),
  ('st-vk-shared-mcp-stack','software','vibe-kanban'),
  ('sk-vk-sage-workspace-steward','software','vibe-kanban'),
  ('st-mq-auto-merger','software','tester-merger'),
  ('st-bead-native-reviewer','software','tester-merger'),
  ('st-bd-close-code-verify','software','tester-merger'),
  ('sk-capture-completeness','software','master-kanban'),
  ('sk-coding-factory-reconciliation','software','master-kanban'),
  ('sk-deck-operationalisierung','software','master-kanban'),
  ('sk-detox-bulk-ansicht','software','master-kanban'),
  ('sk-kanban-flow-manager','software','master-kanban'),
  ('sk-master-kanban-bead-linkage','software','master-kanban'),
  ('sk-master-kanban-dispatch','software','master-kanban'),
  ('sk-master-kanban-mcp-copilot','software','master-kanban'),
  ('sk-resource-fleet-manager','software','master-kanban'),
  ('sk-tenant-workspaces','software','master-kanban'),
  ('st-promote-completion','software','master-kanban'),
  ('sk-master-kanban-build','software','master-kanban'),
  ('sk-cicd-stack-tooling','software','deployer'),
  ('st-catch-all','software','shared'),
  ('st-catchall','software','shared'),
  ('sk-catch-all','software','shared'),
  ('st-deepwiki-open-auto-doku-gilde-abloesung','software','shared'),
  ('sk-deepwiki-open-auto-doku-gilde-abloesung','software','shared'),
  ('st-statuspage','software','shared')
ON CONFLICT DO NOTHING;

-- 3) Final-Enum: alte Fabrik-Zeilen-Werte raus
ALTER TABLE portfolio.initiative    DROP CONSTRAINT IF EXISTS initiative_firma_check;
ALTER TABLE portfolio.initiative    ADD CONSTRAINT initiative_firma_check
  CHECK (firma IN ('stayawesome','quantbot','mariobrain','angeloos','code-factory'));
ALTER TABLE portfolio.unlinked_item DROP CONSTRAINT IF EXISTS unlinked_item_firma_check;
ALTER TABLE portfolio.unlinked_item ADD CONSTRAINT unlinked_item_firma_check
  CHECK (firma IN ('stayawesome','quantbot','mariobrain','angeloos','code-factory'));

COMMIT;

-- Verifikation (rein lesend; Soll: code-factory=43 [WP0-Gate N: 42 aus Triage
-- + sk-master-kanban-build, das F1-Fall-5 per Retenant NEU auf die
-- Fabrik-Zeile bringt], solartown=0, stack=0)
SELECT firma, count(*) FROM portfolio.initiative GROUP BY firma ORDER BY 2 DESC;
SELECT 'alt-zeilen' AS check, count(*) FROM portfolio.initiative WHERE firma IN ('solartown','stack');
SELECT 'software-tags' AS check, count(*) FROM portfolio.initiative_tag WHERE kind='software';
