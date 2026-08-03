-- portfolio-033 — initiative_event.kind: EINE Union-Definition (Befund 2026-08-03)
--
-- Latente Landmine, gefunden beim Tilgen der Integrationstest-Schulden:
--   * portfolio-014-flow-manager.sql  ergaenzt 'flow_action'
--   * portfolio-015-manager.sql       DROP+ADD, nimmt nur 'manager_flag' mit  → verliert flow_action
--   * portfolio-015-promote-damped.sql DROP+ADD, nimmt nur 'promote_damped' mit → verliert beide
--   * 'merged_into' (merge.go), 'handover', 'assigned' fehlen in JEDER Migration
-- Bei linearer Anwendung auf eine FRISCHE DB (Wiederaufbau, neue Umgebung,
-- Integrationstests) entsteht daher ein CHECK, der genau die Kinds ablehnt,
-- die der laufende Code schreibt: der Flow-Manager wuerde still verstummen
-- (INSERT scheitert, Aufrufer verwerfen den Fehler), Merges wuerden brechen.
-- Live faellt das nicht auf, weil die Prod-DB gar keinen kind-CHECK traegt
-- (irgendwann gedroppt) — Schema-Dateien und Wirklichkeit sind auseinander.
--
-- Diese Migration setzt EINE Definition aus (a) allen live vorkommenden Kinds,
-- (b) allen im Code geschriebenen Kinds, (c) 'schatten' (Autopilot Stufe 2:
-- Schatten-Laeufe protokollieren, was scharf gewesen waere). Additiv und
-- verifiziert: der Guard unten bricht ab, bevor eine bestehende Zeile
-- ungueltig wuerde.
\set ON_ERROR_STOP on

-- Guard: kein Kind im Bestand darf durch die neue Liste fallen.
DO $do$
DECLARE fehlend text;
BEGIN
  SELECT string_agg(DISTINCT kind, ', ') INTO fehlend
    FROM portfolio.initiative_event
   WHERE kind NOT IN ('created','moved','edited','linked','unlinked','activity',
                      'stage_proposed','completed','commented','archived','dispatched',
                      'deployed','workspace_started','ai_message','ai_action','sage_action',
                      'flow_action','manager_flag','promote_damped','merged_into',
                      'handover','assigned','schatten');
  IF fehlend IS NOT NULL THEN
    RAISE EXCEPTION 'Abbruch: Bestand enthaelt Kinds ausserhalb der neuen Liste (%). Liste ergaenzen, nicht Daten loeschen.', fehlend;
  END IF;
END $do$;

ALTER TABLE portfolio.initiative_event DROP CONSTRAINT IF EXISTS initiative_event_kind_check;
ALTER TABLE portfolio.initiative_event ADD CONSTRAINT initiative_event_kind_check
  CHECK (kind = ANY (ARRAY[
    'created','moved','edited','linked','unlinked','activity',
    'stage_proposed','completed','commented','archived','dispatched',
    'deployed','workspace_started','ai_message','ai_action','sage_action',
    'flow_action','manager_flag','promote_damped','merged_into',
    'handover','assigned','schatten'
  ]::text[]));

-- Verifikation (rein lesend): Definition + Bestands-Abdeckung.
SELECT pg_get_constraintdef(oid) AS neue_definition
  FROM pg_constraint WHERE conname='initiative_event_kind_check';
SELECT count(DISTINCT kind) AS kinds_im_bestand FROM portfolio.initiative_event;
