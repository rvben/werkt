-- A deployment's identity is the normalized content of its package, not the
-- compressed bytes it arrived in. package_digest stays the transport integrity
-- check for the upload; content_digest is what an idempotency key promises is
-- unchanged. An empty value means the row predates this column and its content
-- is unknown, which is not the same as matching.
ALTER TABLE deployments ADD COLUMN content_digest TEXT NOT NULL DEFAULT '';
