-- portfolio-008-mcp.sql — MCP Copilot Event Kinds
--
-- Erweitert initiative_event um 'ai_message' und 'ai_action' Kinds
-- für die persistierten Chat-Gespräche und KI-Aktionen.
--
-- Idempotent — re-run sicher.

ALTER TABLE portfolio.initiative_event DROP CONSTRAINT IF EXISTS initiative_event_kind_check;
ALTER TABLE portfolio.initiative_event ADD CONSTRAINT initiative_event_kind_check
  CHECK (kind = ANY (ARRAY[
    'created', 'moved', 'edited', 'linked', 'unlinked', 'activity',
    'stage_proposed', 'completed', 'commented', 'archived', 'dispatched',
    'deployed', 'workspace_started', 'ai_message', 'ai_action'
  ]));
