-- portfolio-016-tier-model.sql — Tier-Modell (library / code-fabrik / product)
--
-- Versioniert den Live-Stand, der am 30.06. direkt an der DB gepatcht, aber nie
-- in ein Schema-File geschrieben wurde (Live-vs-git-Drift, 00-architecture §3):
--   1. portfolio.initiative.tier — Tier-Zuordnung je Initiative; die Cockpit-Tabs
--      code-fabrik/library/product filtern darueber.
--   2. portfolio.initiative_tag  — generische (kind,value)-Tags je Initiative.
-- Extrahiert 1:1 aus der Live-DB (mario_brain, :5434) via pg_dump/\d.
-- Idempotent: re-run-sicher auch gegen die bereits gepatchte Live-DB (IF NOT EXISTS).

ALTER TABLE portfolio.initiative
  ADD COLUMN IF NOT EXISTS tier text;

-- ponytail: treu zum Live-Stand — tier ist freies text ohne CHECK, und
-- initiative_tag traegt KEINEN FK auf initiative(id) (anders als initiative_link,
-- das ON DELETE CASCADE referenziert). Bewusst NICHT gesetzt, damit dieses File
-- die Live-DB exakt reproduziert. Upgrade-Pfad, falls gewuenscht: (a) CHECK
-- (tier IN ('library','code-fabrik','product')) nach Bereinigung leerer Werte;
-- (b) FK initiative_id -> initiative(id) ON DELETE CASCADE nach Entfernen
-- ungenutzter Tags.
CREATE TABLE IF NOT EXISTS portfolio.initiative_tag (
  initiative_id text        NOT NULL,
  kind          text        NOT NULL,
  value         text        NOT NULL,
  added_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (initiative_id, kind, value)
);
