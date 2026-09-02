CREATE TABLE IF NOT EXISTS notification_events (
    id TEXT PRIMARY KEY,
    event_type TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expanded_at TIMESTAMPTZ,
    UNIQUE (event_type, subject_id),
    CHECK (status IN ('pending', 'expanded', 'skipped')),
    CHECK (jsonb_typeof(payload) = 'object')
);

CREATE INDEX IF NOT EXISTS notification_events_pending_idx
    ON notification_events (created_at, id)
    WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS notification_deliveries (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL REFERENCES notification_events(id) ON DELETE CASCADE,
    destination_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    provider_config JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued',
    attempt INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 5,
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_owner TEXT,
    lease_expires_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    response_code INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at TIMESTAMPTZ,
    UNIQUE (event_id, destination_id),
    CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
    CHECK (jsonb_typeof(provider_config) = 'object')
);

CREATE INDEX IF NOT EXISTS notification_deliveries_queue_idx
    ON notification_deliveries (available_at, created_at, id)
    WHERE status IN ('queued', 'running');

ALTER TABLE approvals
    ADD COLUMN IF NOT EXISTS expiring_notified_at TIMESTAMPTZ;
