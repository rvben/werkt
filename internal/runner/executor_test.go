package runner

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"testing"
)

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
