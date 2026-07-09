-- W2 Renames (master-kanban-eingangs-gate-prd) — je Karte eine Transaktion.
-- Muster: Kopie mit neuer id, Referenzen umhängen, alte Zeile entfernen.
\set ON_ERROR_STOP on

-- 1) sa-sa-deploy-stufen → sa-deploy-stufen
BEGIN;
INSERT INTO portfolio.initiative
SELECT (jsonb_populate_record(null::portfolio.initiative, to_jsonb(i) || jsonb_build_object('id','sa-deploy-stufen'))).*
FROM portfolio.initiative i WHERE i.id='sa-sa-deploy-stufen';
UPDATE portfolio.deployments      SET initiative_id='sa-deploy-stufen' WHERE initiative_id='sa-sa-deploy-stufen';
UPDATE portfolio.initiative_event SET initiative_id='sa-deploy-stufen' WHERE initiative_id='sa-sa-deploy-stufen';
UPDATE portfolio.initiative_link  SET initiative_id='sa-deploy-stufen' WHERE initiative_id='sa-sa-deploy-stufen';
UPDATE portfolio.initiative_tag   SET initiative_id='sa-deploy-stufen' WHERE initiative_id='sa-sa-deploy-stufen';
UPDATE portfolio.sage_escalation  SET initiative_id='sa-deploy-stufen' WHERE initiative_id='sa-sa-deploy-stufen';
UPDATE portfolio.plan_item        SET initiative_id='sa-deploy-stufen' WHERE initiative_id='sa-sa-deploy-stufen';
INSERT INTO portfolio.plan_item
SELECT (jsonb_populate_record(null::portfolio.plan_item, to_jsonb(pi) || jsonb_build_object('id','sa-deploy-stufen','slug','deploy-stufen'))).*
FROM portfolio.plan_item pi WHERE pi.id='sa-sa-deploy-stufen';
UPDATE portfolio.plan_item SET parent_id='sa-deploy-stufen' WHERE parent_id='sa-sa-deploy-stufen';
DELETE FROM portfolio.plan_item  WHERE id='sa-sa-deploy-stufen';
DELETE FROM portfolio.initiative WHERE id='sa-sa-deploy-stufen';
COMMIT;

-- 2) sa-sa-deployment-platform → sa-deployment-platform
BEGIN;
INSERT INTO portfolio.initiative
SELECT (jsonb_populate_record(null::portfolio.initiative, to_jsonb(i) || jsonb_build_object('id','sa-deployment-platform'))).*
FROM portfolio.initiative i WHERE i.id='sa-sa-deployment-platform';
UPDATE portfolio.deployments      SET initiative_id='sa-deployment-platform' WHERE initiative_id='sa-sa-deployment-platform';
UPDATE portfolio.initiative_event SET initiative_id='sa-deployment-platform' WHERE initiative_id='sa-sa-deployment-platform';
UPDATE portfolio.initiative_link  SET initiative_id='sa-deployment-platform' WHERE initiative_id='sa-sa-deployment-platform';
UPDATE portfolio.initiative_tag   SET initiative_id='sa-deployment-platform' WHERE initiative_id='sa-sa-deployment-platform';
UPDATE portfolio.sage_escalation  SET initiative_id='sa-deployment-platform' WHERE initiative_id='sa-sa-deployment-platform';
UPDATE portfolio.plan_item        SET initiative_id='sa-deployment-platform' WHERE initiative_id='sa-sa-deployment-platform';
INSERT INTO portfolio.plan_item
SELECT (jsonb_populate_record(null::portfolio.plan_item, to_jsonb(pi) || jsonb_build_object('id','sa-deployment-platform','slug','deployment-platform'))).*
FROM portfolio.plan_item pi WHERE pi.id='sa-sa-deployment-platform';
UPDATE portfolio.plan_item SET parent_id='sa-deployment-platform' WHERE parent_id='sa-sa-deployment-platform';
DELETE FROM portfolio.plan_item  WHERE id='sa-sa-deployment-platform';
DELETE FROM portfolio.initiative WHERE id='sa-sa-deployment-platform';
COMMIT;

-- 3) sa-sa-mews-finance-reporting → sa-mews-finance-reporting (+ Kind-Item)
BEGIN;
INSERT INTO portfolio.initiative
SELECT (jsonb_populate_record(null::portfolio.initiative, to_jsonb(i) || jsonb_build_object('id','sa-mews-finance-reporting'))).*
FROM portfolio.initiative i WHERE i.id='sa-sa-mews-finance-reporting';
UPDATE portfolio.deployments      SET initiative_id='sa-mews-finance-reporting' WHERE initiative_id='sa-sa-mews-finance-reporting';
UPDATE portfolio.initiative_event SET initiative_id='sa-mews-finance-reporting' WHERE initiative_id='sa-sa-mews-finance-reporting';
UPDATE portfolio.initiative_link  SET initiative_id='sa-mews-finance-reporting' WHERE initiative_id='sa-sa-mews-finance-reporting';
UPDATE portfolio.initiative_tag   SET initiative_id='sa-mews-finance-reporting' WHERE initiative_id='sa-sa-mews-finance-reporting';
UPDATE portfolio.sage_escalation  SET initiative_id='sa-mews-finance-reporting' WHERE initiative_id='sa-sa-mews-finance-reporting';
UPDATE portfolio.plan_item        SET initiative_id='sa-mews-finance-reporting' WHERE initiative_id='sa-sa-mews-finance-reporting';
INSERT INTO portfolio.plan_item
SELECT (jsonb_populate_record(null::portfolio.plan_item, to_jsonb(pi) || jsonb_build_object('id','sa-mews-finance-reporting','slug','mews-finance-reporting'))).*
FROM portfolio.plan_item pi WHERE pi.id='sa-sa-mews-finance-reporting';
UPDATE portfolio.plan_item SET parent_id='sa-mews-finance-reporting' WHERE parent_id='sa-sa-mews-finance-reporting';
DELETE FROM portfolio.plan_item WHERE id='sa-sa-mews-finance-reporting';
INSERT INTO portfolio.plan_item
SELECT (jsonb_populate_record(null::portfolio.plan_item, to_jsonb(pi) || jsonb_build_object('id','sa-mews-prod-reporting-rollout'))).*
FROM portfolio.plan_item pi WHERE pi.id='sa-sa-mews-prod-reporting-rollout';
UPDATE portfolio.plan_item SET parent_id='sa-mews-prod-reporting-rollout' WHERE parent_id='sa-sa-mews-prod-reporting-rollout';
DELETE FROM portfolio.plan_item  WHERE id='sa-sa-mews-prod-reporting-rollout';
DELETE FROM portfolio.initiative WHERE id='sa-sa-mews-finance-reporting';
COMMIT;

-- 4) st-angelo-vk-bridge → st-angelo-vk-dispatch (Capability-Name)
BEGIN;
INSERT INTO portfolio.initiative
SELECT (jsonb_populate_record(null::portfolio.initiative, to_jsonb(i) || jsonb_build_object('id','st-angelo-vk-dispatch','title','Angelo-VK-Dispatch'))).*
FROM portfolio.initiative i WHERE i.id='st-angelo-vk-bridge';
UPDATE portfolio.deployments      SET initiative_id='st-angelo-vk-dispatch' WHERE initiative_id='st-angelo-vk-bridge';
UPDATE portfolio.initiative_event SET initiative_id='st-angelo-vk-dispatch' WHERE initiative_id='st-angelo-vk-bridge';
UPDATE portfolio.initiative_link  SET initiative_id='st-angelo-vk-dispatch' WHERE initiative_id='st-angelo-vk-bridge';
UPDATE portfolio.initiative_tag   SET initiative_id='st-angelo-vk-dispatch' WHERE initiative_id='st-angelo-vk-bridge';
UPDATE portfolio.sage_escalation  SET initiative_id='st-angelo-vk-dispatch' WHERE initiative_id='st-angelo-vk-bridge';
UPDATE portfolio.plan_item        SET initiative_id='st-angelo-vk-dispatch' WHERE initiative_id='st-angelo-vk-bridge';
INSERT INTO portfolio.plan_item
SELECT (jsonb_populate_record(null::portfolio.plan_item, to_jsonb(pi) || jsonb_build_object('id','st-angelo-vk-dispatch','slug','angelo-vk-dispatch'))).*
FROM portfolio.plan_item pi WHERE pi.id='st-angelo-vk-bridge';
UPDATE portfolio.plan_item SET parent_id='st-angelo-vk-dispatch' WHERE parent_id='st-angelo-vk-bridge';
DELETE FROM portfolio.plan_item  WHERE id='st-angelo-vk-bridge';
DELETE FROM portfolio.initiative WHERE id='st-angelo-vk-bridge';
COMMIT;

-- 5) D4: mb-master-kanban-build → sk-master-kanban-build (RETENANT stack)
BEGIN;
INSERT INTO portfolio.initiative
SELECT (jsonb_populate_record(null::portfolio.initiative, to_jsonb(i) || jsonb_build_object('id','sk-master-kanban-build','firma','stack'))).*
FROM portfolio.initiative i WHERE i.id='mb-master-kanban-build';
UPDATE portfolio.deployments      SET initiative_id='sk-master-kanban-build' WHERE initiative_id='mb-master-kanban-build';
UPDATE portfolio.initiative_event SET initiative_id='sk-master-kanban-build' WHERE initiative_id='mb-master-kanban-build';
UPDATE portfolio.initiative_link  SET initiative_id='sk-master-kanban-build' WHERE initiative_id='mb-master-kanban-build';
UPDATE portfolio.initiative_tag   SET initiative_id='sk-master-kanban-build' WHERE initiative_id='mb-master-kanban-build';
UPDATE portfolio.sage_escalation  SET initiative_id='sk-master-kanban-build' WHERE initiative_id='mb-master-kanban-build';
UPDATE portfolio.plan_item        SET initiative_id='sk-master-kanban-build' WHERE initiative_id='mb-master-kanban-build';
DELETE FROM portfolio.initiative WHERE id='mb-master-kanban-build';
-- Tag-Achse nachziehen: code-fabrik ⇒ firma-Tag 'shared' statt 'personal'
DELETE FROM portfolio.initiative_tag WHERE initiative_id='sk-master-kanban-build' AND kind='firma' AND value='personal';
INSERT INTO portfolio.initiative_tag (initiative_id, kind, value) VALUES ('sk-master-kanban-build','firma','shared') ON CONFLICT DO NOTHING;
COMMIT;

-- 6) D3: Biz-Karten ohne Code → Notion, Karte archiviert mit Verweis
BEGIN;
UPDATE portfolio.initiative SET archived_at=now(),
  description = COALESCE(description,'') || E'\n\n[D3 master-kanban-eingangs-gate 2026-07-09] Biz-Prozess ohne Code → weiterverfolgt als Notion-Menschen-Task (Stay-Awesome-Backlog); Board-Karte archiviert.'
WHERE id IN ('sa-markthalle-buergschaft','sa-muse-klaerung') AND archived_at IS NULL;
COMMIT;
