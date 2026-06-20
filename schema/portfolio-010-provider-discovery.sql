-- portfolio-010-provider-discovery.sql — Fleet Provider Discovery Schema und Seed
--
-- Erstellt die Tabelle portfolio.provider_discovery zur Klassifizierung von
-- Prozessen/Executors/Modellen auf entsprechende Provider-Buckets,
-- und befüllt diese sowie portfolio.provider_usage initial.
--
-- Idempotent — re-run sicher.

CREATE TABLE IF NOT EXISTS portfolio.provider_discovery (
    id serial PRIMARY KEY,
    process_pattern text,
    executor_pattern text,
    model_pattern text,
    provider_bucket text NOT NULL,
    priority integer NOT NULL DEFAULT 100,
    description text,
    CONSTRAINT uq_provider_discovery_rule UNIQUE (process_pattern, executor_pattern, model_pattern, provider_bucket)
);

CREATE INDEX IF NOT EXISTS idx_provider_discovery_priority ON portfolio.provider_discovery (priority DESC);

-- 1) Initialen Seed für provider_usage sicherstellen
INSERT INTO portfolio.provider_usage (provider_bucket) VALUES
('Claude'),
('Gemini'),
('DeepSeek'),
('GLM'),
('DeepSeek/GLM'),
('flows'),
('other')
ON CONFLICT (provider_bucket) DO NOTHING;

-- 2) Initialen Seed für provider_discovery sicherstellen
INSERT INTO portfolio.provider_discovery (process_pattern, executor_pattern, model_pattern, provider_bucket, priority, description)
SELECT val.process_pattern, val.executor_pattern, val.model_pattern, val.provider_bucket, val.priority, val.description
FROM (VALUES
    ('claude', NULL::text, NULL::text, 'Claude', 100, 'Claude Code oder Standard Claude CLI Prozesse'),
    ('gemini', NULL::text, NULL::text, 'Gemini', 100, 'Gemini CLI oder Solartown-Polecat Prozesse'),
    ('opencode', NULL::text, 'deepseek', 'DeepSeek', 110, 'opencode Prozesse mit DeepSeek Modell-Flag'),
    ('opencode', NULL::text, 'glm', 'GLM', 110, 'opencode Prozesse mit GLM Modell-Flag'),
    ('opencode', NULL::text, NULL::text, 'DeepSeek/GLM', 100, 'Allgemeiner Fallback für opencode Prozesse'),
    ('paperclip-worker', NULL::text, NULL::text, 'flows', 100, 'Paperclip-Worker Prozesse'),
    ('Paperclip-Worker', NULL::text, NULL::text, 'flows', 100, 'Alternative Schreibweise für Paperclip-Worker'),
    (NULL::text, 'flows', NULL::text, 'flows', 100, 'Zuweisung über den Executor flows'),
    (NULL::text, NULL::text, NULL::text, 'other', 0, 'Fallback für unklassifizierte Prozesse/Executors')
) AS val(process_pattern, executor_pattern, model_pattern, provider_bucket, priority, description)
WHERE NOT EXISTS (
    SELECT 1 FROM portfolio.provider_discovery pd
    WHERE (pd.process_pattern IS NOT DISTINCT FROM val.process_pattern)
      AND (pd.executor_pattern IS NOT DISTINCT FROM val.executor_pattern)
      AND (pd.model_pattern IS NOT DISTINCT FROM val.model_pattern)
      AND (pd.provider_bucket IS NOT DISTINCT FROM val.provider_bucket)
);

-- Berechtigungen vergeben
GRANT ALL ON portfolio.provider_discovery TO mario;
