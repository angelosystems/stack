-- portfolio-011-sage.sql — vk-Sage Workspace-Steward Event Kind
--
-- Erweitert initiative_event um 'sage_action' Kind
-- und 'sage' Source-Backend für die persistierten Sage-Klassifikationen und -Heilungen.
--
-- Idempotent — re-run sicher.

ALTER TABLE portfolio.initiative_event DROP CONSTRAINT IF EXISTS initiative_event_kind_check;
ALTER TABLE portfolio.initiative_event ADD CONSTRAINT initiative_event_kind_check
  CHECK (kind = ANY (ARRAY[
    'created', 'moved', 'edited', 'linked', 'unlinked', 'activity',
    'stage_proposed', 'completed', 'commented', 'archived', 'dispatched',
    'deployed', 'workspace_started', 'ai_message', 'ai_action', 'sage_action'
  ]));

ALTER TABLE portfolio.initiative_event DROP CONSTRAINT IF EXISTS initiative_event_source_backend_check;
ALTER TABLE portfolio.initiative_event ADD CONSTRAINT initiative_event_source_backend_check
  CHECK (source_backend = ANY (ARRAY[
    'vk', 'solartown', 'github', 'plan_file', 'master', 'coder', 'sage'
  ]));

-- vk-Sage tables for atomic lease, compare-and-set and retry budget tracking.
CREATE TABLE IF NOT EXISTS portfolio.sage_lease (
    bead_id text NOT NULL PRIMARY KEY,
    locked_until timestamp with time zone NOT NULL,
    locked_by text NOT NULL,
    heal_counter integer NOT NULL DEFAULT 0,
    updated_at timestamp with time zone NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS portfolio.sage_heal_count (
    bead_id text NOT NULL PRIMARY KEY,
    heal_count integer NOT NULL DEFAULT 0,
    escalated boolean NOT NULL DEFAULT false,
    updated_at timestamp with time zone DEFAULT now()
);

CREATE TABLE IF NOT EXISTS portfolio.sage_status (
    id text NOT NULL PRIMARY KEY,
    last_run timestamp with time zone NOT NULL,
    status text NOT NULL,
    error_message text,
    updated_at timestamp with time zone DEFAULT now()
);

-- Insert initial row if not exists
INSERT INTO portfolio.sage_status (id, last_run, status, error_message)
VALUES ('sage-steward', now(), 'healthy', NULL)
ON CONFLICT (id) DO NOTHING;

INSERT INTO portfolio.sage_status (id, last_run, status, error_message)
VALUES ('kanban-flow-manager', now(), 'healthy', NULL)
ON CONFLICT (id) DO NOTHING;
