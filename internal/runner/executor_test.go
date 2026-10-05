package runner

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestValidateRunControlAcceptsTypedApprovalAndRejectsUnsafeShapes(t *testing.T) {
	now := time.Now().UTC()
	valid := []byte(`{"approval":{"key":"publish-42","title":"Publish recording?","expiresAt":"` + now.Add(time.Hour).Format(time.RFC3339) + `","fields":[{"id":"title","label":"Title","type":"text","required":true}],"actions":[{"id":"approve","label":"Publish","style":"primary","requiresFields":true},{"id":"reject","label":"Skip","style":"neutral"}]}}`)
	control, err := validateRunControl(valid, now)
	if err != nil {
		t.Fatal(err)
	}
	if control.Approval == nil || control.Approval.Fields[0].Type != "text" || !control.Approval.Actions[0].RequiresFields {
		t.Fatalf("control=%#v", control)
	}
	invalid := []byte(`{"approval":{"key":"publish-42","title":"Publish?","expiresAt":"` + now.Add(time.Hour).Format(time.RFC3339) + `","fields":[{"id":"visibility","label":"Visibility","type":"select","options":[]}],"actions":[{"id":"execute","label":"Do it"}]}}`)
	if _, err := validateRunControl(invalid, now); err == nil {
		t.Fatal("unsafe approval shape was accepted")
	}
}

// approvalImage is a JPEG that decodes, padded after its end marker to size
// bytes so a fixture can sit exactly on a limit.
func approvalImage(size int) string {
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, image.NewGray(image.Rect(0, 0, 8, 8)), nil); err != nil {
		panic(err)
	}
	if buffer.Len() > size {
		panic(fmt.Sprintf("an approval image fixture takes at least %d bytes", buffer.Len()))
	}
	padded := append(buffer.Bytes(), make([]byte, size-buffer.Len())...)
	return domain.ApprovalImagePrefix + base64.StdEncoding.EncodeToString(padded)
}

func approvalControl(t *testing.T, now time.Time, fields ...map[string]any) []byte {
	t.Helper()
	control := map[string]any{"approval": map[string]any{
		"key": "publish-42", "title": "Publish recording?", "expiresAt": now.Add(time.Hour).Format(time.RFC3339),
		"fields":  fields,
		"actions": []map[string]any{{"id": "approve", "label": "Publish", "requiresFields": true}},
	}}
	encoded, err := json.Marshal(control)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestValidateRunControlAcceptsBoundedInlineJPEGImageField(t *testing.T) {
	now := time.Now().UTC()
	image := approvalImage(domain.MaxApprovalImageBytes)
	value := approvalControl(t, now,
		map[string]any{"id": "slide", "label": "Slide", "type": "image", "value": image},
		map[string]any{"id": "title", "label": "Title", "type": "text", "required": true},
	)
	if len(value) <= 64*1024 {
		t.Fatalf("fixture of %d bytes does not exercise a control larger than the text-only budget", len(value))
	}
	control, err := validateRunControl(value, now)
	if err != nil {
		t.Fatal(err)
	}
	if control.Approval.Fields[0].Type != "image" || control.Approval.Fields[0].Value != image {
		t.Fatalf("image field was not kept intact: %#v", control.Approval.Fields[0].Type)
	}
	four := make([]map[string]any, 0, 4)
	for index := range 4 {
		four = append(four, map[string]any{"id": fmt.Sprintf("frame-%d", index), "label": "Frame", "type": "image", "value": approvalImage(domain.MaxApprovalImagesBytes / 4)})
	}
	if _, err := validateRunControl(approvalControl(t, now, four...), now); err != nil {
		t.Fatalf("images filling the whole budget were refused: %v", err)
	}
}

func TestValidateRunControlRefusesUnsafeImageFields(t *testing.T) {
	now := time.Now().UTC()
	png := domain.ApprovalImagePrefix + base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nrest"))
	overBudget := make([]map[string]any, 0, 5)
	for index := range 4 {
		overBudget = append(overBudget, map[string]any{"id": fmt.Sprintf("frame-%d", index), "label": "Frame", "type": "image", "value": approvalImage(domain.MaxApprovalImagesBytes / 4)})
	}
	overBudget = append(overBudget, map[string]any{"id": "frame-4", "label": "Frame", "type": "image", "value": approvalImage(1024)})
	cases := []struct {
		name   string
		fields []map[string]any
		want   string
	}{
		{"oversized", []map[string]any{{"id": "slide", "label": "Slide", "type": "image", "value": approvalImage(domain.MaxApprovalImageBytes + 1)}}, "at most"},
		{"png bytes", []map[string]any{{"id": "slide", "label": "Slide", "type": "image", "value": png}}, "JPEG"},
		{"png media type", []map[string]any{{"id": "slide", "label": "Slide", "type": "image", "value": "data:image/png;base64,/9j/4A=="}}, "must start with"},
		{"invalid base64", []map[string]any{{"id": "slide", "label": "Slide", "type": "image", "value": domain.ApprovalImagePrefix + "/9j/4A=*"}}, "base64"},
		{"unpadded base64", []map[string]any{{"id": "slide", "label": "Slide", "type": "image", "value": domain.ApprovalImagePrefix + "/9j/4A"}}, "base64"},
		{"not a string", []map[string]any{{"id": "slide", "label": "Slide", "type": "image", "value": 42}}, "string"},
		{"no image", []map[string]any{{"id": "slide", "label": "Slide", "type": "image"}}, "require a value"},
		{"required", []map[string]any{{"id": "slide", "label": "Slide", "type": "image", "required": true, "value": approvalImage(1024)}}, "display-only"},
		{"options", []map[string]any{{"id": "slide", "label": "Slide", "type": "image", "options": []string{"a"}, "value": approvalImage(1024)}}, "options"},
		{"over the per-approval budget", overBudget, "together"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := approvalControl(t, now, tc.fields...)
			if len(value) > MaxRunControlBytes {
				t.Fatalf("fixture of %d bytes is refused by the file bound before the field check runs", len(value))
			}
			_, err := validateRunControl(value, now)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestValidateRunControlKeepsTextBudgetBesideImages(t *testing.T) {
	now := time.Now().UTC()
	padding := strings.Repeat("x", 64*1024)
	value := approvalControl(t, now, map[string]any{"id": "slide", "label": "Slide", "type": "image", "value": approvalImage(1024)})
	value = []byte(`{"defer":{"key":"next","until":"` + now.Add(time.Hour).Format(time.RFC3339) + `","data":{"padding":"` + padding + `"}},` + string(value[1:]))
	if _, err := validateRunControl(value, now); err == nil || !strings.Contains(err.Error(), "besides approval images") {
		t.Fatalf("error=%v, want the text budget to refuse 64 KiB of continuation data", err)
	}
}

func TestValidateRunControlAcceptsApprovalWithExpiryContinuation(t *testing.T) {
	now := time.Now().UTC()
	value := []byte(`{"defer":{"key":"recording-42.approval-expiry","until":"` + now.Add(7*24*time.Hour).Format(time.RFC3339) + `","data":{"jobId":"recording-42","step":"approval-expiry"}},"approval":{"key":"recording-42.publish","title":"Publish recording?","expiresAt":"` + now.Add(7*24*time.Hour).Format(time.RFC3339) + `","fields":[],"actions":[{"id":"approve","label":"Publish"}]}}`)
	control, err := validateRunControl(value, now)
	if err != nil {
		t.Fatal(err)
	}
	if control.Defer == nil || control.Approval == nil {
		t.Fatalf("control=%#v", control)
	}
}

func TestValidateRunControlAcceptsBoundedProviderNeutralNotifications(t *testing.T) {
	control, err := validateRunControl([]byte(`{"notifications":[{"key":"recording.started","title":"Recording started","body":"The Sunday service recording has started."},{"key":"recording.stopped","title":"Recording stopped","body":"Media processing can begin.","priority":"high"}]}`), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(control.Notifications) != 2 || control.Notifications[0].Priority != "default" {
		t.Fatalf("control=%#v", control)
	}
	for _, value := range []string{
		`{"notifications":[{"key":"same","title":"One","body":"Body"},{"key":"same","title":"Two","body":"Body"}]}`,
		`{"notifications":[{"key":"message","title":"Title","body":"Body","priority":"urgent"}]}`,
		`{"notifications":[{"key":"message","title":" ","body":"Body"}]}`,
	} {
		if _, err := validateRunControl([]byte(value), time.Now().UTC()); err == nil {
			t.Fatalf("invalid notification control was accepted: %s", value)
		}
	}
}

func TestValidateRunControlChargesEscapedImageBytesToTheImageBudget(t *testing.T) {
	now := time.Now().UTC()
	image := approvalImage(48 * 1024)
	value := approvalControl(t, now, map[string]any{"id": "slide", "label": "Slide", "type": "image", "value": image})
	encoded, err := json.Marshal(image)
	if err != nil {
		t.Fatal(err)
	}
	// The same string, spelled with every "A" as a JSON \u escape: what an
	// encoder that escapes more than it must is entitled to write.
	escaped := strings.ReplaceAll(string(encoded), "A", "\\u0041")
	value = bytes.Replace(value, encoded, []byte(escaped), 1)
	if len(value) <= 64*1024 || len(value) > MaxRunControlBytes {
		t.Fatalf("fixture of %d bytes does not sit between the text budget and the file bound", len(value))
	}
	control, err := validateRunControl(value, now)
	if err != nil {
		t.Fatalf("escaped image was charged to the text budget: %v", err)
	}
	if control.Approval.Fields[0].Value != image {
		t.Fatal("escaped image did not decode to the same value")
	}
}
