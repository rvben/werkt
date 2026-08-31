package provenance

import (
	"encoding/base64"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/werkt/internal/domain"
)

func TestAttestorDetectsArtifactContentAndModeChanges(t *testing.T) {
	attestor, err := NewAttestor(base64.StdEncoding.EncodeToString(bytesOf(32, 7)))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "run.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho ok\n"), 0o750); err != nil {
		t.Fatal(err)
	}
	manifest := domain.Manifest{Metadata: domain.Metadata{Name: "signed-example"}, Runtime: domain.Runtime{
		Image: "python@sha256:" + strings.Repeat("a", 64),
	}}
	value, err := attestor.Attest(directory, manifest, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if value.Algorithm != AlgorithmEd25519 || value.ArtifactDigest == "" || value.Signature == "" || value.SigningKeyID == "" {
		t.Fatalf("incomplete provenance: %#v", value)
	}
	if err := attestor.Verify(directory, value); err != nil {
		t.Fatalf("verify original artifact: %v", err)
	}

	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := attestor.Verify(directory, value); !errors.Is(err, ErrArtifactDigestMismatch) {
		t.Fatalf("mode change error=%v", err)
	}
	if err := os.Chmod(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho changed\n"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := attestor.Verify(directory, value); !errors.Is(err, ErrArtifactDigestMismatch) {
		t.Fatalf("content change error=%v", err)
	}
}

func TestAttestorExcludesReservedMetadataFromArtifactDigest(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "main.py"), []byte("print('ok')\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	digest, err := DigestDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, MetadataDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, MetadataDirectory, "provenance.json"), []byte("metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := DigestDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	if after != digest {
		t.Fatalf("metadata changed digest from %s to %s", digest, after)
	}
}

func TestAttestorVersionTwoBindsResolvedToolEnvironment(t *testing.T) {
	attestor, err := NewAttestor(base64.StdEncoding.EncodeToString(bytesOf(32, 9)))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "main.py"), []byte("print('ok')\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	environment := &domain.ResolvedToolEnvironment{
		Version: 2, IdentityDigest: "sha256:" + strings.Repeat("a", 64),
		Image: "werkt-tools-test", ImageDigest: "sha256:" + strings.Repeat("b", 64),
		CatalogRevision: "test", PreparerRevision: "test", Platform: "linux-arm64",
		BaseImage: "minimal-base", BaseImageDigest: "sha256:" + strings.Repeat("c", 64),
		Installer: domain.ToolInstaller{Name: "mise", Version: "2026.8.1", Digest: "sha256:" + strings.Repeat("d", 64)},
		Tools: []domain.ResolvedTool{{Name: "python", Version: "3.13.7", Backend: "python",
			Executables: []domain.ResolvedToolExecutable{{Name: "python3", RelativePath: "bin/python3"}}, Capabilities: []string{"python3"}}},
		Capabilities: []string{"python3"},
	}
	manifest := domain.Manifest{Metadata: domain.Metadata{Name: "tools-example"}, Runtime: domain.Runtime{Tools: map[string]string{"python": "3.13.7"}, ResolvedTools: environment}}
	value, err := attestor.Attest(directory, manifest, strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	if value.Version != 2 || value.ToolEnvironment == nil {
		t.Fatalf("tool provenance = %#v", value)
	}
	if err := attestor.Verify(directory, value); err != nil {
		t.Fatalf("verify tool provenance: %v", err)
	}
	value.ToolEnvironment.ImageDigest = "sha256:" + strings.Repeat("f", 64)
	if err := attestor.Verify(directory, value); !errors.Is(err, ErrInvalidAttestation) {
		t.Fatalf("tampered tool digest error = %v", err)
	}
}

func TestValidToolEnvironmentPreservesV1AndRejectsUnsafeV2ExecutablePaths(t *testing.T) {
	base := &domain.ResolvedToolEnvironment{
		IdentityDigest: "sha256:" + strings.Repeat("a", 64), Image: "werkt-tools-test",
		ImageDigest: "sha256:" + strings.Repeat("b", 64), BaseImage: "minimal-base",
		BaseImageDigest: "sha256:" + strings.Repeat("c", 64),
		Installer:       domain.ToolInstaller{Digest: "sha256:" + strings.Repeat("d", 64)},
		Tools:           []domain.ResolvedTool{{Name: "python", Version: "3.13.7", Backend: "python"}},
	}
	base.Version = 1
	if !validToolEnvironment(base) {
		t.Fatal("legacy v1 tool environment rejected")
	}
	base.Version = 2
	base.Tools[0].Executables = []domain.ResolvedToolExecutable{{Name: "python3", RelativePath: "../bin/python3"}}
	if validToolEnvironment(base) {
		t.Fatal("v2 tool environment accepted path traversal")
	}
	base.Tools[0].Executables[0].RelativePath = "bin/python3"
	if !validToolEnvironment(base) {
		t.Fatal("valid v2 tool environment rejected")
	}
}

func TestLoadRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	directory := t.TempDir()
	metadataDirectory := filepath.Join(directory, MetadataDirectory)
	if err := os.Mkdir(metadataDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(metadataDirectory, metadataFilename)
	for _, encoded := range []string{
		`{"version":1,"unknown":true}`,
		`{"version":1} {"version":1}`,
	} {
		if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(directory); err == nil {
			t.Fatalf("Load accepted malformed metadata %q", encoded)
		}
	}
}

func TestLoadPreservesMissingMetadataError(t *testing.T) {
	if _, err := Load(t.TempDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing metadata error=%v", err)
	}
}

func TestPinHuskerImagesRequiresImmutableOCIReferences(t *testing.T) {
	digest := "sha256:" + strings.Repeat("c", 64)
	value := domain.Manifest{Runtime: domain.Runtime{Image: "python@" + digest}}
	pinned, err := PinHuskerImages(value)
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Runtime.Image != value.Runtime.Image || pinned.Runtime.BuildImage != "" {
		t.Fatalf("pinned manifest=%#v", pinned.Runtime)
	}

	for _, runtime := range []domain.Runtime{
		{},
		{Image: "python:3.13-alpine"},
		{Image: "python@" + digest, BuildImage: "golang:latest"},
	} {
		_, err := PinHuskerImages(domain.Manifest{Runtime: runtime})
		if !errors.Is(err, ErrUnpinnedImage) {
			t.Fatalf("runtime=%#v error=%v", runtime, err)
		}
	}
}

func TestNewAttestorRejectsMissingOrMalformedCustodyKey(t *testing.T) {
	for _, value := range []string{"", "not-base64", base64.StdEncoding.EncodeToString(bytesOf(31, 1))} {
		if _, err := NewAttestor(value); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("key %q error=%v", value, err)
		}
	}
}

func bytesOf(length int, value byte) []byte {
	result := make([]byte, length)
	for index := range result {
		result[index] = value
	}
	return result
}
