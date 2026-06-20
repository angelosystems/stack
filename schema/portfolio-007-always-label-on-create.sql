-- portfolio-007-always-label-on-create.sql — Always-Label-on-Create trigger and backfill
--
-- 1) Create the fn_auto_label_on_create() function in beads database.
-- 2) Create the tr_issues_auto_label_on_create trigger on beads.issues table.
-- 3) Perform the backfill of plan:<slug> labels on all existing issues.
--
-- Idempotent — re-run/re-apply safe.

-- 1) Trigger-Funktion für automatische Label-Erzeugung bei Bead-Creation
CREATE OR REPLACE FUNCTION beads.fn_auto_label_on_create()
RETURNS trigger AS $$
DECLARE
  derived_slug text;
BEGIN
  derived_slug := NULL;

  -- A) Extrahiere Slug aus spec_id, falls vorhanden
  IF NEW.spec_id IS NOT NULL AND NEW.spec_id <> '' THEN
    derived_slug := regexp_replace(regexp_replace(lower(substring(NEW.spec_id from '[^/]+$')), '\.md$', ''), '-prd$', '');
  END IF;

  -- B) Falls nicht fündig geworden, extrahiere aus der Beschreibung (Description-Pfad)
  IF (derived_slug IS NULL OR derived_slug = '') AND NEW.description IS NOT NULL AND NEW.description <> '' THEN
    derived_slug := regexp_replace(regexp_replace(lower(substring(NEW.description from 'docs/plans/([a-zA-Z0-9_-]+)')), '\.md$', ''), '-prd$', '');
  END IF;

  -- C) Falls ein Slug abgeleitet werden konnte, das entsprechende Label plan:<slug> anlegen
  IF derived_slug IS NOT NULL AND derived_slug <> '' THEN
    INSERT INTO beads.labels (issue_id, rig, label)
    VALUES (NEW.id, NEW.rig, 'plan:' || derived_slug)
    ON CONFLICT (issue_id, label) DO UPDATE SET deleted_at = NULL;
  END IF;

  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- 2) Trigger anlegen
DROP TRIGGER IF EXISTS tr_issues_auto_label_on_create ON beads.issues;
CREATE TRIGGER tr_issues_auto_label_on_create
AFTER INSERT ON beads.issues
FOR EACH ROW
EXECUTE FUNCTION beads.fn_auto_label_on_create();

-- 3) Einmaliger A1-Backfill für bestehende Alt-Orphans
WITH derived AS (
  SELECT
    id,
    rig,
    CASE
      WHEN spec_id IS NOT NULL AND spec_id <> '' THEN
        regexp_replace(regexp_replace(lower(substring(spec_id from '[^/]+$')), '\.md$', ''), '-prd$', '')
      WHEN description IS NOT NULL AND description <> '' THEN
        regexp_replace(regexp_replace(lower(substring(description from 'docs/plans/([a-zA-Z0-9_-]+)')), '\.md$', ''), '-prd$', '')
      ELSE NULL
    END AS slug
  FROM beads.issues
  WHERE deleted_at IS NULL
)
INSERT INTO beads.labels (issue_id, rig, label)
SELECT id, rig, 'plan:' || slug
FROM derived
WHERE slug IS NOT NULL AND slug <> ''
ON CONFLICT (issue_id, label) DO UPDATE SET deleted_at = NULL;
