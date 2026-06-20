-- portfolio-012-sage-lease.sql — Sage Lease and Heal Counters
--

CREATE TABLE IF NOT EXISTS portfolio.sage_leases (
  workspace_id  text PRIMARY KEY,
  bead_id       text,
  locked_at     timestamptz NOT NULL DEFAULT now(),
  expires_at    timestamptz NOT NULL,
  owner         text NOT NULL
);

CREATE TABLE IF NOT EXISTS portfolio.sage_heals (
  bead_id       text PRIMARY KEY,
  heal_count    int NOT NULL DEFAULT 0,
  last_healed_at timestamptz NOT NULL DEFAULT now()
);
