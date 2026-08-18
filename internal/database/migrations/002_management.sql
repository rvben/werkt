ALTER TABLE automations
    ADD COLUMN enabled BOOLEAN NOT NULL DEFAULT true;

CREATE TABLE audit_events (
    id TEXT PRIMARY KEY,
    action TEXT NOT NULL,
    automation_id TEXT NOT NULL,
    actor TEXT NOT NULL,
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX audit_events_created_idx ON audit_events (created_at DESC, id DESC);
CREATE INDEX audit_events_automation_idx ON audit_events (automation_id, created_at DESC);
