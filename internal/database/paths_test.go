package database

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testStorageFixture creates a storage directory of the given kind inside the
// data directory, which is where every recorded location has to live.
func testStorageFixture(t *testing.T, dataDir, kind, name string) string {
	t.Helper()
	path := filepath.Join(dataDir, kind, name)
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStorageLocationsAreRecordedRelativeToTheDataDirectory(t *testing.T) {
	dataDir := t.TempDir()
	store := &Store{dataDir: dataDir}

	for name, path := range map[string]string{
		"artifact": filepath.Join(dataDir, "artifacts", strings.Repeat("a", 64)),
		"source":   filepath.Join(dataDir, "deployment-sources", "dep_1"),
	} {
		t.Run(name, func(t *testing.T) {
			recorded, err := store.relativeStorageLocation(path)
			if err != nil {
				t.Fatalf("relativeStorageLocation(%q) error = %v", path, err)
			}
			if filepath.IsAbs(recorded) || strings.Contains(recorded, dataDir) {
				t.Fatalf("recorded location %q still carries the data directory", recorded)
			}
			resolved, err := store.resolveStorageLocation(recorded)
			if err != nil {
				t.Fatalf("resolveStorageLocation(%q) error = %v", recorded, err)
			}
			if resolved != path {
				t.Fatalf("resolved = %q, want %q", resolved, path)
			}
		})
	}
}

func TestRecordedStorageLocationsFollowTheDataDirectoryToANewRoot(t *testing.T) {
	original := t.TempDir()
	moved := t.TempDir()
	contentHash := strings.Repeat("b", 64)

	recorded, err := (&Store{dataDir: original}).relativeStorageLocation(filepath.Join(original, "artifacts", contentHash))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := (&Store{dataDir: moved}).resolveStorageLocation(recorded)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(moved, "artifacts", contentHash); resolved != want {
		t.Fatalf("resolved = %q, want %q", resolved, want)
	}
}

func TestDetachedStorageStaysDetached(t *testing.T) {
	store := &Store{dataDir: t.TempDir()}
	recorded, err := store.relativeStorageLocation("")
	if err != nil || recorded != "" {
		t.Fatalf("relativeStorageLocation(\"\") = %q, %v", recorded, err)
	}
	resolved, err := store.resolveStorageLocation("")
	if err != nil || resolved != "" {
		t.Fatalf("resolveStorageLocation(\"\") = %q, %v", resolved, err)
	}
}

func TestStorageLocationsOutsideTheDataDirectoryAreRefused(t *testing.T) {
	dataDir := t.TempDir()
	store := &Store{dataDir: dataDir}

	// Writing any of these would record a location the store cannot resolve
	// back to the storage it was handed.
	for name, path := range map[string]string{
		"sibling directory": filepath.Join(filepath.Dir(dataDir), "elsewhere", "artifacts", "x"),
		"parent directory":  filepath.Dir(dataDir),
		"data directory":    dataDir,
		"absolute elsewhere": filepath.Join(string(filepath.Separator), "var", "lib", "werkt", "data",
			"artifacts", strings.Repeat("c", 64)),
	} {
		t.Run(name, func(t *testing.T) {
			if recorded, err := store.relativeStorageLocation(path); err == nil {
				t.Fatalf("relativeStorageLocation(%q) = %q, want an error", path, recorded)
			}
		})
	}
}

func TestRecordedStorageLocationsThatCannotBeResolvedAreRefused(t *testing.T) {
	dataDir := t.TempDir()
	store := &Store{dataDir: dataDir}

	for name, recorded := range map[string]string{
		// A row written before storage locations were relativized. Joining it
		// would name a directory under the data directory that never existed.
		"absolute path": "/var/lib/werkt/data/artifacts/" + strings.Repeat("d", 64),
		"escape":        "../elsewhere/artifacts/" + strings.Repeat("e", 64),
		"escape after cleaning": "artifacts/../../elsewhere/artifacts/" + strings.Repeat("f", 64),
		"the data directory itself": "artifacts/..",
	} {
		t.Run(name, func(t *testing.T) {
			resolved, err := store.resolveStorageLocation(recorded)
			if err == nil {
				t.Fatalf("resolveStorageLocation(%q) = %q, want an error", recorded, resolved)
			}
			if strings.HasPrefix(resolved, dataDir) {
				t.Fatalf("refused location still returned a path: %q", resolved)
			}
		})
	}
}
