-- portfolio-031 — Teil-Index fuer Stage-Eintritts-Lookups (KPI-Leiste /
-- Spalten-Alter, kpis.go). Die stage_since-Ermittlung (juengstes moved-Event
-- je Karte+Zielspalte) und die KPI-Queries (Durchsatz moved→done 7T,
-- Automatik-Anteil) filtern immer kind='moved' — der bestehende
-- idx_event_initiative_at traegt das nicht: /api/initiatives lag bei
-- ~3-10 s (771k Events, korrelierte Scans). Partial-Index macht die
-- Lookups indexgestuetzt; moved-Events sind ein winziger Bruchteil der
-- Tabelle (flow_action/activity dominieren).
\set ON_ERROR_STOP on

CREATE INDEX IF NOT EXISTS idx_event_moved_initiative_stage_at
  ON portfolio.initiative_event (initiative_id, to_stage, at DESC)
  WHERE kind = 'moved';

-- Verifikation (rein lesend)
SELECT count(*) AS moved_events FROM portfolio.initiative_event WHERE kind='moved';
