-- Backfill (Einmaljob, PRD mk-pipeline-ampel WP1): Bestands-Ledger-Zeilen
-- ohne initiative_id per service→software-Tag-Match verknuepfen.
-- Regeln: NUR NULL→Wert (nie ueberschreiben), NUR eindeutige Matches,
-- EINE Transaktion. Rueckweg = UPDATE aus dem Backup-CSV.
--
-- VOR dem Apply (dry-run): den SELECT unten einzeln ausfuehren und Kandidaten
-- sichten. Backup ist Teil der Transaktion (COPY schreibt serverseitig):
--   COPY (SELECT id, initiative_id FROM portfolio.deployments)
--     TO '/root/backups/deployments-backfill-<datum>.csv' CSV HEADER;
--
-- Dry-run (Kandidaten + Ziel-Karte):
--   SELECT d.id, d.service, d.environment, m.initiative_id
--     FROM portfolio.deployments d
--     JOIN ( SELECT t.value AS service, min(t.initiative_id) AS initiative_id
--              FROM portfolio.initiative_tag t
--              JOIN portfolio.initiative i ON i.id=t.initiative_id AND i.archived_at IS NULL
--             WHERE t.kind='software'
--             GROUP BY t.value HAVING count(DISTINCT t.initiative_id)=1 ) m
--       ON m.service = d.service
--    WHERE d.initiative_id IS NULL ORDER BY d.service;

BEGIN;

UPDATE portfolio.deployments d
   SET initiative_id = m.initiative_id
  FROM ( SELECT t.value AS service, min(t.initiative_id) AS initiative_id
           FROM portfolio.initiative_tag t
           JOIN portfolio.initiative i ON i.id = t.initiative_id AND i.archived_at IS NULL
          WHERE t.kind = 'software'
          GROUP BY t.value
         HAVING count(DISTINCT t.initiative_id) = 1 ) m
 WHERE m.service = d.service
   AND d.initiative_id IS NULL;

COMMIT;
