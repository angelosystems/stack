-- portfolio-029: User-Management — Personen-Verzeichnis + Zuordnung
-- (PRD mk-user-management). Loest die MK_MITARBEITER_SCOPE-Env als Quelle der
-- Wahrheit ab (Env bleibt nur Bootstrap-Seed) und gibt Karten einen Owner,
-- PRDs/plan_items einen Assignee. Idempotent (IF NOT EXISTS / OR REPLACE),
-- gefahrlos re-applybar.

-- ---------------------------------------------------------------------------
-- Personen-Verzeichnis (fabrikweit, ueber alle Firmen)
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS portfolio.person (
    email        text PRIMARY KEY,                       -- SSO-Identitaet (X-Auth-Request-Email, lowercase)
    display_name text NOT NULL,
    role         text NOT NULL DEFAULT 'mitarbeiter'     -- 'admin' | 'mitarbeiter'
                 CHECK (role IN ('admin','mitarbeiter')),
    active       boolean NOT NULL DEFAULT true,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- Person <-> Firma (n:m; ein Mitarbeiter kann mehrere Firmen sehen)
CREATE TABLE IF NOT EXISTS portfolio.person_firma (
    email text NOT NULL REFERENCES portfolio.person(email) ON DELETE CASCADE,
    firma text NOT NULL,
    PRIMARY KEY (email, firma)
);
CREATE INDEX IF NOT EXISTS idx_person_firma_firma ON portfolio.person_firma (firma);

-- ---------------------------------------------------------------------------
-- Zuordnung: Owner an der Karte, Assignee am PRD/plan_item.
-- Bewusst soft-ref (kein FK): Deaktivieren/Loeschen einer Person darf keine
-- Karte zerreissen; die UI loest email->name auf, unbekannt -> grau/"?".
-- ---------------------------------------------------------------------------
ALTER TABLE portfolio.initiative ADD COLUMN IF NOT EXISTS owner_email text;
ALTER TABLE portfolio.plan_item  ADD COLUMN IF NOT EXISTS assignee_email text;
CREATE INDEX IF NOT EXISTS idx_initiative_owner   ON portfolio.initiative (owner_email);
CREATE INDEX IF NOT EXISTS idx_plan_item_assignee ON portfolio.plan_item (assignee_email);

-- ---------------------------------------------------------------------------
-- initiative_summary: Basis 028 wortgleich, drei Spalten hinten angehaengt —
--   owner_email      : Roh-Email des Card-Owners (soft-ref)
--   owner_name       : aufgeloest ueber portfolio.person (NULL wenn unbekannt)
--   assignee_emails  : Set der Assignee-Emails aller plan_items der Karte
--                      (fuer den "assigned-to-me"-Read-Scope, s. mitarbeiter_scope.go)
-- ---------------------------------------------------------------------------
CREATE OR REPLACE VIEW portfolio.initiative_summary AS
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
   live_sha,
   beads_closed,
   beads_total,
   ( SELECT d.status || '@' || COALESCE(NULLIF(d.version,''), substr(d.git_sha,1,7))
          FROM portfolio.deployments d
         WHERE d.initiative_id = i.id AND d.environment = 'staging'
         ORDER BY d.deployed_at DESC LIMIT 1) AS staging_state,
   ( SELECT max(d.deployed_at)
          FROM portfolio.deployments d
         WHERE d.initiative_id = i.id AND d.environment = 'staging') AS staging_at,
   ( SELECT d.status || '@' || COALESCE(NULLIF(d.version,''), substr(d.git_sha,1,7))
          FROM portfolio.deployments d
         WHERE d.initiative_id = i.id AND d.environment IN ('prod','prod-mvp')
         ORDER BY d.deployed_at DESC LIMIT 1) AS prod_state,
   ( SELECT max(d.deployed_at)
          FROM portfolio.deployments d
         WHERE d.initiative_id = i.id AND d.environment IN ('prod','prod-mvp')) AS prod_at,
   ( SELECT t.value FROM portfolio.initiative_tag t
          WHERE t.initiative_id = i.id AND t.kind = 'lane' LIMIT 1) AS dispatch_lane,
   parent_id,
   ( SELECT count(*) FROM portfolio.initiative k
          WHERE k.parent_id = i.id AND k.archived_at IS NULL) AS kinder_count,
   EXISTS ( SELECT 1 FROM portfolio.plan_item p
          WHERE p.initiative_id = i.id
            AND p.status IN ('approved','approved-with-notes')) AS approved_plan,
   ( SELECT t.value FROM portfolio.initiative_tag t
          WHERE t.initiative_id = i.id AND t.kind = 'session' LIMIT 1) AS session_tag,
   owner_email,
   ( SELECT pn.display_name FROM portfolio.person pn WHERE pn.email = i.owner_email) AS owner_name,
   COALESCE(( SELECT json_agg(DISTINCT pi.assignee_email)
          FROM portfolio.plan_item pi
         WHERE pi.initiative_id = i.id AND pi.assignee_email IS NOT NULL), '[]'::json) AS assignee_emails
  FROM portfolio.initiative i
 WHERE archived_at IS NULL;
