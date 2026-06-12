-- Plan-Item-Baum unter Initiativen (PRD solartown-production-lane P4.1)
-- Dokumente bleiben in git; hier nur Projektion: wer hängt unter wem.

CREATE TABLE IF NOT EXISTS portfolio.plan_item (
  id            text PRIMARY KEY,                -- <firma-präfix>-<slug>
  initiative_id text NOT NULL REFERENCES portfolio.initiative(id) ON DELETE CASCADE,
  parent_id     text REFERENCES portfolio.plan_item(id) ON DELETE SET NULL,
  slug          text NOT NULL,
  layer         text,                            -- vision | roadmap | prd | epic | …
  status        text,                            -- draft | review | approved | …
  title         text,
  repo          text,                            -- lokale Repo-Wurzel
  path          text NOT NULL,                   -- absoluter File-Pfad
  updated_at    timestamptz DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_plan_item_initiative ON portfolio.plan_item (initiative_id);
CREATE INDEX IF NOT EXISTS idx_plan_item_parent     ON portfolio.plan_item (parent_id);
