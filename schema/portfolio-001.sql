-- Master-Kanban Portfolio-Schema
-- Tenant: mario-brain (eigene Postgres :5434, getrennt von solartown)
-- Ref: vault/projects/master-kanban-plan.md Stage 4

CREATE SCHEMA IF NOT EXISTS portfolio;

CREATE TABLE IF NOT EXISTS portfolio.initiative (
  id              text PRIMARY KEY,
  firma           text NOT NULL CHECK (firma IN ('stayawesome','solartown','quantbot','mariobrain','angeloos')),
  stage           text NOT NULL CHECK (stage IN ('idea','now','soon','watching','done')),
  title           text NOT NULL,
  status_dot      text,
  wip_pinned      boolean DEFAULT false,
  primary_backend text,
  created_at      timestamptz DEFAULT now(),
  updated_at      timestamptz DEFAULT now(),
  archived_at     timestamptz
);

CREATE TABLE IF NOT EXISTS portfolio.initiative_link (
  initiative_id   text NOT NULL REFERENCES portfolio.initiative(id) ON DELETE CASCADE,
  kind            text NOT NULL CHECK (kind IN ('bead','vk_workspace','github_pr','plan_file')),
  ref             text NOT NULL,
  added_at        timestamptz DEFAULT now(),
  PRIMARY KEY (initiative_id, kind, ref)
);

CREATE TABLE IF NOT EXISTS portfolio.initiative_event (
  id              bigserial PRIMARY KEY,
  initiative_id   text NOT NULL REFERENCES portfolio.initiative(id) ON DELETE CASCADE,
  kind            text NOT NULL CHECK (kind IN ('created','moved','edited','linked','unlinked','activity','stage_proposed','completed')),
  source_backend  text NOT NULL CHECK (source_backend IN ('vk','solartown','github','plan_file','master')),
  from_stage      text,
  to_stage        text,
  payload         jsonb,
  actor           text,
  at              timestamptz DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_event_initiative_at ON portfolio.initiative_event (initiative_id, at DESC);
CREATE INDEX IF NOT EXISTS idx_event_source_at     ON portfolio.initiative_event (source_backend, at DESC);
CREATE INDEX IF NOT EXISTS idx_link_kind_ref       ON portfolio.initiative_link (kind, ref);

CREATE OR REPLACE VIEW portfolio.initiative_summary AS
  SELECT
    i.*,
    COALESCE((SELECT count(*) FROM portfolio.initiative_link l WHERE l.initiative_id = i.id AND l.kind = 'bead'), 0)         AS bead_count,
    COALESCE((SELECT count(*) FROM portfolio.initiative_link l WHERE l.initiative_id = i.id AND l.kind = 'vk_workspace'), 0) AS vk_count,
    COALESCE((SELECT count(*) FROM portfolio.initiative_link l WHERE l.initiative_id = i.id AND l.kind = 'github_pr'), 0)    AS pr_count,
    COALESCE((SELECT count(*) FROM portfolio.initiative_link l WHERE l.initiative_id = i.id AND l.kind = 'plan_file'), 0)    AS plan_count,
    (SELECT max(at) FROM portfolio.initiative_event e WHERE e.initiative_id = i.id) AS last_activity
  FROM portfolio.initiative i
  WHERE i.archived_at IS NULL;

-- Trigger: NOTIFY on stage change (für edge-triggered Cockpit-Refresh)
CREATE OR REPLACE FUNCTION portfolio.notify_stage_change() RETURNS trigger AS $$
BEGIN
  IF TG_OP = 'UPDATE' AND OLD.stage IS DISTINCT FROM NEW.stage THEN
    PERFORM pg_notify('portfolio_stage_change',
      json_build_object('id', NEW.id, 'firma', NEW.firma, 'from', OLD.stage, 'to', NEW.stage)::text);
    INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, from_stage, to_stage, actor)
      VALUES (NEW.id, 'moved', 'master', OLD.stage, NEW.stage, current_user);
  END IF;
  NEW.updated_at = now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_initiative_stage_change ON portfolio.initiative;
CREATE TRIGGER trg_initiative_stage_change
  BEFORE UPDATE ON portfolio.initiative
  FOR EACH ROW EXECUTE FUNCTION portfolio.notify_stage_change();
