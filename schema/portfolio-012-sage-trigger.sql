-- portfolio-012-sage-trigger.sql — vk-Sage Event Trigger
--
-- Erstellt den PostgreSQL-Trigger, der bei jedem neuen Event im System,
-- das von dem 'vk' (vibe-kanban) Adapter kommt, ein 'portfolio_event_inserted'
-- PG NOTIFY-Signal aussendet. Dies dient als edge-triggered Re-Sync für den
-- im Hintergrund laufenden vk-Sage Workspace-Steward.
--
-- Idempotent — re-run sicher.

CREATE OR REPLACE FUNCTION portfolio.notify_event_inserted() RETURNS trigger AS $$
BEGIN
  IF NEW.source_backend = 'vk' THEN
    PERFORM pg_notify('portfolio_event_inserted',
      json_build_object(
        'initiative_id', NEW.initiative_id,
        'kind', NEW.kind,
        'source_backend', NEW.source_backend
      )::text
    );
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS tr_event_inserted ON portfolio.initiative_event;
CREATE TRIGGER tr_event_inserted
  AFTER INSERT ON portfolio.initiative_event
  FOR EACH ROW EXECUTE FUNCTION portfolio.notify_event_inserted();
