package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
}

func (s *fakeStore) Ping(context.Context) error { return nil }

func (s *fakeStore) IngestEvent(context.Context, string, string, string, string, time.Time, json.RawMessage, map[string]any) (string, bool, error) {
	s.ingested = true
	return "run_hook", true, nil
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

	request = httptest.NewRequest(http.MethodPost, "/api/v1/hooks/example/incoming", strings.NewReader(`{"ok":true}`))
	response = httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("trigger status = %d, want 202", response.Code)
	}
	if !store.ingested {
		t.Fatal("public trigger ingress did not reach the store")
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
