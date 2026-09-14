-- Storage locations move from absolute paths to locations relative to the data
-- directory. An absolute path outlives the directory it was written under: move
-- the data root, or restore a recovery bundle anywhere other than the host it
-- came from, and every row names storage that is no longer there while the
-- artifacts themselves are intact and present.
--
-- The rewrite keys on the layout the control plane owns (<data dir>/artifacts,
-- <data dir>/deployment-sources) rather than on any one directory, so it also
-- repairs rows already orphaned by a move. The greedy prefix takes the last
-- occurrence, which is the deepest, so a data directory that itself contains a
-- path segment of the same name still relativizes correctly. Rows that do not
-- carry the segment at all are left exactly as they are: rewriting one into
-- something plausible would replace a location that can be reported as broken
-- with one that quietly resolves to the wrong place.
--
-- Rolling back to a binary that reads these columns as absolute paths requires
-- restoring the rows as well; the artifact digest covers content and relative
-- paths only, so the storage itself is unaffected either way.

UPDATE revisions
SET artifact_path = regexp_replace(artifact_path, '^.*/artifacts/', 'artifacts/')
WHERE artifact_path LIKE '/%/artifacts/%';

UPDATE deployments
SET source_path = regexp_replace(source_path, '^.*/deployment-sources/', 'deployment-sources/')
WHERE source_path LIKE '/%/deployment-sources/%';

UPDATE retention_items
SET storage_path = regexp_replace(storage_path, '^.*/artifacts/', 'artifacts/')
WHERE storage_path LIKE '/%/artifacts/%';

UPDATE retention_items
SET storage_path = regexp_replace(storage_path, '^.*/deployment-sources/', 'deployment-sources/')
WHERE storage_path LIKE '/%/deployment-sources/%';
