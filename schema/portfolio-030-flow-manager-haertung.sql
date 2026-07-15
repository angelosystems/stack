-- portfolio-030 — Flow-Manager-Haertung (PRD mk-flow-manager-haertung, WP1)
--
-- Zwei neue steward_findings-Klassen + Trigger-Attribution:
--
--   7) flow-diagnose: juengstes flow_action-Event je Karte mit nicht-leerem
--      Flag-Satz. ERSETZT den toten gt-mail-Digest des Flow-Managers als
--      Meldeweg — die Diagnose fliesst damit in den bewiesenen Kreislauf
--      Reflex → Verwalter → Werksleiter-Digest. Findings sind MELDUNGEN
--      (Verwalter-Urteil); Vollzug daraus erst nach 3/3-Kalibrierung.
--   8) promote-sackgasse: idea/soon-Karte, alle Beads closed — dafuer gibt es
--      bewusst KEINEN Auto-Vollzugspfad (now vollzieht der Sweep, watching
--      deckt watching-ohne-deploy): der Fall ist ein Urteilsfall und wurde
--      vorher pro Runde sinnlos GLM-diagnostiziert (Ist-Fall qb-desk-truth).
--
-- Trigger notify_stage_change: actor kommt jetzt bevorzugt aus dem
-- transaktionslokalen GUC portfolio.actor (set_config(..., true)) — der
-- Flow-Manager-Vollzug stempelt sich damit ehrlich in die moved-Events
-- (vorher: actor=current_user, also immer der DB-User, egal wer bewegte).
-- Ohne gesetzten GUC bleibt current_user (Cockpit/CLI unveraendert).
--
-- View-Text der Klassen 1-6 UNVERAENDERT aus portfolio-025 uebernommen
-- (Konvention: Views aus der Vorgaenger-Datei generieren, nie aus dem Kopf).
\set ON_ERROR_STOP on

-- Guard: pg_trgm vorhanden? (dup-kandidat nutzt similarity())
DO $do$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN
    RAISE EXCEPTION 'steward_findings braucht Extension pg_trgm. Als Superuser: CREATE EXTENSION pg_trgm;';
  END IF;
END $do$;

CREATE OR REPLACE VIEW portfolio.steward_findings AS
-- 1) dup-kandidat: Titel-Aehnlichkeit (pg_trgm) innerhalb gleicher firma, beide
--    unarchiviert, Paar-Normierung (kleinere id zuerst → keine a-b/b-a-Doppel).
--    fabrik-alarm-Karten AUSGESCHLOSSEN: eigene Schiene (ap-forwarder-Dedup +
--    Einmal-Kehraus, WP2); die ~28 identischen Alarm-Titel wuerden sonst
--    C(28,2)=~378 Paare erzeugen und das echte Dup-Signal (vollbetrieb-finale)
--    zudecken.
SELECT
  'dup-kandidat'::text AS klasse,
  a.id                 AS initiative_id,
  a.title              AS titel,
  a.firma              AS firma,
  a.stage              AS stage,
  jsonb_build_object(
    'partner_id',    b.id,
    'partner_titel', b.title,
    'similarity',    round(similarity(lower(a.title), lower(b.title))::numeric, 3)
  ) AS detail,
  md5('dup-kandidat' || a.id || b.id) AS finding_hash
FROM portfolio.initiative a
JOIN portfolio.initiative b
  ON a.firma = b.firma
 AND a.id < b.id
 AND a.archived_at IS NULL
 AND b.archived_at IS NULL
 AND a.id NOT LIKE 'fabrik-alarm-%'
 AND b.id NOT LIKE 'fabrik-alarm-%'
 AND similarity(lower(a.title), lower(b.title)) > 0.55

UNION ALL

-- 2) parent-check v3 (mk-karten-hierarchie WP4, nach den 4 COD-93-Befunden):
--    nur Karten OHNE parent_id, die selbst kein Umbrella sind; Kandidaten nur
--    echte Daecher derselben firma (Umbrella-Titel ODER hat schon Kinder),
--    nie done, nie das eigene Kind (Inversions-Wache); Findings ohne einen
--    einzigen Kandidaten entfallen (waren reine KEEP-Arbeit).
SELECT
  'parent-check'::text,
  i.id, i.title, i.firma, i.stage,
  jsonb_build_object('dach_kandidaten', k.kandidaten) AS detail,
  md5('parent-check' || i.id) AS finding_hash
FROM portfolio.initiative i
JOIN portfolio.initiative_tag pt
  ON pt.initiative_id = i.id AND pt.kind = 'triage' AND pt.value = 'parent-check'
JOIN LATERAL (
  SELECT jsonb_agg(jsonb_build_object('id', cand.id, 'grund', cand.grund)) AS kandidaten
  FROM (
    SELECT o.id,
           CASE WHEN o.title ~* '(roadmap|vision|vollausbau|strategie|dekade)' THEN 'umbrella'
                ELSE 'hat-kinder' END AS grund
    FROM portfolio.initiative o
    WHERE o.id <> i.id AND o.firma = i.firma AND o.archived_at IS NULL
      AND o.stage <> 'done'
      AND COALESCE(o.parent_id,'') <> i.id
      AND (
        o.title ~* '(roadmap|vision|vollausbau|strategie|dekade)'
        OR EXISTS (SELECT 1 FROM portfolio.initiative kk WHERE kk.parent_id = o.id)
      )
    ORDER BY (o.title ~* '(roadmap|vision|vollausbau|strategie|dekade)') DESC, o.id
    LIMIT 3
  ) cand
) k ON k.kandidaten IS NOT NULL
WHERE i.archived_at IS NULL
  AND i.parent_id IS NULL
  AND i.title !~* '(roadmap|vision|vollausbau|strategie|dekade)'

UNION ALL

-- 3) tier-los: tier IS NULL; Vorschlag aus firma (code-factory→code-fabrik,
--    sonst product — wie firmaDefaultTier im planfile-adapter).
--    fabrik-alarm AUSGESCHLOSSEN (eigene Schiene): Ist-Stand 2026-07-12 sind
--    ALLE tier-NULL-Karten Alarm-Karten; sie werden im Kehraus zu EINER
--    kanonischen zusammengefuehrt, die dort ihren tier bekommt — 29 identische
--    tier-los-Findings waeren reines Rauschen.
SELECT
  'tier-los'::text,
  i.id, i.title, i.firma, i.stage,
  jsonb_build_object('tier_vorschlag',
    CASE WHEN i.firma = 'code-factory' THEN 'code-fabrik' ELSE 'product' END) AS detail,
  md5('tier-los' || i.id || CASE WHEN i.firma = 'code-factory' THEN 'code-fabrik' ELSE 'product' END) AS finding_hash
FROM portfolio.initiative i
WHERE i.tier IS NULL
  AND i.archived_at IS NULL
  AND i.id NOT LIKE 'fabrik-alarm-%'

UNION ALL

-- 4) now-ohne-evidenz: stage=now, kein bead-Link, kein aktiver plan_item
--    (approved/approved-with-notes/in-progress), aelter als 3 Tage.
--    Alarmkarten AUSGESCHLOSSEN (eigene Schiene).
SELECT
  'now-ohne-evidenz'::text,
  i.id, i.title, i.firma, i.stage,
  jsonb_build_object('alter_tage',
    floor(extract(epoch FROM (now() - i.created_at)) / 86400)::int) AS detail,
  md5('now-ohne-evidenz' || i.id) AS finding_hash
FROM portfolio.initiative i
WHERE i.stage = 'now'
  AND i.archived_at IS NULL
  AND i.id NOT LIKE 'fabrik-alarm-%'
  AND i.created_at < now() - interval '3 days'
  AND NOT EXISTS (SELECT 1 FROM portfolio.initiative_link l
                    WHERE l.initiative_id = i.id AND l.kind = 'bead')
  AND NOT EXISTS (SELECT 1 FROM portfolio.plan_item pi
                    WHERE pi.initiative_id = i.id
                      AND pi.status IN ('approved','approved-with-notes','in-progress'))

UNION ALL

-- 5) watching-ohne-deploy: stage=watching, Delivery-Beleg da (plan_item
--    delivered/done ODER *-delivery.md verlinkt), aber juengster deployments-
--    Eintrag der Karte fehlt/≠live, UND die Software kommt ueberhaupt im Ledger
--    vor (software-Tag ↔ deployments.service). Spiegelt watchingDoneDecision
--    (flow_vollzug.go) — dieselbe Regel, ein Source of Truth.
SELECT
  'watching-ohne-deploy'::text,
  i.id, i.title, i.firma, i.stage,
  jsonb_build_object('letzter_deploy_status',
    (SELECT d.status FROM portfolio.deployments d
      WHERE d.initiative_id = i.id
      ORDER BY d.deployed_at DESC NULLS LAST LIMIT 1)) AS detail,
  md5('watching-ohne-deploy' || i.id) AS finding_hash
FROM portfolio.initiative i
WHERE i.stage = 'watching'
  AND i.archived_at IS NULL
  AND (
    EXISTS (SELECT 1 FROM portfolio.plan_item pi
              WHERE pi.initiative_id = i.id AND pi.status IN ('delivered','done'))
    OR EXISTS (SELECT 1 FROM portfolio.initiative_link l
                 WHERE l.initiative_id = i.id AND l.kind = 'plan_file' AND l.ref LIKE '%-delivery.md')
  )
  AND COALESCE((SELECT d.status FROM portfolio.deployments d
                  WHERE d.initiative_id = i.id
                  ORDER BY d.deployed_at DESC NULLS LAST LIMIT 1), '') <> 'live'
  AND EXISTS (
    SELECT 1 FROM portfolio.initiative_tag t
    JOIN portfolio.deployments d ON lower(d.service) = lower(t.value)
    WHERE t.initiative_id = i.id AND t.kind = 'software'
  )

UNION ALL

-- 6) lane-pending (mk-dispatch-gate WP3): Karte hat ein approved PRD, aber
--    kein lane-Tag — der Decomposer haelt (Gate), die Lane-Entscheidung
--    wartet auf Mario. Empfehlung = grobe Groessenordnungs-Heuristik, NUR
--    als Vorschlagstext.
SELECT
  'lane-pending'::text,
  i.id, i.title, i.firma, i.stage,
  jsonb_build_object(
    'plan_items', (SELECT count(*) FROM portfolio.plan_item pp WHERE pp.initiative_id = i.id),
    'empfehlung', CASE
      WHEN (SELECT count(*) FROM portfolio.plan_item pp WHERE pp.initiative_id = i.id) > 1
        THEN 'solartown? (mehrere Plan-Teile)'
      WHEN NOT EXISTS (SELECT 1 FROM portfolio.initiative_link l
                        WHERE l.initiative_id = i.id AND l.kind = 'bead')
        THEN 'vibe-kanban? (klein, bead-los)'
      ELSE 'unklar — Mario entscheidet' END) AS detail,
  md5('lane-pending' || i.id) AS finding_hash
FROM portfolio.initiative i
WHERE i.archived_at IS NULL
  -- nur ECHTE wartende Entscheidungen: fruehe Stages, noch nicht zerlegt
  -- (Karten mit Beads sind faktisch schon in der Plan-Lane gewesen).
  AND i.stage IN ('idea','soon','now')
  AND NOT EXISTS (SELECT 1 FROM portfolio.initiative_link l
                  WHERE l.initiative_id = i.id AND l.kind = 'bead')
  AND EXISTS (SELECT 1 FROM portfolio.plan_item p
              WHERE p.initiative_id = i.id
                AND p.status IN ('approved','approved-with-notes'))
  AND NOT EXISTS (SELECT 1 FROM portfolio.initiative_tag t
                  WHERE t.initiative_id = i.id AND t.kind = 'lane')

UNION ALL

-- 7) flow-diagnose (mk-flow-manager-haertung WP1): juengstes flow_action-
--    Event je Karte mit nicht-leeren flagged_reasons — der Meldeweg der
--    Sweep-Diagnosen (ersetzt den toten gt-mail-Digest). Clearing-Events
--    (flagged_reasons=[]) beenden das Finding; finding_hash haengt am
--    flags_hash, damit ein NEUER Flag-Satz als neues Finding zaehlt
--    (Mengen-Diff im board-pflege-Reflex).
SELECT
  'flow-diagnose'::text,
  i.id, i.title, i.firma, i.stage,
  jsonb_build_object(
    'category',        e.payload->>'category',
    'confidence',      e.payload->>'confidence',
    'reasons',         e.payload->'flagged_reasons',
    'proposed_action', e.payload->>'proposed_action') AS detail,
  md5('flow-diagnose' || i.id || COALESCE(e.payload->>'flags_hash','')) AS finding_hash
FROM portfolio.initiative i
JOIN LATERAL (
  SELECT ev.payload
    FROM portfolio.initiative_event ev
   WHERE ev.initiative_id = i.id AND ev.kind = 'flow_action'
   ORDER BY ev.at DESC LIMIT 1
) e ON true
WHERE i.archived_at IS NULL
  AND COALESCE(e.payload->'flagged_reasons', '[]'::jsonb) <> '[]'::jsonb

UNION ALL

-- 8) promote-sackgasse (mk-flow-manager-haertung WP1): idea/soon-Karte mit
--    Beads, alle closed — kein Auto-Vollzugspfad (bewusst), Urteilsfall fuer
--    den Verwalter. Zaehler kommen aus dem Sweep-Cache (beads_closed/_total).
SELECT
  'promote-sackgasse'::text,
  i.id, i.title, i.firma, i.stage,
  jsonb_build_object('beads', i.beads_closed || '/' || i.beads_total) AS detail,
  md5('promote-sackgasse' || i.id) AS finding_hash
FROM portfolio.initiative i
WHERE i.archived_at IS NULL
  AND i.stage IN ('idea','soon')
  AND i.beads_total > 0
  AND i.beads_closed = i.beads_total;


-- Trigger-Attribution: portfolio.actor (transaktionslokal) gewinnt vor
-- current_user. NULL/leer ⇒ Verhalten wie bisher.
CREATE OR REPLACE FUNCTION portfolio.notify_stage_change() RETURNS trigger AS $fn$
BEGIN
  IF TG_OP = 'UPDATE' AND OLD.stage IS DISTINCT FROM NEW.stage THEN
    PERFORM pg_notify('portfolio_stage_change',
      json_build_object('id', NEW.id, 'firma', NEW.firma, 'from', OLD.stage, 'to', NEW.stage)::text);
    INSERT INTO portfolio.initiative_event (initiative_id, kind, source_backend, from_stage, to_stage, actor)
      VALUES (NEW.id, 'moved', 'master', OLD.stage, NEW.stage,
              COALESCE(NULLIF(current_setting('portfolio.actor', true), ''), current_user));
  END IF;
  NEW.updated_at = now();
  RETURN NEW;
END;
$fn$ LANGUAGE plpgsql;


-- Verifikation (rein lesend): Findings pro Klasse.
SELECT klasse, count(*) FROM portfolio.steward_findings GROUP BY klasse ORDER BY klasse;
