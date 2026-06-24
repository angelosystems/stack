-- portfolio-015-promote-damped.sql — Auto-Promotion Damped Event Kind
--
-- Adds 'promote_damped' kind to portfolio.initiative_event table CHECK constraint
-- to support logging when auto-promotion to 'watching' is damped due to low linkage.

ALTER TABLE portfolio.initiative_event DROP CONSTRAINT IF EXISTS initiative_event_kind_check;
ALTER TABLE portfolio.initiative_event ADD CONSTRAINT initiative_event_kind_check
  CHECK (kind = ANY (ARRAY[
    'created', 'moved', 'edited', 'linked', 'unlinked', 'activity',
    'stage_proposed', 'completed', 'commented', 'archived', 'dispatched',
    'deployed', 'workspace_started', 'ai_message', 'ai_action', 'sage_action',
    'promote_damped'
  ]));
