ALTER TABLE deployments DROP CONSTRAINT deployments_status_check;
ALTER TABLE deployments ADD CONSTRAINT deployments_status_check
    CHECK (status IN ('queued', 'validating', 'building', 'checking', 'activating', 'succeeded', 'failed', 'cancelled'));

ALTER TABLE deployments
    ADD COLUMN retry_of TEXT REFERENCES deployments(id),
    ADD COLUMN cancel_requested_at TIMESTAMPTZ;

CREATE TABLE deployment_steps (
    deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('validate', 'build', 'check', 'activate')),
    status TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    logs TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    PRIMARY KEY (deployment_id, id)
);

CREATE UNIQUE INDEX deployment_steps_position_idx ON deployment_steps (deployment_id, position);

DROP INDEX deployments_queue_idx;
CREATE INDEX deployments_queue_idx ON deployments (created_at)
    WHERE status IN ('queued', 'validating', 'building', 'checking', 'activating');

CREATE INDEX deployments_retry_idx ON deployments (retry_of, created_at DESC)
    WHERE retry_of IS NOT NULL;
