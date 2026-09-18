CREATE TABLE investigations (
    request_id TEXT PRIMARY KEY,
    repository_id TEXT NOT NULL,
    issue_number BIGINT NOT NULL CHECK (issue_number > 0),
    spec JSONB NOT NULL,
    state JSONB NOT NULL,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX investigations_one_active_subject ON investigations (repository_id, issue_number)
    WHERE state->>'status' NOT IN ('completed', 'abandoned');
CREATE UNIQUE INDEX investigations_one_cloud_task ON investigations ((state->>'taskId'))
    WHERE state->>'taskId' IS NOT NULL;
CREATE INDEX investigations_subject_history ON investigations (repository_id, issue_number, created_at DESC, request_id DESC);

-- Retain every accepted version. Run/automation retention deliberately does not
-- delete these records or their issue-to-task associations.
CREATE TABLE investigation_versions (
    request_id TEXT NOT NULL REFERENCES investigations(request_id),
    version BIGINT NOT NULL,
    state JSONB NOT NULL,
    actor TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (request_id, version)
);
