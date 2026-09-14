package database

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Storage locations are recorded relative to the data directory, so the
// directory is owned by configuration and not frozen into every row. A tree
// that moves, or a recovery bundle restored somewhere other than the host it
// came from, keeps its rows naming the artifacts it actually carries; recorded
// absolute, those rows outlive the directory they were written under and point
// at storage that is no longer there.
//
// The translation lives here rather than at each call site so that callers go
// on handing the store absolute paths and receiving them back, and so that no
// reader can forget to join. Writes are strict: a location outside the data
// directory is refused rather than recorded as something the store cannot
// resolve later.

// relativeStorageLocation records an absolute path as a location inside the
// data directory. An empty path means detached storage, which retention writes
// deliberately, and stays empty.
func (s *Store) relativeStorageLocation(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve storage path %q: %w", path, err)
	}
	relative, err := filepath.Rel(s.dataDir, absolute)
	if err != nil || !isContainedRelativePath(relative) {
		return "", fmt.Errorf("storage path %q is outside the data directory %s", path, s.dataDir)
	}
	return filepath.ToSlash(relative), nil
}

// resolveStorageLocation turns a recorded location back into a filesystem path
// under the data directory this store was opened with.
func (s *Store) resolveStorageLocation(recorded string) (string, error) {
	if recorded == "" {
		return "", nil
	}
	local := filepath.FromSlash(recorded)
	// An absolute value is a row written before storage locations were
	// relativized. Joining it would silently produce a path under the data
	// directory that has never existed, so say what is actually wrong.
	if filepath.IsAbs(local) || strings.HasPrefix(recorded, "/") {
		return "", fmt.Errorf("storage location %q is recorded as an absolute path and cannot be resolved against the data directory %s", recorded, s.dataDir)
	}
	resolved := filepath.Join(s.dataDir, local)
	// Join cleans the result, so a recorded "../" would otherwise escape here
	// without any of the callers noticing.
	relative, err := filepath.Rel(s.dataDir, resolved)
	if err != nil || !isContainedRelativePath(relative) {
		return "", fmt.Errorf("storage location %q escapes the data directory %s", recorded, s.dataDir)
	}
	return resolved, nil
}

// isContainedRelativePath reports whether a filepath.Rel result names something
// strictly inside the directory it was taken against. "." is the directory
// itself, which is never a storage location.
func isContainedRelativePath(relative string) bool {
	if relative == "" || relative == "." || relative == ".." || filepath.IsAbs(relative) {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
