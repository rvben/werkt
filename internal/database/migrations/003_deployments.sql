CREATE TABLE deployments (
    id TEXT PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE,
    package_digest TEXT NOT NULL,
    source_path TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'validating', 'building', 'activating', 'succeeded', 'failed')),
    automation_id TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL DEFAULT '',
    revision_id TEXT REFERENCES revisions(id),
    actor TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    lease_owner TEXT,
    lease_expires_at TIMESTAMPTZ
);

CREATE INDEX deployments_queue_idx ON deployments (created_at)
    WHERE status IN ('queued', 'validating', 'building', 'activating');
CREATE INDEX deployments_automation_idx ON deployments (automation_id, created_at DESC);
