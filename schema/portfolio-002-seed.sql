-- Seed der 38 Initiativen aus vault/projects/portfolio-inventur.md
-- Idempotent — re-run sicher dank ON CONFLICT.

INSERT INTO portfolio.initiative (id, firma, stage, title, primary_backend) VALUES
-- Stay Awesome (12)
('sa-markthalle-buergschaft', 'stayawesome', 'now',      'Markthalle Bürgschaft Sparkasse Mai 2026',      'plan_file'),
('sa-fred-vsop-offboarding',  'stayawesome', 'now',      'Fred Neust Austritt + VSOP-Nachzeichnung',      'plan_file'),
('sa-mews-skr04-bridge',      'stayawesome', 'now',      'Mews-LedgerAccountCode → SKR-04 (direct)',      'plan_file'),
('sa-cards-pleo',             'stayawesome', 'soon',     'Kreditkarten-Setup Pleo-Alternative',           'plan_file'),
('sa-wdl-inventur',           'stayawesome', 'soon',     'WDL-Inventur 363 PandaDoc-docs Korrektur',      'plan_file'),
('sa-documenso-user-mgmt',    'stayawesome', 'watching', 'Documenso self-hosted — User-Anlage via Container', 'plan_file'),
('sa-office-gcp-bootstrap',   'stayawesome', 'done',     'GCP-Projekt + gaia-ai SA + DWD + Chat',         'plan_file'),
('sa-sso-stack',              'stayawesome', 'done',     'oauth2-proxy + Workspace SSO',                  'plan_file'),
('sa-inbox-zero',             'stayawesome', 'done',     'Inbox Zero self-hosted',                        'plan_file'),
('sa-dns-migration',          'stayawesome', 'done',     'DNS-Migration 8 Zonen → Cloudflare',            'plan_file'),
('sa-bitwarden-org',          'stayawesome', 'watching', 'Bitwarden Org auf vault.bitwarden.com',         'plan_file'),
('sa-fin-repo-v2',            'stayawesome', 'idea',     'fin-Repo V2 — Schema + USALI-Mapping',          'plan_file'),
-- Solartown (10)
('st-promote-completion',         'solartown', 'now',      'Promote 2026-05-01 vervollständigen',                              'solartown'),
('st-quantbot-paperclip-rollout', 'solartown', 'now',      'Epic qb-4m4 — QuantumShift × Paperclip Final Rollout',             'solartown plan_file'),
('st-end-to-end-ingest',          'solartown', 'now',      'Epic-Ingest Pipeline Stages 3-5',                                  'solartown github plan_file'),
('st-staging-mode-c',             'solartown', 'now',      'Staging Mode C 2026-05-03',                                        'plan_file'),
('st-postgres-decom-finish',      'solartown', 'soon',     'Postgres-Decommission abschließen',                                'solartown'),
('st-reactor-fixes',              'solartown', 'now',      'Reactor-Stuck + Polecat-Watcher + Stuck-Claim-Recovery',           'solartown github'),
('st-mq-auto-merger',             'solartown', 'done',     'Auto-Merger + Pre-Reviewer + Reactor DB-pick',                     'github'),
('st-sage-advisor',               'solartown', 'done',     'Sage-Advisor Phase-2',                                             'solartown'),
('st-v15-haertung',               'solartown', 'done',     'V15 Härtungs-Run',                                                 'solartown'),
('st-weft-migration',             'solartown', 'watching', 'weft-Migration in /opt/weft-lab/',                                 'plan_file'),
-- QuantBot (7)
('qb-pusd-trade-flow-mystery', 'quantbot', 'now',      'pUSD-Allowance + Trade-Flow klären',           'vk solartown'),
('qb-v2-only-policy',          'quantbot', 'now',      'V2-only Policy',                               'solartown'),
('qb-tsdb-compression-fix',    'quantbot', 'soon',     'TSDB Columnstore-Policy Contention',           'solartown'),
('qb-live-trading-day-1',      'quantbot', 'done',     'Erste Live-Trades 2026-04-29 — +$57.23',       'solartown'),
('qb-master-blueprint',        'quantbot', 'done',     '5-Mermaid Master-Karte',                       'plan_file'),
('qb-kingdom-dashboard',       'quantbot', 'watching', 'Kingdom Dashboard Next.js 16 :3333',           'plan_file'),
('qb-paperclip-consolidation', 'quantbot', 'now',      'Paperclip-Consolidation',                      'plan_file'),
-- mario-brain (4)
('mb-master-kanban-build',   'mariobrain', 'now',  'Master-Kanban / Arbeitsoberfläche bauen', 'plan_file'),
('mb-vault-segmentation-p1', 'mariobrain', 'soon', 'Vault-Segmentation P1',                   'plan_file'),
('mb-phase-1-live',          'mariobrain', 'done', 'Phase 1 — Sessions + FTS live',           'plan_file'),
('mb-phase-2-pgvector',      'mariobrain', 'idea', 'Phase 2 — Embeddings/pgvector',           'plan_file'),
-- AngeloOS (5)
('ag-llm-sidecar-revival',    'angeloos', 'now',      'gt-llm-sidecar reaktivieren',           'solartown'),
('ag-cross-tenant-trennung',  'angeloos', 'soon',     'Cross-Tenant Trennung P1-P3',           'plan_file'),
('ag-jcode-harness',          'angeloos', 'watching', 'jcode harness evaluieren',              'plan_file'),
('ag-whatsapp-bridge-pflege', 'angeloos', 'watching', 'WhatsApp-Bridge Pflege',                'plan_file'),
('ag-cockpit-architektur',    'angeloos', 'idea',     'Cockpit-Architektur',                   'plan_file')
ON CONFLICT (id) DO UPDATE SET
  firma = EXCLUDED.firma,
  stage = EXCLUDED.stage,
  title = EXCLUDED.title,
  primary_backend = EXCLUDED.primary_backend,
  updated_at = now();

-- Initial-Links aus inventur (subset — nur die mit konkreten refs)
INSERT INTO portfolio.initiative_link (initiative_id, kind, ref) VALUES
('sa-markthalle-buergschaft', 'plan_file', '/root/stayawesomeOS/docs/plans/markthalle-bank-nachforderung.md'),
('sa-fred-vsop-offboarding',  'plan_file', '/root/stayawesomeOS/docs/plans/fred-neust-offboarding.md'),
('sa-cards-pleo',             'plan_file', '/root/stayawesomeOS/docs/plans/cards-backlog.md'),
('sa-cards-pleo',             'plan_file', '/root/stayawesomeOS/docs/plans/cards-pleo-alternative.md'),
('sa-documenso-user-mgmt',    'plan_file', '/root/stayawesomeOS/docs/plans/documenso-authentik-options.md'),
('sa-dns-migration',          'plan_file', '/root/stayawesomeOS/docs/plans/dns-backlog.md'),
('st-promote-completion',         'bead',      'cl-fzrdyr'),
('st-promote-completion',         'bead',      'cl-cm90qz'),
('st-quantbot-paperclip-rollout', 'plan_file', '/root/gt/strategiekreis/plans/quantbot-paperclip-rollout-final-implementation.md'),
('st-quantbot-paperclip-rollout', 'bead',      'qb-4m4'),
('st-end-to-end-ingest',          'plan_file', '/opt/solartown/docs/plans/end-to-end-ingest-vision.md'),
('st-end-to-end-ingest',          'plan_file', '/root/gt/strategiekreis/plans/end-to-end-ingest-vision-implementation.md'),
('st-end-to-end-ingest',          'github_pr', 'angelosystems/solartown#11'),
('st-staging-mode-c',             'plan_file', '/root/gt/strategiekreis/plans/solartown-staging-mode-c-2026-05-03-implementation.md'),
('st-reactor-fixes',              'github_pr', 'angelosystems/solartown#12'),
('st-reactor-fixes',              'github_pr', 'angelosystems/solartown#7'),
('st-reactor-fixes',              'github_pr', 'angelosystems/solartown#5'),
('st-mq-auto-merger',             'github_pr', 'angelosystems/solartown#4'),
('st-mq-auto-merger',             'github_pr', 'angelosystems/solartown#6'),
('qb-master-blueprint',           'plan_file', '/opt/quantbot/docs/blueprint/'),
('qb-kingdom-dashboard',          'plan_file', '/root/gt/strategiekreis/plans/kingdom-quantbot-zentrale-implementation.md'),
('qb-paperclip-consolidation',    'plan_file', '/opt/quantbot/research/warehouse/plans/paperclip-consolidation.md'),
('mb-master-kanban-build',        'plan_file', '/root/mario-brain/vault/projects/master-kanban-plan.md'),
('mb-master-kanban-build',        'plan_file', '/root/mario-brain/vault/projects/master-kanban-vision.md'),
('mb-master-kanban-build',        'plan_file', '/root/mario-brain/vault/projects/portfolio-inventur.md'),
('mb-vault-segmentation-p1',      'plan_file', '/root/mario-brain/vault/projects/vault-segmentation-audit.md'),
('ag-llm-sidecar-revival',        'bead',      'cl-aia40b'),
('ag-cockpit-architektur',        'plan_file', '/root/gt/strategiekreis/plans/0010-cockpit-architektur-implementation.md')
ON CONFLICT DO NOTHING;
