package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/werkt/internal/domain"
	"github.com/rvben/werkt/internal/provenance"
)

type recordingBuilder struct {
	calls int
}

func (b *recordingBuilder) Build(_ context.Context, directory string, _ domain.Manifest, _ domain.DeploymentStepReporter) error {
	b.calls++
	return os.WriteFile(filepath.Join(directory, "built.txt"), []byte("immutable output\n"), 0o640)
}

func TestBuildArtifactAttestsPublishedTreeAndRefusesTamperedReuse(t *testing.T) {
	attestor, err := provenance.NewAttestor(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "main.py"), []byte("print('ok')\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	builder := &recordingBuilder{}
	deployer := &Deployer{dataDir: t.TempDir(), builder: builder, attestor: attestor}
	prepared := PreparedDeployment{
		SourceDirectory: source,
		ContentHash:     strings.Repeat("a", 64),
		Manifest: domain.Manifest{
			Metadata: domain.Metadata{Name: "attested-example"},
			Runtime:  domain.Runtime{Build: []string{"build"}, Command: []string{"run"}},
		},
	}
	built, err := deployer.BuildArtifact(context.Background(), prepared, nil)
	if err != nil {
		t.Fatal(err)
	}
	if builder.calls != 1 || built.Provenance.ArtifactDigest == "" {
		t.Fatalf("builder calls=%d provenance=%#v", builder.calls, built.Provenance)
	}
	if err := attestor.Verify(built.ArtifactPath, built.Provenance); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(built.ArtifactPath, provenance.MetadataDirectory, "provenance.json")); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(built.ArtifactPath, "main.py"), []byte("tampered\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	_, err = deployer.BuildArtifact(context.Background(), prepared, nil)
	if !errors.Is(err, provenance.ErrArtifactDigestMismatch) {
		t.Fatalf("tampered reuse error=%v", err)
	}
	if builder.calls != 1 {
		t.Fatalf("builder ran before retained artifact verification: calls=%d", builder.calls)
	}
}

func TestCopyDirectoryRejectsReservedProvenanceMetadata(t *testing.T) {
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, provenance.MetadataDirectory), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := copyDirectory(source, t.TempDir()); err == nil || !strings.Contains(err.Error(), "reserved .werkt") {
		t.Fatalf("error=%v", err)
	}
}
