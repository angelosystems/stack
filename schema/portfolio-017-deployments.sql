-- portfolio-017-deployments.sql — Release-Ledger (Code-Fabrik Release-Pipeline, WP3/WP4)
--
-- „Was läuft gerade, welcher Commit?" bekommt eine Tabelle statt Rätselraten aus
-- health.json (PRD code-fabrik-release-pipeline, Diagnose G3/G4):
--   1. portfolio.deployments      — der Ledger: eine Zeile je Deploy-Vorgang.
--   2. portfolio.deploy_state_map — publizierte status→deploy_state-Abbildung
--      (Panel-Minor: Mapping als Tabelle, nicht im Reconciler hartkodiert);
--      rank steuert die Worst-of-Aggregation auf die Karte.
--   3. initiative.deploy_state / live_version / live_sha — denormalisierte
--      Board-Felder (WP4); EINZIGE Schreibquelle ist der Reconciler (D13).
--   4. initiative_summary neu — Live-Definition + die drei neuen Felder.
--
-- Schlüssel-Entscheidungen (PRD-Anker):
--   * Identität der Live-Query ist (service, environment) — D9, ab sofort,
--     nicht erst post-V1.0. MVP: environment konstant 'prod-mvp'.
--   * Idempotenz-Schlüssel ist UNIQUE (service, environment, git_sha) — D11.
--     Im MVP (ein env) erfüllt das wortgleich das WP3-Done-Kriterium „genau
--     eine Ledger-Zeile je (service,git_sha)"; multi-env kollidiert nicht.
--   * deployed_at (DB now()) ist der EINZIGE Ordering-Key; built_at im
--     /version-Vertrag ist Build-Zeit und ordnet nichts.
--   * status='pending' ist die Transactional-Outbox-Zeile (D10). Dieses File
--     erzeugt nur die Struktur; produziert wird das Event vom Merger
--     (Session/PRD Test-Gate), konsumiert vom Deploy-Reaktor (WP5) — hier
--     ausdrücklich NICHT scharfgeschaltet.
--
-- Die View-Neudefinition ersetzt den Live-Stand 1:1 (Spalten bis „softwares"
-- wörtlich aus pg_get_viewdef der Live-DB am 2026-07-06) und hängt die drei
-- Deploy-Felder an. Achtung Drift-Historie: portfolio-014-lane-badges.sql
-- definierte eine `lane`-Spalte, die es LIVE nie gab (der 30.06.-Patch lief
-- an git vorbei, vgl. portfolio-016). DROP+CREATE statt CREATE OR REPLACE,
-- damit das File auf beiden Ausgangszuständen (Scratch-Kette mit 014 wie
-- Live-DB ohne lane) durchläuft. Keine abhängigen Views (geprüft 2026-07-06).
--
-- Idempotent: re-run-sicher (IF NOT EXISTS / ON CONFLICT DO NOTHING / DROP+CREATE).
-- Anwendung: psql -1 -f portfolio-017-deployments.sql  (eine Transaktion).

-- ── 1. Release-Ledger ────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS portfolio.deployments (
  id              bigserial PRIMARY KEY,
  service         text        NOT NULL,             -- 'master-kanban', 'planfile-adapter', …
  probe_kind      text        NOT NULL DEFAULT 'http'
                  CONSTRAINT deployments_probe_kind_check
                  CHECK (probe_kind IN ('http','cli')),   -- Smoke-Oberfläche (D18)
  environment     text        NOT NULL DEFAULT 'prod-mvp',-- Identitäts-Key-Teil (D9)
  version         text        NOT NULL,             -- git describe --tags --always
  git_sha         text        NOT NULL,             -- SHA des Merge-/Deploy-Commits (D12)
  migration_version      text,                      -- Schema-Stand dieses Deploys (D19)
  prev_migration_version text,                      -- Rollback-Ziel-Schema (D19)
  config_sha      text,                             -- Fingerprint /etc/… (D20b; Writer folgt in WP5)
  initiative_id   text REFERENCES portfolio.initiative(id) ON DELETE SET NULL,
  bead_ids        text[],                           -- Owner: Deploy-Reaktor bei Anlage; VK-Fallback leer
  status          text        NOT NULL DEFAULT 'deploying'
                  CONSTRAINT deployments_status_check
                  CHECK (status IN ('pending','deploying','live','errored','rolled_back')),
  owned_by        text,                             -- Lease-Halter, z.B. 'deploy-reactor@stack' (D13)
  owned_until     timestamptz,                      -- Lease-Deadline; Reconciler überspringt geleaste Zeilen
  deployed_at     timestamptz NOT NULL DEFAULT now(), -- EINZIGER Ordering-Key
  deployed_by     text        NOT NULL DEFAULT 'manual', -- owning Actor
  deploy_method   text,                             -- 'deploy-gt' | 'mk-deploy-sh' | … (Mechanismus ≠ Actor)
  prev_version    text,                             -- Rollback-Ziel (Binary)
  log_url         text,                             -- durable Build+Smoke-Ausgabe (D20c)
  health_url      text                              -- http: /version-URL · cli: absoluter Binary-Pfad · NULL: nicht sondierbar
);

-- Idempotenz (D11): Doppel-Zustellung/Re-Deploy derselben SHA im selben env
-- trifft dieselbe Zeile (Upsert), erzeugt nie eine zweite.
CREATE UNIQUE INDEX IF NOT EXISTS deployments_service_env_sha_key
  ON portfolio.deployments (service, environment, git_sha);

-- Live-Query „was läuft?" (D9): DISTINCT ON (service, environment) … ORDER BY deployed_at DESC.
CREATE INDEX IF NOT EXISTS deployments_service_env_deployed_at_idx
  ON portfolio.deployments (service, environment, deployed_at DESC);

-- Outbox-Drain für den (später) konsumierenden Deploy-Reaktor (D10) — Struktur
-- jetzt, Konsument erst hinter dem Test-Gate (WP5, nicht Teil dieses Files).
CREATE INDEX IF NOT EXISTS deployments_pending_idx
  ON portfolio.deployments (deployed_at) WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS deployments_initiative_idx
  ON portfolio.deployments (initiative_id) WHERE initiative_id IS NOT NULL;

-- ── 2. Publizierte status→deploy_state-Abbildung (WP4) ──────────────────────
-- rank: Worst-of-Präzedenz bei mehreren Services je Initiative (5 = schlimmst).
CREATE TABLE IF NOT EXISTS portfolio.deploy_state_map (
  status       text PRIMARY KEY,
  deploy_state text NOT NULL,
  rank         int  NOT NULL
);

INSERT INTO portfolio.deploy_state_map (status, deploy_state, rank) VALUES
  ('errored',     'errored',     5),
  ('rolled_back', 'rolled_back', 4),
  ('deploying',   'deploying',   3),
  ('pending',     'pending',     2),
  ('live',        'live',        1)
ON CONFLICT (status) DO NOTHING;

-- ── 3. Denormalisierte Board-Felder (WP4, Schreibquelle: nur Reconciler, D13) ─
ALTER TABLE portfolio.initiative
  ADD COLUMN IF NOT EXISTS deploy_state text,
  ADD COLUMN IF NOT EXISTS live_version text,
  ADD COLUMN IF NOT EXISTS live_sha     text;

-- ── 4. initiative_summary: Live-Definition + Deploy-Felder ──────────────────
DROP VIEW IF EXISTS portfolio.initiative_summary;
CREATE VIEW portfolio.initiative_summary AS
 SELECT id,
    firma,
    stage,
    title,
    status_dot,
    wip_pinned,
    primary_backend,
    created_at,
    updated_at,
    archived_at,
    COALESCE(( SELECT count(*) AS count
           FROM portfolio.initiative_link l
          WHERE l.initiative_id = i.id AND l.kind = 'bead'::text), 0::bigint) AS bead_count,
    COALESCE(( SELECT count(*) AS count
           FROM portfolio.initiative_link l
          WHERE l.initiative_id = i.id AND l.kind = 'vk_workspace'::text), 0::bigint) AS vk_count,
    COALESCE(( SELECT count(*) AS count
           FROM portfolio.initiative_link l
          WHERE l.initiative_id = i.id AND l.kind = 'github_pr'::text), 0::bigint) AS pr_count,
    COALESCE(( SELECT count(*) AS count
           FROM portfolio.initiative_link l
          WHERE l.initiative_id = i.id AND l.kind = 'plan_file'::text), 0::bigint) AS plan_count,
    ( SELECT max(e.at) AS max
           FROM portfolio.initiative_event e
          WHERE e.initiative_id = i.id) AS last_activity,
    description,
    tier,
    COALESCE(( SELECT json_agg(t.value ORDER BY t.value) AS json_agg
           FROM portfolio.initiative_tag t
          WHERE t.initiative_id = i.id AND t.kind = 'firma'::text), '[]'::json) AS firmas,
    COALESCE(( SELECT json_agg(t.value ORDER BY t.value) AS json_agg
           FROM portfolio.initiative_tag t
          WHERE t.initiative_id = i.id AND t.kind = 'software'::text), '[]'::json) AS softwares,
    deploy_state,
    live_version,
    live_sha
   FROM portfolio.initiative i
  WHERE archived_at IS NULL;
