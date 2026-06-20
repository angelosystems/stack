-- portfolio-005-coder-adapter.sql — Coder-Workspaces → Master-Kanban
--
-- 1) Erweitert source_backend um 'coder' und initiative_link.kind um
--    'coder_workspace' (Workspaces als Initiative-Links).
-- 2) Legt einen scoped Postgres-Role `coder_adapter` an, der ausschließlich
--    in das portfolio-Schema schreiben darf — und dort nur initiative_event
--    INSERTen darf. Initiative/Link/etc. bleiben read-only. Damit kann der
--    Adapter Lifecycle-Events emittieren, ohne den Kanban-Stamm zu mutieren.
--
-- Idempotent — re-run sicher.

-- 1) Erlaubte Werte erweitern -------------------------------------------------

ALTER TABLE portfolio.initiative_event DROP CONSTRAINT IF EXISTS initiative_event_source_backend_check;
ALTER TABLE portfolio.initiative_event ADD CONSTRAINT initiative_event_source_backend_check
  CHECK (source_backend = ANY (ARRAY['vk','solartown','github','plan_file','master','coder']));

ALTER TABLE portfolio.initiative_link DROP CONSTRAINT IF EXISTS initiative_link_kind_check;
ALTER TABLE portfolio.initiative_link ADD CONSTRAINT initiative_link_kind_check
  CHECK (kind = ANY (ARRAY['bead','vk_workspace','github_pr','plan_file','coder_workspace']));

-- 2) Scoped Role -------------------------------------------------------------
-- NOLOGIN: der Service-User erbt die Rolle, Passwort liegt am User, nicht hier.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'coder_adapter') THEN
    CREATE ROLE coder_adapter NOLOGIN;
  END IF;
END$$;

-- Default: nichts. Wir vergeben gezielt.
REVOKE ALL ON ALL TABLES    IN SCHEMA portfolio FROM coder_adapter;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA portfolio FROM coder_adapter;
REVOKE ALL ON SCHEMA portfolio FROM coder_adapter;

GRANT USAGE ON SCHEMA portfolio TO coder_adapter;

-- Schreibrecht ausschließlich auf initiative_event.
GRANT INSERT ON portfolio.initiative_event TO coder_adapter;
-- bigserial → Sequenz braucht USAGE für nextval().
GRANT USAGE  ON SEQUENCE portfolio.initiative_event_id_seq TO coder_adapter;

-- Lesen für Initiative-Lookup (Workspace → initiative_id via Link-Tabelle).
GRANT SELECT ON portfolio.initiative_link TO coder_adapter;
GRANT SELECT ON portfolio.initiative      TO coder_adapter;
