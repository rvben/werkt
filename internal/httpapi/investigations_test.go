package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/domain"
	"gopkg.in/yaml.v3"
)

type fakeInvestigations struct {
	items  []domain.Investigation
	err    error
	writes int
}

func TestInvestigationOpenAPIReferences(t *testing.T) {
	raw, err := openAPIFS.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				if key == "$ref" {
					ref, ok := child.(string)
					if !ok || !strings.HasPrefix(ref, "#/") {
						t.Fatalf("unsupported reference %v", child)
					}
					var target any = document
					for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
						object, ok := target.(map[string]any)
						if !ok {
							t.Fatalf("invalid reference %s", ref)
						}
						target, ok = object[part]
						if !ok {
							t.Fatalf("missing reference %s", ref)
						}
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(document)
	paths := document["paths"].(map[string]any)
	for _, path := range []string{"/api/v1/investigations", "/api/v1/investigations/{investigation}"} {
		methods := paths[path].(map[string]any)
		for method, raw := range methods {
			if method == "parameters" {
				continue
			}
			operation := raw.(map[string]any)
			responses, ok := operation["responses"].(map[string]any)
			if !ok || responses["200"] == nil || responses["503"] == nil {
				t.Fatalf("incomplete contract for %s %s", method, path)
			}
		}
	}
}

func (f *fakeInvestigations) GetInvestigation(context.Context, string) (domain.Investigation, error) {
	return domain.Investigation{}, f.err
}
func (f *fakeInvestigations) ListInvestigations(context.Context, string, int64, database.ListCursor, int) ([]domain.Investigation, error) {
	return f.items, f.err
}
func (f *fakeInvestigations) ReserveInvestigation(context.Context, domain.InvestigationSpec, string) (domain.Investigation, bool, error) {
	f.writes++
	return domain.Investigation{}, true, f.err
}
func (f *fakeInvestigations) UpdateInvestigation(context.Context, string, int64, domain.InvestigationState, string) (domain.Investigation, error) {
	f.writes++
	return domain.Investigation{}, f.err
}

func TestInvestigationLookupAuthAndAvailability(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, token, body string
		err                             error
		status                          int
		contains                        string
	}{
		{"empty registry", "GET", "?repositoryId=123&issueNumber=42", "reader", "", nil, 200, `"status":"none"`},
		{"unavailable is not none", "GET", "?repositoryId=123&issueNumber=42", "reader", "", errors.New("private connection details"), 503, `lookup_unavailable`},
		{"auth required", "GET", "?repositoryId=123&issueNumber=42", "", "", nil, 401, `authentication_required`},
		{"reader cannot reserve", "POST", "", "reader", `{}`, nil, 403, `insufficient_scope`},
		{"reader cannot update", "PUT", "/request", "reader", `{}`, nil, 403, `insufficient_scope`},
		{"no broad enumeration", "GET", "", "reader", "", nil, 400, `invalid_subject`},
		{"missing version", "PUT", "/request", "operator", `{"state":{"status":"running"}}`, nil, 400, `expectedVersion`},
		{"unknown field", "PUT", "/request", "operator", `{"expectedVersion":1,"state":{"status":"running","typo":true}}`, nil, 400, `unknown field`},
		{"stale write", "PUT", "/request", "operator", `{"expectedVersion":1,"state":{"status":"running"}}`, database.ErrInvestigationConflict, 409, `investigation_conflict`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := &fakeInvestigations{err: tc.err}
			s := New(&fakeStore{}, "localhost:0", "root", WithInvestigationRegistry(registry), WithScopedManagementToken("reader", ScopeRead), WithScopedManagementToken("operator", ScopeRead, ScopeOperate))
			r := httptest.NewRequest(tc.method, "/api/v1/investigations"+tc.path, strings.NewReader(tc.body))
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			s.server.Handler.ServeHTTP(w, r)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "private connection") {
				t.Fatal("database error leaked")
			}
			if tc.token == "reader" && registry.writes != 0 {
				t.Fatal("read credential mutated registry")
			}
		})
	}
	s := New(&fakeStore{}, "localhost:0", "")
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/investigations?repositoryId=123&issueNumber=42", nil))
	if w.Code != 503 {
		t.Fatalf("unconfigured registry reported %d", w.Code)
	}
}

func TestInvestigationLookupPagination(t *testing.T) {
	f := &fakeInvestigations{items: []domain.Investigation{{InvestigationSpec: domain.InvestigationSpec{RequestID: "second"}, CreatedAt: time.Now()}, {InvestigationSpec: domain.InvestigationSpec{RequestID: "first"}}}}
	s := New(&fakeStore{}, "localhost:0", "", WithInvestigationRegistry(f))
	w := httptest.NewRecorder()
	s.server.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/investigations?repositoryId=123&issueNumber=42&limit=1", nil))
	if w.Code != 200 || w.Header().Get("X-Werkt-Next-Cursor") == "" || strings.Contains(w.Body.String(), `"requestId":"first"`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("headers=%v body=%s", w.Header(), w.Body.String())
	}
}
