package python

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallProducesImportablePackageAndRejectsCollision(t *testing.T) {
	root := t.TempDir()
	if err := Install(root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"werkt/__init__.py", "werkt/runtime.py", "werkt/connectors/base.py"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(ArtifactDirectory), filepath.FromSlash(path))); err != nil {
			t.Fatalf("installed %s: %v", path, err)
		}
	}
	if err := Install(root); err == nil {
		t.Fatal("SDK artifact collision was accepted")
	}
}

func TestRevisionHashChangesPackageIdentity(t *testing.T) {
	if Digest() == "" || RevisionHash("package") == RevisionHash("other") || RevisionHash("package") == "package" {
		t.Fatal("SDK digest is not part of the revision hash")
	}
}
