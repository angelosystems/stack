-- portfolio-012-cooldown-heartbeat.sql — Cooldown and Flow Manager Status
--

CREATE TABLE IF NOT EXISTS portfolio.proposal_cooldown (
  bead_id      text PRIMARY KEY,
  rejected_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS portfolio.manager_status (
  id            text PRIMARY KEY, -- 'flow-manager'
  last_run      timestamptz NOT NULL,
  status        text NOT NULL, -- 'healthy', 'alarm'
  error_message text
);

-- Insert initial row if not exists
INSERT INTO portfolio.manager_status (id, last_run, status, error_message)
VALUES ('flow-manager', now(), 'healthy', NULL)
ON CONFLICT (id) DO NOTHING;
