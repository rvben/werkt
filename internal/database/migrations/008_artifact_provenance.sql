ALTER TABLE revisions
    ADD COLUMN provenance JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE deployments
    ADD COLUMN provenance JSONB NOT NULL DEFAULT '{}'::jsonb;
