-- portfolio-015-manager.sql — Kanban Flow Manager Schema
--
-- Create status, digest and active flags tables for Kanban Flow Manager.

CREATE TABLE IF NOT EXISTS portfolio.manager_status (
    id             text PRIMARY KEY,
    last_run       timestamptz NOT NULL,
    status         text NOT NULL,
    error_message  text
);

CREATE TABLE IF NOT EXISTS portfolio.manager_digest (
    id             text PRIMARY KEY,
    payload        jsonb NOT NULL,
    updated_at     timestamptz DEFAULT now() NOT NULL
);

ALTER TABLE portfolio.initiative_event DROP CONSTRAINT IF EXISTS initiative_event_kind_check;
ALTER TABLE portfolio.initiative_event ADD CONSTRAINT initiative_event_kind_check
  CHECK (kind = ANY (ARRAY[
    'created', 'moved', 'edited', 'linked', 'unlinked', 'activity',
    'stage_proposed', 'completed', 'commented', 'archived', 'dispatched',
    'deployed', 'workspace_started', 'ai_message', 'ai_action', 'sage_action',
    'manager_flag'
  ]));
