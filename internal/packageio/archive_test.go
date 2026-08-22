package packageio

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteArchiveIsDeterministicAndRoundTrips(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "automation.yaml"), []byte("kind: Automation\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "bin"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "bin", "run"), []byte("#!/bin/sh\n"), 0o750); err != nil {
		t.Fatal(err)
	}

	var first, second bytes.Buffer
	firstDigest, err := WriteArchive(source, &first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := WriteArchive(source, &second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) || firstDigest != secondDigest {
		t.Fatal("archive output is not deterministic")
	}
	sum := sha256.Sum256(first.Bytes())
	if firstDigest != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest = %q", firstDigest)
	}

	archivePath := filepath.Join(t.TempDir(), "package.tar.gz")
	if err := os.WriteFile(archivePath, first.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := Extract(archivePath, destination, DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(destination, "bin", "run"))
	if err != nil || string(contents) != "#!/bin/sh\n" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	info, err := os.Stat(filepath.Join(destination, "bin", "run"))
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
}

func TestExtractRejectsTraversalLinksDuplicatesAndExpansion(t *testing.T) {
	tests := []struct {
		name    string
		headers []*tar.Header
		body    []byte
		limit   int64
		want    error
	}{
		{name: "traversal", headers: []*tar.Header{{Name: "../escape", Typeflag: tar.TypeReg, Size: 1}}, body: []byte("x"), limit: 10, want: ErrUnsafeArchive},
		{name: "symlink", headers: []*tar.Header{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}, limit: 10, want: ErrUnsafeArchive},
		{name: "duplicate", headers: []*tar.Header{{Name: "A", Typeflag: tar.TypeReg}, {Name: "a", Typeflag: tar.TypeReg}}, limit: 10, want: ErrUnsafeArchive},
		{name: "expanded", headers: []*tar.Header{{Name: "large", Typeflag: tar.TypeReg, Size: 4}}, body: []byte("data"), limit: 3, want: ErrExpandedLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archivePath := writeTestArchive(t, test.headers, test.body)
			limits := DefaultLimits()
			limits.ExpandedBytes = test.limit
			err := Extract(archivePath, t.TempDir(), limits)
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
}

func TestReceiveEnforcesCompressedLimitAndRemovesPartialFile(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "upload")
	_, err := Receive(bytes.NewReader([]byte("too large")), destination, 3)
	if !errors.Is(err, ErrCompressedLimit) {
		t.Fatalf("error=%v", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("partial upload remained: %v", statErr)
	}
}

func writeTestArchive(t *testing.T, headers []*tar.Header, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, header := range headers {
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := tarWriter.Write(body[:header.Size]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
