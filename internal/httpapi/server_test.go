package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/domain"
	"github.com/rvben/werkt/internal/service"
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
		config = json.RawMessage(`{"secret":"tests/webhook","tokenSecret":"tests/email"}`)
	}
	return database.TriggerIngressPolicy{Config: config}, nil
}

type fakeSecretManager struct {
	values   map[string]string
	metadata map[string]domain.SecretMetadata
}

func testSecretManager(values map[string]string) *fakeSecretManager {
	return &fakeSecretManager{values: values, metadata: make(map[string]domain.SecretMetadata)}
}

func (m *fakeSecretManager) Resolve(_ context.Context, names []string) (map[string]string, error) {
	values := make(map[string]string, len(names))
	for _, name := range names {
		value, ok := m.values[name]
		if !ok {
			return nil, database.ErrSecretNotFound
		}
		values[name] = value
	}
	return values, nil
}

func (m *fakeSecretManager) List(context.Context) ([]domain.SecretMetadata, error) {
	values := make([]domain.SecretMetadata, 0, len(m.metadata))
	for _, value := range m.metadata {
		values = append(values, value)
	}
	return values, nil
}

func (m *fakeSecretManager) Get(_ context.Context, name string) (domain.SecretMetadata, error) {
	value, ok := m.metadata[name]
	if !ok {
		return domain.SecretMetadata{}, database.ErrSecretNotFound
	}
	return value, nil
}

func (m *fakeSecretManager) Put(_ context.Context, name, value, description, _ string) (domain.SecretMetadata, bool, error) {
	current, exists := m.metadata[name]
	current.Name = name
	current.Description = description
	current.Version++
	m.metadata[name] = current
	if m.values == nil {
		m.values = make(map[string]string)
	}
	m.values[name] = value
	return current, !exists, nil
}

func (m *fakeSecretManager) Delete(_ context.Context, name, _ string) error {
	if _, ok := m.metadata[name]; !ok {
		return database.ErrSecretNotFound
	}
	delete(m.metadata, name)
	delete(m.values, name)
	return nil
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

func (s *fakeStore) RollbackAutomation(context.Context, string, string, string) (bool, error) {
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

func (s *fakeStore) ListDeploymentsFiltered(context.Context, string, string, int) ([]domain.Deployment, error) {
	return []domain.Deployment{{ID: "dep_example", Status: domain.DeploymentSucceeded}}, nil
}

func (s *fakeStore) GetDeployment(_ context.Context, deploymentID string) (domain.Deployment, error) {
	if deploymentID == "missing" {
		return domain.Deployment{}, database.ErrDeploymentNotFound
	}
	return domain.Deployment{ID: deploymentID, Status: domain.DeploymentSucceeded}, nil
}

func (s *fakeStore) RequestDeploymentCancellation(_ context.Context, deploymentID, _ string) (domain.Deployment, error) {
	if deploymentID == "missing" {
		return domain.Deployment{}, database.ErrDeploymentNotFound
	}
	return domain.Deployment{ID: deploymentID, Status: domain.DeploymentCancelled}, nil
}

func (s *fakeStore) RetryDeployment(_ context.Context, originalID, deploymentID, _ string, actor string) (domain.Deployment, bool, error) {
	if originalID == "missing" {
		return domain.Deployment{}, false, database.ErrDeploymentNotFound
	}
	return domain.Deployment{ID: deploymentID, RetryOf: originalID, Status: domain.DeploymentQueued, Actor: actor}, true, nil
}

type fakeDeploymentIntake struct {
	expectedDigest string
	idempotencyKey string
	actor          string
	created        bool
}

type fakeRetentionManager struct {
	policy      domain.RetentionPolicy
	actor       string
	appliedPlan string
	applyErr    error
}

func (m *fakeRetentionManager) Plan(_ context.Context, policy domain.RetentionPolicy, actor string) (domain.RetentionPlan, error) {
	m.policy = policy
	m.actor = actor
	return domain.RetentionPlan{ID: "ret_example", Status: domain.RetentionPlanPlanned, Policy: policy, Items: []domain.RetentionItem{}}, nil
}

func (m *fakeRetentionManager) Get(_ context.Context, planID string) (domain.RetentionPlan, error) {
	if planID == "missing" {
		return domain.RetentionPlan{}, database.ErrRetentionPlanNotFound
	}
	return domain.RetentionPlan{ID: planID, Status: domain.RetentionPlanPlanned, Items: []domain.RetentionItem{}}, nil
}

func (m *fakeRetentionManager) Apply(_ context.Context, planID, actor string) (domain.RetentionPlan, error) {
	m.appliedPlan = planID
	m.actor = actor
	if m.applyErr != nil {
		return domain.RetentionPlan{}, m.applyErr
	}
	return domain.RetentionPlan{ID: planID, Status: domain.RetentionPlanApplied, Items: []domain.RetentionItem{}}, nil
}

func (i *fakeDeploymentIntake) Accept(_ context.Context, reader io.Reader, expectedDigest, idempotencyKey, actor string) (domain.Deployment, bool, error) {
	_, _ = io.Copy(io.Discard, reader)
	i.expectedDigest = expectedDigest
	i.idempotencyKey = idempotencyKey
	i.actor = actor
	return domain.Deployment{ID: "dep_created", Status: domain.DeploymentQueued, PackageDigest: expectedDigest, Actor: actor}, i.created, nil
}

func TestManagementRoutesRequireBearerTokenButTriggerIngressDoesNot(t *testing.T) {
	const webhookSecret = "test-webhook-secret-at-least-32-bytes"
	store := &fakeStore{}
	server := New(store, ":0", "management-secret", WithSecretManager(testSecretManager(map[string]string{"tests/webhook": webhookSecret})))

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

func TestWorkspaceServesEmbeddedAssetsWithoutExposingManagementToken(t *testing.T) {
	server := New(&fakeStore{}, ":0", "management-secret-that-must-not-be-rendered")

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusTemporaryRedirect || response.Header().Get("Location") != "/app/" {
		t.Fatalf("root status=%d location=%q", response.Code, response.Header().Get("Location"))
	}

	request = httptest.NewRequest(http.MethodGet, "/app/", nil)
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("workspace status = %d, body = %s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("workspace content type = %q", contentType)
	}
	for _, header := range []string{"Content-Security-Policy", "Cross-Origin-Opener-Policy", "Referrer-Policy", "X-Content-Type-Options"} {
		if response.Header().Get(header) == "" {
			t.Errorf("workspace omitted %s", header)
		}
	}
	contents := response.Body.String()
	for _, marker := range []string{"Werkt workspace", "Skip to workspace", "/app/workspace.css", "/app/workspace.js"} {
		if !strings.Contains(contents, marker) {
			t.Errorf("workspace omitted %q", marker)
		}
	}
	if strings.Contains(contents, "management-secret-that-must-not-be-rendered") {
		t.Fatal("workspace rendered the management token")
	}

	for asset, contentType := range map[string]string{
		"/app/workspace.css": "text/css",
		"/app/workspace.js":  "text/javascript",
	} {
		request = httptest.NewRequest(http.MethodGet, asset, nil)
		response = httptest.NewRecorder()
		server.server.Handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), contentType) {
			t.Errorf("%s status=%d content-type=%q", asset, response.Code, response.Header().Get("Content-Type"))
		}
		if response.Body.Len() < 1000 {
			t.Errorf("%s unexpectedly small: %d bytes", asset, response.Body.Len())
		}
	}
}

func TestRetentionPlanAndApplyRoutesAreExplicit(t *testing.T) {
	manager := &fakeRetentionManager{}
	server := New(&fakeStore{}, ":0", "", WithRetentionManager(manager))

	request := httptest.NewRequest(http.MethodPost, "/api/v1/retention/plans", strings.NewReader(`{"sourceMaxAge":"48h"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Werkt-Actor", "agent:operator")
	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("plan status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Location") != "/api/v1/retention/plans/ret_example" {
		t.Fatalf("plan location = %q", response.Header().Get("Location"))
	}
	if manager.policy.SourceMaxAge != "48h" || manager.policy.ArtifactMaxAge != service.DefaultRetentionPolicy.ArtifactMaxAge {
		t.Fatalf("merged policy = %#v", manager.policy)
	}
	if manager.policy.KeepRetryableSources != service.DefaultRetentionPolicy.KeepRetryableSources || manager.actor != "agent:operator" {
		t.Fatalf("policy=%#v actor=%q", manager.policy, manager.actor)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/retention/plans/ret_example/apply", nil)
	request.Header.Set("X-Werkt-Actor", "agent:operator")
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || manager.appliedPlan != "ret_example" {
		t.Fatalf("apply status=%d plan=%q body=%s", response.Code, manager.appliedPlan, response.Body.String())
	}
	var applied domain.RetentionPlan
	if err := json.Unmarshal(response.Body.Bytes(), &applied); err != nil || applied.Status != domain.RetentionPlanApplied {
		t.Fatalf("apply response = %#v, err = %v", applied, err)
	}
}

func TestRetentionApplyMapsExpiredPlanToGone(t *testing.T) {
	manager := &fakeRetentionManager{applyErr: database.ErrRetentionPlanExpired}
	server := New(&fakeStore{}, ":0", "", WithRetentionManager(manager))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/retention/plans/expired/apply", nil)
	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusGone {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestSecretManagementNeverReturnsValuesAndRequiresAuthentication(t *testing.T) {
	manager := testSecretManager(nil)
	server := New(&fakeStore{}, ":0", "management-secret", WithSecretManager(manager))
	path := "/api/v1/secrets/ops%2Fgithub%2Ftoken"

	request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"value":"super-secret-value","description":"GitHub automation"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated put status=%d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"value":"super-secret-value","description":"GitHub automation"}`))
	request.Header.Set("Authorization", "Bearer management-secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Werkt-Actor", "agent:operator")
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("put status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "super-secret-value") {
		t.Fatal("put response disclosed the secret value")
	}
	if response.Header().Get("Location") != path {
		t.Fatalf("location=%q", response.Header().Get("Location"))
	}

	for _, target := range []string{path, "/api/v1/secrets"} {
		request = httptest.NewRequest(http.MethodGet, target, nil)
		request.Header.Set("Authorization", "Bearer management-secret")
		response = httptest.NewRecorder()
		server.server.Handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "super-secret-value") {
			t.Fatalf("get %s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}

	request = httptest.NewRequest(http.MethodDelete, path, nil)
	request.Header.Set("Authorization", "Bearer management-secret")
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTriggerIngressRejectsInvalidCredentials(t *testing.T) {
	const webhookSecret = "test-webhook-secret-at-least-32-bytes"
	const emailToken = "test-email-token-at-least-32-bytes-long"
	store := &fakeStore{}
	server := New(store, ":0", "", WithSecretManager(testSecretManager(map[string]string{"tests/webhook": webhookSecret, "tests/email": emailToken})))

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
	store := &fakeStore{}
	server := New(store, ":0", "", WithSecretManager(testSecretManager(map[string]string{"tests/webhook": webhookSecret})))
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
	store := &fakeStore{}
	server := New(store, ":0", "", WithSecretManager(testSecretManager(map[string]string{"tests/email": emailToken})))
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

func TestDeploymentIntakeRequiresArchiveContractAndPreservesAttribution(t *testing.T) {
	store := &fakeStore{}
	intake := &fakeDeploymentIntake{created: true}
	server := New(store, ":0", "management-secret", WithDeploymentIntake(intake))

	request := httptest.NewRequest(http.MethodPost, "/api/v1/deployments", strings.NewReader("not-an-archive"))
	request.Header.Set("Authorization", "Bearer management-secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("content-type status=%d body=%s", response.Code, response.Body.String())
	}

	digest := strings.Repeat("a", 64)
	request = httptest.NewRequest(http.MethodPost, "/api/v1/deployments", strings.NewReader("archive"))
	request.Header.Set("Authorization", "Bearer management-secret")
	request.Header.Set("Content-Type", "application/gzip")
	request.Header.Set("Idempotency-Key", "deploy-example-1")
	request.Header.Set("X-Werkt-Content-SHA256", digest)
	request.Header.Set("X-Werkt-Actor", "agent:builder")
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("deployment status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Location") != "/api/v1/deployments/dep_created" || response.Header().Get("Retry-After") != "1" {
		t.Fatalf("location=%q retry-after=%q", response.Header().Get("Location"), response.Header().Get("Retry-After"))
	}
	if intake.expectedDigest != digest || intake.idempotencyKey != "deploy-example-1" || intake.actor != "agent:builder" {
		t.Fatalf("intake digest=%q idempotency=%q actor=%q", intake.expectedDigest, intake.idempotencyKey, intake.actor)
	}
}

func TestDeploymentLifecycleMutationsAndRollbackAreAgentAccessible(t *testing.T) {
	server := New(&fakeStore{}, ":0", "management-secret")

	request := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/dep_active/cancel", nil)
	request.Header.Set("Authorization", "Bearer management-secret")
	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("cancel status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/deployments/dep_failed/retry", nil)
	request.Header.Set("Authorization", "Bearer management-secret")
	request.Header.Set("Idempotency-Key", "repair-1")
	request.Header.Set("X-Werkt-Actor", "agent:repair")
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"retryOf":"dep_failed"`) {
		t.Fatalf("retry status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/automations/example/rollback", strings.NewReader(`{"revisionId":"rev_previous"}`))
	request.Header.Set("Authorization", "Bearer management-secret")
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"changed":true`) {
		t.Fatalf("rollback status=%d body=%s", response.Code, response.Body.String())
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
		"/api/v1/automations/{automation}/runs", "/api/v1/automations/{automation}/rollback",
		"/api/v1/runs", "/api/v1/audit", "/api/v1/deployments",
		"/api/v1/deployments/{deployment}", "/api/v1/deployments/{deployment}/cancel",
		"/api/v1/deployments/{deployment}/retry",
		"/api/v1/retention/plans", "/api/v1/retention/plans/{plan}",
		"/api/v1/retention/plans/{plan}/apply",
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
