-- portfolio-012-sage.sql — Persistent Heal Counter on Initiatives
--

ALTER TABLE portfolio.initiative ADD COLUMN IF NOT EXISTS heal_counter integer DEFAULT 0;
