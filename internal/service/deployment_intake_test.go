package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/werkt/internal/packageio"
)

func TestDeploymentIntakeRejectsInvalidMetadataBeforeReadingBody(t *testing.T) {
	intake := NewDeploymentIntake(nil, t.TempDir(), packageio.DefaultLimits())
	tests := []struct {
		name           string
		digest         string
		idempotencyKey string
		want           error
	}{
		{name: "missing digest", idempotencyKey: "deploy-1", want: ErrInvalidDeploymentDigest},
		{name: "short digest", digest: "abcd", idempotencyKey: "deploy-1", want: ErrInvalidDeploymentDigest},
		{name: "missing idempotency key", digest: strings.Repeat("a", 64), want: ErrInvalidIdempotencyKey},
		{name: "control in idempotency key", digest: strings.Repeat("a", 64), idempotencyKey: "deploy\n1", want: ErrInvalidIdempotencyKey},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := intake.Accept(context.Background(), bytes.NewReader(nil), test.digest, test.idempotencyKey, "agent:test")
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
}

func TestDeploymentIntakeRejectsDigestMismatchAndCleansStaging(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "automation.yaml"), []byte("kind: Automation\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if _, err := packageio.WriteArchive(source, &archive); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	intake := NewDeploymentIntake(nil, dataDir, packageio.DefaultLimits())
	_, _, err := intake.Accept(context.Background(), &archive, strings.Repeat("f", 64), "deploy-1", "agent:test")
	if !errors.Is(err, ErrDeploymentDigestMismatch) {
		t.Fatalf("error=%v", err)
	}
	uploads, err := os.ReadDir(filepath.Join(dataDir, "deployment-uploads"))
	if err != nil {
		t.Fatal(err)
	}
	if len(uploads) != 0 {
		t.Fatalf("temporary uploads remained: %v", uploads)
	}
	sources, err := os.ReadDir(filepath.Join(dataDir, "deployment-sources"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 0 {
		t.Fatalf("temporary sources remained: %v", sources)
	}
}
