-- portfolio-009-fleet-parser.sql — Fleet Parser Schema
--
-- Erstellt transcript_offset und provider_usage Tabellen für den
-- inkrementellen Transkript-429-Parser.
--
-- Idempotent — re-run sicher.

CREATE TABLE IF NOT EXISTS portfolio.transcript_offset (
    file_path text PRIMARY KEY,
    last_offset bigint NOT NULL,
    updated_at timestamp with time zone NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS portfolio.provider_usage (
    provider_bucket text PRIMARY KEY,
    input_tokens bigint NOT NULL DEFAULT 0,
    output_tokens bigint NOT NULL DEFAULT 0,
    cache_creation_tokens bigint NOT NULL DEFAULT 0,
    cache_read_tokens bigint NOT NULL DEFAULT 0,
    overload_events bigint NOT NULL DEFAULT 0,
    updated_at timestamp with time zone NOT NULL DEFAULT now()
);

-- Berechtigungen vergeben
GRANT ALL ON portfolio.transcript_offset TO mario;
GRANT ALL ON portfolio.provider_usage TO mario;
