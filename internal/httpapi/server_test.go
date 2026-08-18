package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/domain"
	"gopkg.in/yaml.v3"
)

type fakeStore struct {
	listFilter       database.AutomationFilter
	listCalled       bool
	setEnabled       *bool
	setActor         string
	manualData       json.RawMessage
	manualExternalID string
	manualActor      string
	ingested         bool
	ingressConfig    json.RawMessage
}

func (s *fakeStore) Ping(context.Context) error { return nil }

func (s *fakeStore) IngestEvent(context.Context, string, string, string, string, time.Time, json.RawMessage, map[string]any) (string, bool, error) {
	s.ingested = true
	return "run_hook", true, nil
}

func (s *fakeStore) GetTriggerIngressPolicy(context.Context, string, string, string) (database.TriggerIngressPolicy, error) {
	config := s.ingressConfig
	if len(config) == 0 {
		config = json.RawMessage(`{"secretEnv":"TEST_WEBHOOK_SECRET","tokenEnv":"TEST_EMAIL_TOKEN"}`)
	}
	return database.TriggerIngressPolicy{Config: config}, nil
}

func (s *fakeStore) ListAutomations(_ context.Context, filter database.AutomationFilter) ([]database.AutomationSummary, error) {
	s.listCalled = true
	s.listFilter = filter
	return []database.AutomationSummary{{ID: "example", Project: "ops", Enabled: true}}, nil
}

func (s *fakeStore) GetAutomation(_ context.Context, automationID string) (database.AutomationDetail, error) {
	if automationID == "missing" {
		return database.AutomationDetail{}, database.ErrAutomationNotFound
	}
	return database.AutomationDetail{
		AutomationSummary: database.AutomationSummary{ID: automationID, Project: "ops", Enabled: true},
		Triggers:          []database.TriggerSummary{},
		Revisions:         []database.RevisionSummary{},
	}, nil
}

func (s *fakeStore) SetAutomationEnabled(_ context.Context, _ string, enabled bool, actor string) (bool, error) {
	s.setEnabled = &enabled
	s.setActor = actor
	return true, nil
}

func (s *fakeStore) EnqueueManualRun(_ context.Context, _ string, externalID string, data json.RawMessage, actor string) (string, bool, error) {
	s.manualExternalID = externalID
	s.manualData = append(json.RawMessage(nil), data...)
	s.manualActor = actor
	return "run_manual", true, nil
}

func (s *fakeStore) ListRunsFiltered(context.Context, string, string, int) ([]domain.Run, error) {
	return []domain.Run{}, nil
}

func (s *fakeStore) GetRun(_ context.Context, runID string) (domain.Run, error) {
	if runID == "missing" {
		return domain.Run{}, database.ErrRunNotFound
	}
	return domain.Run{ID: runID, Status: domain.RunSucceeded}, nil
}

func (s *fakeStore) ListAuditEvents(context.Context, string, int) ([]database.AuditEvent, error) {
	return []database.AuditEvent{}, nil
}

func TestManagementRoutesRequireBearerTokenButTriggerIngressDoesNot(t *testing.T) {
	const webhookSecret = "test-webhook-secret-at-least-32-bytes"
	t.Setenv("TEST_WEBHOOK_SECRET", webhookSecret)
	store := &fakeStore{}
	server := New(store, ":0", "management-secret")

	request := httptest.NewRequest(http.MethodGet, "/api/v1/automations", nil)
	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("management status = %d, want 401", response.Code)
	}
	if response.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("management response omitted WWW-Authenticate")
	}
	if store.listCalled {
		t.Fatal("unauthenticated request reached the store")
	}

	body := []byte(`{"ok":true}`)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	request = httptest.NewRequest(http.MethodPost, "/api/v1/hooks/example/incoming", strings.NewReader(string(body)))
	request.Header.Set("Idempotency-Key", "hook-test-1")
	request.Header.Set("X-Werkt-Timestamp", timestamp)
	request.Header.Set("X-Werkt-Signature", webhookSignature(webhookSecret, timestamp, "hook-test-1", body))
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("trigger status = %d, want 202", response.Code)
	}
	if !store.ingested {
		t.Fatal("public trigger ingress did not reach the store")
	}
}

func TestTriggerIngressRejectsInvalidCredentials(t *testing.T) {
	const webhookSecret = "test-webhook-secret-at-least-32-bytes"
	const emailToken = "test-email-token-at-least-32-bytes-long"
	t.Setenv("TEST_WEBHOOK_SECRET", webhookSecret)
	t.Setenv("TEST_EMAIL_TOKEN", emailToken)
	store := &fakeStore{}
	server := New(store, ":0", "")

	request := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/example/incoming", strings.NewReader(`{"ok":true}`))
	request.Header.Set("Idempotency-Key", "invalid-hook-test")
	request.Header.Set("X-Werkt-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	request.Header.Set("X-Werkt-Signature", "sha256=00")
	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || store.ingested {
		t.Fatalf("webhook status=%d ingested=%v", response.Code, store.ingested)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/email/example/mail", strings.NewReader("From: sender@example.com\nTo: automation@example.com\n\nhello"))
	request.Header.Set("Authorization", "Bearer wrong-token")
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || store.ingested {
		t.Fatalf("email status=%d ingested=%v", response.Code, store.ingested)
	}
}

func TestWebhookRejectsStaleSignatureAndMissingIdempotencyKey(t *testing.T) {
	const webhookSecret = "test-webhook-secret-at-least-32-bytes"
	t.Setenv("TEST_WEBHOOK_SECRET", webhookSecret)
	store := &fakeStore{}
	server := New(store, ":0", "")
	body := []byte(`{"ok":true}`)

	staleTimestamp := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/hooks/example/incoming", strings.NewReader(string(body)))
	request.Header.Set("Idempotency-Key", "stale-hook-test")
	request.Header.Set("X-Werkt-Timestamp", staleTimestamp)
	request.Header.Set("X-Werkt-Signature", webhookSignature(webhookSecret, staleTimestamp, "stale-hook-test", body))
	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || store.ingested {
		t.Fatalf("stale webhook status=%d ingested=%v", response.Code, store.ingested)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/hooks/example/incoming", strings.NewReader(string(body)))
	request.Header.Set("X-Werkt-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || store.ingested {
		t.Fatalf("missing-idempotency webhook status=%d ingested=%v", response.Code, store.ingested)
	}
}

func TestAuthenticatedEmailIngress(t *testing.T) {
	const emailToken = "test-email-token-at-least-32-bytes-long"
	t.Setenv("TEST_EMAIL_TOKEN", emailToken)
	store := &fakeStore{}
	server := New(store, ":0", "")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/email/example/mail", strings.NewReader("Message-Id: <test@example.com>\nFrom: sender@example.com\nTo: automation@example.com\nSubject: test\n\nhello"))
	request.Header.Set("Authorization", "Bearer "+emailToken)
	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || !store.ingested {
		t.Fatalf("status=%d ingested=%v body=%s", response.Code, store.ingested, response.Body.String())
	}
}

func TestAutomationInventoryAcceptsAuthenticatedFilters(t *testing.T) {
	store := &fakeStore{}
	server := New(store, ":0", "management-secret")
	request := httptest.NewRequest(http.MethodGet, "/api/v1/automations?project=ops&folder=alerts&label=critical&q=cpu&enabled=false", nil)
	request.Header.Set("Authorization", "Bearer management-secret")
	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !store.listCalled {
		t.Fatal("authenticated request did not reach store")
	}
	if store.listFilter.Project != "ops" || store.listFilter.Folder != "alerts" || store.listFilter.Label != "critical" || store.listFilter.Query != "cpu" {
		t.Fatalf("filter = %#v", store.listFilter)
	}
	if store.listFilter.Enabled == nil || *store.listFilter.Enabled {
		t.Fatalf("enabled filter = %v, want false", store.listFilter.Enabled)
	}
}

func TestManagementMutationsValidateInputAndPreserveActor(t *testing.T) {
	store := &fakeStore{}
	server := New(store, ":0", "management-secret")

	request := httptest.NewRequest(http.MethodPatch, "/api/v1/automations/example", strings.NewReader(`{"enabled":false,"unknown":true}`))
	request.Header.Set("Authorization", "Bearer management-secret")
	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown-field status = %d, want 400", response.Code)
	}
	if store.setEnabled != nil {
		t.Fatal("invalid mutation reached store")
	}

	request = httptest.NewRequest(http.MethodPatch, "/api/v1/automations/example", strings.NewReader(`{"enabled":false}`))
	request.Header.Set("Authorization", "Bearer management-secret")
	request.Header.Set("X-Werkt-Actor", "agent:operator")
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", response.Code, response.Body.String())
	}
	if store.setEnabled == nil || *store.setEnabled || store.setActor != "agent:operator" {
		t.Fatalf("enabled = %v, actor = %q", store.setEnabled, store.setActor)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/automations/example/runs", strings.NewReader(`{"reason":"diagnostic"}`))
	request.Header.Set("Authorization", "Bearer management-secret")
	request.Header.Set("Idempotency-Key", "diagnostic-1")
	request.Header.Set("X-Werkt-Actor", "agent:operator")
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("manual-run status = %d, body = %s", response.Code, response.Body.String())
	}
	if store.manualExternalID != "diagnostic-1" || store.manualActor != "agent:operator" || string(store.manualData) != `{"reason":"diagnostic"}` {
		t.Fatalf("manual run external=%q actor=%q data=%s", store.manualExternalID, store.manualActor, store.manualData)
	}
}

func TestManagementQueryValidation(t *testing.T) {
	store := &fakeStore{}
	server := New(store, ":0", "")
	tests := []string{
		"/api/v1/automations?enabled=sometimes",
		"/api/v1/runs?limit=501",
		"/api/v1/runs?status=unknown",
	}
	for _, target := range tests {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		server.server.Handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400", target, response.Code)
		}
	}
}

func TestOpenAPIContractIsPublicAndDocumentsManagementRoutes(t *testing.T) {
	server := New(&fakeStore{}, ":0", "management-secret")
	request := httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil)
	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "application/yaml" {
		t.Fatalf("content type = %q", response.Header().Get("Content-Type"))
	}
	var document struct {
		OpenAPI string                    `yaml:"openapi"`
		Paths   map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode OpenAPI: %v", err)
	}
	if document.OpenAPI != "3.1.0" {
		t.Fatalf("openapi = %q", document.OpenAPI)
	}
	for _, path := range []string{
		"/api/v1/automations", "/api/v1/automations/{automation}",
		"/api/v1/automations/{automation}/runs", "/api/v1/runs", "/api/v1/audit",
	} {
		if _, exists := document.Paths[path]; !exists {
			t.Errorf("OpenAPI path %q is missing", path)
		}
	}
}

func webhookSignature(secret, timestamp, idempotencyKey string, body []byte) string {
	digest := hmac.New(sha256.New, []byte(secret))
	_, _ = digest.Write([]byte(timestamp + "." + idempotencyKey + "."))
	_, _ = digest.Write(body)
	return "sha256=" + hex.EncodeToString(digest.Sum(nil))
}
