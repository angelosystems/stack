-- portfolio-011-sage.sql — Sage Liveness & Status
--

CREATE TABLE IF NOT EXISTS portfolio.sage_status (
  id            text PRIMARY KEY, -- 'sage-steward'
  last_run      timestamptz NOT NULL,
  status        text NOT NULL, -- 'healthy', 'alarm'
  error_message text
);

-- Insert initial row if not exists
INSERT INTO portfolio.sage_status (id, last_run, status, error_message)
VALUES ('sage-steward', now(), 'healthy', NULL)
ON CONFLICT (id) DO NOTHING;
