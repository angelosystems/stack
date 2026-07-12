-- portfolio-026: Namenskollision aufgeloest — die Summary-Spalte fuer das
-- Dispatch-Tag heisst dispatch_lane (der /api/initiatives-Handler berechnet
-- bereits ein Feld 'lane' = Bead-Mehrheits-Lane, Default 'untriagiert', das
-- die View-Spalte im JSON ueberschrieb). DROP+CREATE in EINER Transaktion
-- (CREATE OR REPLACE kann nicht umbenennen). Basis = 024-Stand wortgleich.

BEGIN;
DROP VIEW portfolio.initiative_summary;
CREATE VIEW portfolio.initiative_summary AS
SELECT id,
   firma,
   stage,
   title,
   status_dot,
   wip_pinned,
   primary_backend,
   created_at,
   updated_at,
   archived_at,
   COALESCE(( SELECT count(*) AS count
          FROM portfolio.initiative_link l
         WHERE l.initiative_id = i.id AND l.kind = 'bead'::text), 0::bigint) AS bead_count,
   COALESCE(( SELECT count(*) AS count
          FROM portfolio.initiative_link l
         WHERE l.initiative_id = i.id AND l.kind = 'vk_workspace'::text), 0::bigint) AS vk_count,
   COALESCE(( SELECT count(*) AS count
          FROM portfolio.initiative_link l
         WHERE l.initiative_id = i.id AND l.kind = 'github_pr'::text), 0::bigint) AS pr_count,
   COALESCE(( SELECT count(*) AS count
          FROM portfolio.initiative_link l
         WHERE l.initiative_id = i.id AND l.kind = 'plan_file'::text), 0::bigint) AS plan_count,
   ( SELECT max(e.at) AS max
          FROM portfolio.initiative_event e
         WHERE e.initiative_id = i.id) AS last_activity,
   description,
   tier,
   COALESCE(( SELECT json_agg(t.value ORDER BY t.value) AS json_agg
          FROM portfolio.initiative_tag t
         WHERE t.initiative_id = i.id AND t.kind = 'firma'::text), '[]'::json) AS firmas,
   COALESCE(( SELECT json_agg(t.value ORDER BY t.value) AS json_agg
          FROM portfolio.initiative_tag t
         WHERE t.initiative_id = i.id AND t.kind = 'software'::text), '[]'::json) AS softwares,
   deploy_state,
   live_version,
   live_sha,
   beads_closed,
   beads_total,
   -- Ampel: juengster Deploy-Stand je Stufe, Format '<status>@<version>'
   ( SELECT d.status || '@' || COALESCE(NULLIF(d.version,''), substr(d.git_sha,1,7))
          FROM portfolio.deployments d
         WHERE d.initiative_id = i.id AND d.environment = 'staging'
         ORDER BY d.deployed_at DESC LIMIT 1) AS staging_state,
   ( SELECT max(d.deployed_at)
          FROM portfolio.deployments d
         WHERE d.initiative_id = i.id AND d.environment = 'staging') AS staging_at,
   ( SELECT d.status || '@' || COALESCE(NULLIF(d.version,''), substr(d.git_sha,1,7))
          FROM portfolio.deployments d
         WHERE d.initiative_id = i.id AND d.environment IN ('prod','prod-mvp')
         ORDER BY d.deployed_at DESC LIMIT 1) AS prod_state,
   ( SELECT max(d.deployed_at)
          FROM portfolio.deployments d
         WHERE d.initiative_id = i.id AND d.environment IN ('prod','prod-mvp')) AS prod_at,
   ( SELECT t.value FROM portfolio.initiative_tag t
          WHERE t.initiative_id = i.id AND t.kind = 'lane' LIMIT 1) AS dispatch_lane,
   parent_id,
   ( SELECT count(*) FROM portfolio.initiative k
          WHERE k.parent_id = i.id AND k.archived_at IS NULL) AS kinder_count
  FROM portfolio.initiative i
 WHERE archived_at IS NULL;

COMMIT;

COMMIT;
