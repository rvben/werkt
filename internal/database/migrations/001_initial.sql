CREATE TABLE IF NOT EXISTS automations (
    id TEXT PRIMARY KEY,
    project TEXT NOT NULL,
    folder TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    labels JSONB NOT NULL DEFAULT '[]'::jsonb,
    active_revision_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS revisions (
    id TEXT PRIMARY KEY,
    automation_id TEXT NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
    content_hash TEXT NOT NULL,
    manifest JSONB NOT NULL,
    artifact_path TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (automation_id, content_hash)
);

CREATE TABLE IF NOT EXISTS triggers (
    automation_id TEXT NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    revision_id TEXT NOT NULL REFERENCES revisions(id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    enabled BOOLEAN NOT NULL DEFAULT true,
    next_fire_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (automation_id, id)
);

CREATE INDEX IF NOT EXISTS triggers_due_schedule_idx
    ON triggers (next_fire_at)
    WHERE type = 'schedule' AND enabled = true;

CREATE TABLE IF NOT EXISTS events (
    id TEXT PRIMARY KEY,
    trigger_key TEXT NOT NULL,
    external_id TEXT NOT NULL,
    envelope JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (trigger_key, external_id)
);

CREATE TABLE IF NOT EXISTS runs (
    id TEXT PRIMARY KEY,
    automation_id TEXT NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
    revision_id TEXT NOT NULL REFERENCES revisions(id),
    event_id TEXT NOT NULL UNIQUE REFERENCES events(id),
    status TEXT NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 1,
    concurrency_policy TEXT NOT NULL DEFAULT 'allow',
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_owner TEXT,
    lease_expires_at TIMESTAMPTZ,
    logs TEXT NOT NULL DEFAULT '',
    result JSONB,
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS runs_queue_idx ON runs (available_at, created_at)
    WHERE status IN ('queued', 'running');
CREATE INDEX IF NOT EXISTS runs_automation_idx ON runs (automation_id, created_at DESC);
