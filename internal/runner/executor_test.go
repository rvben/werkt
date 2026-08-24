package runner

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rvben/werkt/internal/domain"
	"github.com/rvben/werkt/internal/provenance"
)

type countingExecutor struct {
	calls int
}

func (e *countingExecutor) Execute(context.Context, domain.RunnableRun) (Result, error) {
	e.calls++
	return Result{Output: []byte(`{}`)}, nil
}

func TestVerifyingExecutorStopsTamperedArtifactBeforeIsolationBoundary(t *testing.T) {
	attestor, err := provenance.NewAttestor(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "main")
	if err := os.WriteFile(path, []byte("original"), 0o750); err != nil {
		t.Fatal(err)
	}
	manifest := domain.Manifest{Metadata: domain.Metadata{Name: "verified"}}
	attestation, err := attestor.Attest(directory, manifest, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	next := &countingExecutor{}
	executor := NewVerifyingExecutor(next, attestor)
	run := domain.RunnableRun{ArtifactPath: directory, Provenance: attestation, Manifest: manifest}
	if _, err := executor.Execute(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(context.Background(), run); !errors.Is(err, provenance.ErrArtifactDigestMismatch) {
		t.Fatalf("tampered execution error=%v", err)
	}
	if next.calls != 1 {
		t.Fatalf("inner executor calls=%d", next.calls)
	}
}

func TestLogRedactorMasksPlainAndCommonEncodedSecretForms(t *testing.T) {
	secret := "token with+/symbols"
	redactor := newLogRedactor([]string{secret})
	input := strings.Join([]string{
		secret,
		url.QueryEscape(secret),
		base64.StdEncoding.EncodeToString([]byte(secret)),
		base64.RawStdEncoding.EncodeToString([]byte(secret)),
		`token with+/symbols`,
	}, "\n")
	redacted := redactor.Redact(input)
	if strings.Contains(redacted, secret) || strings.Contains(redacted, base64.StdEncoding.EncodeToString([]byte(secret))) {
		t.Fatalf("redacted logs still contain secret material: %q", redacted)
	}
	if count := strings.Count(redacted, "[REDACTED]"); count != 5 {
		t.Fatalf("redaction count=%d, want 5 in %q", count, redacted)
	}
	if safe := redactor.Error(errors.New("request contained " + secret)); strings.Contains(safe.Error(), secret) {
		t.Fatalf("redacted error still contains secret: %v", safe)
	}
}

func TestLogRedactionHappensBeforeTruncation(t *testing.T) {
	secret := "boundary-secret-value"
	redactor := newLogRedactor([]string{secret})
	stdout := strings.Repeat("x", maxLogs-8) + secret
	logs := formatLogs(redactor.Redact(stdout), "")
	if strings.Contains(logs, secret) {
		t.Fatal("truncated logs retained a boundary secret")
	}
	if !strings.Contains(logs, "[logs truncated]") {
		t.Fatal("expected log truncation marker")
	}
}
