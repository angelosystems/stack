-- portfolio-006-leak-detector.sql — Leak-Detektor und Status-Tabellen
--
-- 1) portfolio.unlinked_item: Speichert "Arbeit ohne Zuhause" (Beads, vk_workspaces, ungezielt)
-- 2) portfolio.detector_status: Heartbeat und Status des Detektors (Liveness/Vollständigkeit)
--
-- Idempotent — re-run sicher.

CREATE TABLE IF NOT EXISTS portfolio.unlinked_item (
  id            text PRIMARY KEY,
  kind          text NOT NULL CHECK (kind = ANY (ARRAY['bead', 'vk_workspace', 'rig'])),
  title         text NOT NULL,
  firma         text NOT NULL CHECK (firma = ANY (ARRAY['stayawesome', 'solartown', 'quantbot', 'mariobrain', 'angeloos', 'stack'])),
  rig_prefix    text NOT NULL,
  join_key      text,
  discovered_at timestamptz DEFAULT now()
);

CREATE TABLE IF NOT EXISTS portfolio.detector_status (
  id               text PRIMARY KEY,
  last_run         timestamptz NOT NULL,
  status           text NOT NULL,
  unreachable_rigs text[] NOT NULL,
  error_message    text
);
