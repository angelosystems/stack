-- portfolio-009-sage.sql — vk-Sage Event Kinds and Source Backend
--
-- Erweitert initiative_event um 'sage_action' Kinds und 'sage' Source Backend
-- für die persistierten Sage-Aktionen auf den Initiatives.
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
