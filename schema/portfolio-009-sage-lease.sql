-- portfolio-009-sage-lease.sql — Sage Lease und Heal Counter
--
-- 1) portfolio.sage_lease: Atomarer Claim/Lock + per-Bead-Lease
--
-- Idempotent — re-run sicher.

CREATE TABLE IF NOT EXISTS portfolio.sage_lease (
  bead_id       text PRIMARY KEY,
  locked_until  timestamptz NOT NULL,
  locked_by     text NOT NULL,
  heal_counter  integer DEFAULT 0 NOT NULL,
  updated_at    timestamptz DEFAULT now() NOT NULL
);
