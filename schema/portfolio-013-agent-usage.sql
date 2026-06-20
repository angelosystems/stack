-- portfolio-013-agent-usage.sql — Agent Usage Schema
--
-- Erstellt die Tabelle portfolio.agent_usage zur Verfolgung von 
-- Token-Verbrauch, Request-Count und Overload-Events je Agent.
--
-- Idempotent — re-run sicher.

CREATE TABLE IF NOT EXISTS portfolio.agent_usage (
    agent_name text NOT NULL,
    provider_bucket text NOT NULL,
    input_tokens bigint NOT NULL DEFAULT 0,
    output_tokens bigint NOT NULL DEFAULT 0,
    cache_creation_tokens bigint NOT NULL DEFAULT 0,
    cache_read_tokens bigint NOT NULL DEFAULT 0,
    overload_events bigint NOT NULL DEFAULT 0,
    request_count bigint NOT NULL DEFAULT 0,
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_name, provider_bucket)
);

-- Berechtigungen vergeben
GRANT ALL ON portfolio.agent_usage TO mario;
