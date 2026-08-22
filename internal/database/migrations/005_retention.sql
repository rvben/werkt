CREATE TABLE retention_plans (
    id TEXT PRIMARY KEY,
    status TEXT NOT NULL CHECK (status IN ('planned', 'applying', 'applied')),
    policy JSONB NOT NULL,
    actor TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    lease_expires_at TIMESTAMPTZ,
    applied_at TIMESTAMPTZ
);

CREATE TABLE retention_items (
    plan_id TEXT NOT NULL REFERENCES retention_plans(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('source', 'artifact')),
    storage_key TEXT NOT NULL,
    storage_path TEXT NOT NULL,
    resource_ids JSONB NOT NULL,
    automation_ids JSONB NOT NULL,
    reason TEXT NOT NULL,
    estimated_bytes BIGINT NOT NULL DEFAULT 0 CHECK (estimated_bytes >= 0),
    status TEXT NOT NULL CHECK (status IN ('planned', 'deleted', 'skipped', 'failed')),
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    PRIMARY KEY (plan_id, position),
    UNIQUE (plan_id, kind, storage_path)
);

CREATE INDEX retention_plans_created_idx ON retention_plans (created_at DESC, id DESC);
