CREATE TABLE IF NOT EXISTS automation_state (
    automation_id TEXT PRIMARY KEY REFERENCES automations(id) ON DELETE CASCADE,
    version BIGINT NOT NULL DEFAULT 0 CHECK (version >= 0),
    value JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(value) = 'object'),
    updated_by_run_id TEXT REFERENCES runs(id),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
