CREATE TABLE IF NOT EXISTS approvals (
    id TEXT PRIMARY KEY,
    automation_id TEXT NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
    revision_id TEXT NOT NULL REFERENCES revisions(id),
    requested_by_run_id TEXT NOT NULL UNIQUE REFERENCES runs(id),
    approval_key TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    fields JSONB NOT NULL DEFAULT '[]'::jsonb,
    actions JSONB NOT NULL DEFAULT '[]'::jsonb,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at TIMESTAMPTZ,
    resolved_by TEXT NOT NULL DEFAULT '',
    response JSONB,
    action_run_id TEXT REFERENCES runs(id),
    UNIQUE (automation_id, approval_key),
    CHECK (status IN ('pending', 'approved', 'rejected', 'expired')),
    CHECK (jsonb_typeof(fields) = 'array'),
    CHECK (jsonb_typeof(actions) = 'array')
);

CREATE INDEX IF NOT EXISTS approvals_status_created_idx
    ON approvals (status, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS approvals_automation_created_idx
    ON approvals (automation_id, created_at DESC, id DESC);
