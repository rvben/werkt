CREATE TABLE secrets (
    name TEXT PRIMARY KEY,
    description TEXT NOT NULL DEFAULT '',
    ciphertext BYTEA NOT NULL,
    key_id TEXT NOT NULL,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE revision_secret_references (
    revision_id TEXT NOT NULL REFERENCES revisions(id) ON DELETE CASCADE,
    secret_name TEXT NOT NULL REFERENCES secrets(name) ON DELETE RESTRICT,
    purpose TEXT NOT NULL,
    PRIMARY KEY (revision_id, secret_name, purpose)
);

CREATE INDEX revision_secret_references_secret_idx
    ON revision_secret_references (secret_name, revision_id);
