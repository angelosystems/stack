-- schema/portfolio-010-sage-action.sql
-- Ref: docs/plans/vk-sage-workspace-steward-prd.md
-- Adds 'sage_action' kind to portfolio.initiative_event table CHECK constraint to support vk-Sage actions.

ALTER TABLE portfolio.initiative_event DROP CONSTRAINT IF EXISTS initiative_event_kind_check;
ALTER TABLE portfolio.initiative_event ADD CONSTRAINT initiative_event_kind_check
  CHECK (kind = ANY (ARRAY[
    'created', 'moved', 'edited', 'linked', 'unlinked', 'activity',
    'stage_proposed', 'completed', 'commented', 'archived', 'dispatched',
    'deployed', 'workspace_started', 'ai_message', 'ai_action', 'sage_action'
  ]));
