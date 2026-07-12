-- portfolio-022: parent-check-Detektor-Nachschaerfung (mk-verwalter-Befund,
-- Volldurchlauf a00c87c8): Umbrella-Karten (Roadmap/Vision/Vollausbau/
-- Strategie/Dekade, gleiche firma) werden als Dach-Kandidaten indexiert und
-- zuerst gereiht. Basis = portfolio-020 WORTGLEICH bis auf den
-- parent-check-Kandidaten-Block; Re-Apply von 020 = Rollback.

-- portfolio-020-steward-findings.sql — Findings-Surface fuer das Urteil
-- PRD: mk-verwalter-vollzug (approved-with-notes, 2026-07-12), WP3.
--
-- Read-only-Ableitung (View, KEIN Speicher — Regel 8, ein Source of Truth pro
-- Frage): sammelt die Urteils-Klassen, die Abwaegung brauchen, aus dem
-- Board-Zustand. Der Reflex cf-reflex-board-pflege (WP4) pollt sie und macht
-- pro neuem finding_hash EIN Issue an den mk-verwalter. Archivierte Karten
-- erscheinen NIE.
--
-- Idempotent: CREATE OR REPLACE VIEW — Zweitlauf = no-op. BEWUSST OHNE eigenes
-- BEGIN/COMMIT, damit die Migration in einer aeusseren Transaktion validiert
-- (BEGIN; \i ...; ROLLBACK;) werden kann, ohne auf die Live-DB zu schreiben.
-- Das eigentliche Apply macht die Hauptsession beim Deploy.
--
-- pg_trgm-ABHAENGIGKEIT: dup-kandidat nutzt similarity() (Trigramm). Live
-- (mario_brain :5434) hat die Extension. Der Guard unten bricht mit
-- handlungsleitender Meldung ab, falls sie fehlt. CREATE EXTENSION ist bewusst
-- NICHT in dieser Migration (Superuser-Frage offen). Upgrade-Pfad / Fallback
-- ohne pg_trgm: dup-kandidat ueber Wort-Token-Overlap statt Trigramm, z.B.
--   cardinality(ARRAY(SELECT unnest(string_to_array(lower(a.title),' '))
--               INTERSECT SELECT unnest(string_to_array(lower(b.title),' '))))
--   >= 0.6 * greatest(#worte_a, #worte_b)  (nur Titel > 1 Wort)
-- — deterministisch, aber grober; das eigentliche Urteil liegt ohnehin beim
-- Agenten, darum ist der Trigramm-Filter als Kandidaten-Sieb ausreichend.
\set ON_ERROR_STOP on

-- Guard: pg_trgm vorhanden? Sonst handlungsleitend abbrechen.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN
    RAISE EXCEPTION 'steward_findings braucht Extension pg_trgm (dup-kandidat nutzt similarity()). Als Superuser: CREATE EXTENSION pg_trgm;  — oder die Token-Overlap-Variante aus dem Datei-Kommentar einsetzen.';
  END IF;
END $$;

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

-- 2) parent-check: Karten mit Tag triage:parent-check. detail traegt bis zu 3
--    Dach-Kandidaten (andere unarchivierte Karten gleicher firma) mit Grund:
--    gemeinsames software-Tag (Vorrang) oder gemeinsamer 3-Token-id-Praefix.
SELECT
  'parent-check'::text,
  i.id, i.title, i.firma, i.stage,
  jsonb_build_object('dach_kandidaten', COALESCE(k.kandidaten, '[]'::jsonb)) AS detail,
  md5('parent-check' || i.id) AS finding_hash
FROM portfolio.initiative i
JOIN portfolio.initiative_tag pt
  ON pt.initiative_id = i.id AND pt.kind = 'triage' AND pt.value = 'parent-check'
LEFT JOIN LATERAL (
  SELECT jsonb_agg(jsonb_build_object('id', cand.id, 'grund', cand.grund)) AS kandidaten
  FROM (
    SELECT o.id,
           CASE WHEN o.title ~* '(roadmap|vision|vollausbau|strategie|dekade)' THEN 'umbrella'
                WHEN sw.matched THEN 'software-match'
                ELSE 'id-praefix' END AS grund
    FROM portfolio.initiative o
    LEFT JOIN LATERAL (
      SELECT true AS matched
      WHERE EXISTS (
        SELECT 1 FROM portfolio.initiative_tag t1
        JOIN portfolio.initiative_tag t2
          ON t1.kind = 'software' AND t2.kind = 'software' AND t1.value = t2.value
        WHERE t1.initiative_id = i.id AND t2.initiative_id = o.id)
    ) sw ON true
    WHERE o.id <> i.id AND o.firma = i.firma AND o.archived_at IS NULL
      AND (
        o.title ~* '(roadmap|vision|vollausbau|strategie|dekade)'
        OR sw.matched
        OR split_part(o.id,'-',1)||'-'||split_part(o.id,'-',2)||'-'||split_part(o.id,'-',3)
         = split_part(i.id,'-',1)||'-'||split_part(i.id,'-',2)||'-'||split_part(i.id,'-',3)
      )
    ORDER BY (o.title ~* '(roadmap|vision|vollausbau|strategie|dekade)') DESC,
             sw.matched DESC NULLS LAST, o.id
    LIMIT 3
  ) cand
) k ON true
WHERE i.archived_at IS NULL

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
  );

-- Verifikation (rein lesend): Findings pro Klasse.
SELECT klasse, count(*) FROM portfolio.steward_findings GROUP BY klasse ORDER BY klasse;
