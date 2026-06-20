-- portfolio-012-sage-escalation.sql — Sage/Steward Escalation
--
-- Fügt die heal_count-Spalte zu portfolio.initiative hinzu und
-- erstellt die sage_escalation_view Sicht für die Sage-Eskalationen.
--
-- Idempotent — re-run sicher.

ALTER TABLE portfolio.initiative ADD COLUMN IF NOT EXISTS heal_count integer NOT NULL DEFAULT 0;

CREATE OR REPLACE VIEW portfolio.sage_escalation_view AS
  SELECT DISTINCT ON (initiative_id)
    id,
    initiative_id,
    kind,
    source_backend,
    payload,
    actor,
    at
  FROM portfolio.initiative_event
  WHERE kind = 'sage_action' AND (payload->>'action') = 'escalate'
  ORDER BY initiative_id, at DESC;
