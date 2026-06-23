-- portfolio-014-lane-badges.sql — Lane Badges for Cockpit
--
-- Redefine portfolio.initiative_summary to include a 'lane' column
-- based on the majority of lane:* labels of its linked beads.

CREATE OR REPLACE VIEW portfolio.initiative_summary AS
 SELECT id, firma, stage, title, status_dot, wip_pinned, primary_backend,
        created_at, updated_at, archived_at,
        COALESCE(( SELECT count(*) FROM portfolio.initiative_link l
                   WHERE l.initiative_id = i.id AND l.kind = 'bead'), 0::bigint) AS bead_count,
        COALESCE(( SELECT count(*) FROM portfolio.initiative_link l
                   WHERE l.initiative_id = i.id AND l.kind = 'vk_workspace'), 0::bigint) AS vk_count,
        COALESCE(( SELECT count(*) FROM portfolio.initiative_link l
                   WHERE l.initiative_id = i.id AND l.kind = 'github_pr'), 0::bigint) AS pr_count,
        COALESCE(( SELECT count(*) FROM portfolio.initiative_link l
                   WHERE l.initiative_id = i.id AND l.kind = 'plan_file'), 0::bigint) AS plan_count,
        ( SELECT max(e.at) FROM portfolio.initiative_event e
          WHERE e.initiative_id = i.id) AS last_activity,
        description,
        COALESCE(
          ( SELECT regexp_replace(bl.label, '^lane:', '')
            FROM beads.labels bl
            JOIN portfolio.initiative_link il ON il.kind = 'bead' AND il.ref = bl.issue_id
            WHERE il.initiative_id = i.id AND bl.label LIKE 'lane:%' AND bl.deleted_at IS NULL
            GROUP BY bl.label
            ORDER BY count(*) DESC, bl.label ASC
            LIMIT 1 ),
          'untriagiert'
        ) AS lane
   FROM portfolio.initiative i
  WHERE archived_at IS NULL;
