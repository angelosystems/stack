-- portfolio-004-drawer.sql — Karten-Detail-Drawer (Side-Peek) im Cockpit
-- Angewendet auf mario_brain (:5434) am 2026-06-12.
--
-- 1) description: Karten-Seiteninhalt (Notion-Stil), editierbar via /api/update
-- 2) initiative_summary: liefert description mit aus (Spalte hinten angehängt,
--    damit CREATE OR REPLACE ohne DROP funktioniert)
-- 3) Event-Kinds: 'commented' (/api/comment) + 'archived' (/api/archive)

ALTER TABLE portfolio.initiative ADD COLUMN IF NOT EXISTS description text;

CREATE OR REPLACE VIEW portfolio.initiative_summary AS
 SELECT id, firma, stage, title, status_dot, wip_pinned, primary_backend,
        created_at, updated_at, archived_at,
        COALESCE(( SELECT count(*) FROM portfolio.initiative_link l
                   WHERE l.initiative_id = i.id AND l.kind = 'bead'), 0::bigint) AS bead_count,
        COALESCE(( SELECT count(*) FROM portfolio.initiative_link l
                   WHERE l.initiative_id = i.id AND l.kind = 'vk_workspace'), 0::bigint) AS vk_count,
        COALESCE(( SELECT count(*) FROM portfolio.initiative_link l
                   WHERE l.initiative_id = i.id AND l.kind = 'github_pr'), 0::bigint) AS pr_count,
        COALESCE(( SELECT count(*) FROM portfolio.initiative_link l
                   WHERE l.initiative_id = i.id AND l.kind = 'plan_file'), 0::bigint) AS plan_count,
        ( SELECT max(e.at) FROM portfolio.initiative_event e
          WHERE e.initiative_id = i.id) AS last_activity,
        description
   FROM portfolio.initiative i
  WHERE archived_at IS NULL;

ALTER TABLE portfolio.initiative_event DROP CONSTRAINT initiative_event_kind_check;
ALTER TABLE portfolio.initiative_event ADD CONSTRAINT initiative_event_kind_check
  CHECK (kind = ANY (ARRAY['created','moved','edited','linked','unlinked','activity',
                           'stage_proposed','completed','commented','archived']));
