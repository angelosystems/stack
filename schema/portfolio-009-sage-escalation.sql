-- portfolio-009-sage-escalation.sql — Sage Stop-&-Escalate Feature
--
-- Adds support for Sage heal count, escalation state, and the 'sage_action' event kind.
--
-- Idempotent — re-run safe.

ALTER TABLE portfolio.initiative ADD COLUMN IF NOT EXISTS sage_heal_count int DEFAULT 0;
ALTER TABLE portfolio.initiative ADD COLUMN IF NOT EXISTS sage_escalated boolean DEFAULT false;

ALTER TABLE portfolio.initiative_event DROP CONSTRAINT IF EXISTS initiative_event_kind_check;
ALTER TABLE portfolio.initiative_event ADD CONSTRAINT initiative_event_kind_check
  CHECK (kind = ANY (ARRAY[
    'created', 'moved', 'edited', 'linked', 'unlinked', 'activity',
    'stage_proposed', 'completed', 'commented', 'archived', 'dispatched',
    'deployed', 'workspace_started', 'ai_message', 'ai_action', 'sage_action'
  ]));
