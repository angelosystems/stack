-- portfolio-004-auto-stage.sql
ALTER TABLE portfolio.initiative ADD COLUMN IF NOT EXISTS stage_locked_by_human boolean DEFAULT false;
