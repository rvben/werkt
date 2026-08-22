package managementclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/werkt/internal/domain"
)

func TestCreateAndWaitDeploymentUsesAgentContract(t *testing.T) {
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization=%q", request.Header.Get("Authorization"))
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/deployments":
			if request.Header.Get("Idempotency-Key") != "deploy-1" || request.Header.Get("X-Werkt-Actor") != "cli" || request.Header.Get("X-Werkt-Content-SHA256") != strings.Repeat("a", 64) {
				t.Errorf("headers=%v", request.Header)
			}
			_ = json.NewEncoder(response).Encode(map[string]any{
				"deployment": domain.Deployment{ID: "dep_1", Status: domain.DeploymentQueued},
				"created":    true,
			})
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/deployments/dep_1":
			status := domain.DeploymentBuilding
			if reads.Add(1) > 1 {
				status = domain.DeploymentSucceeded
			}
			_ = json.NewEncoder(response).Encode(domain.Deployment{ID: "dep_1", Status: status})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client, err := New(server.URL, "secret", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	deployment, created, err := client.CreateDeployment(
		context.Background(), strings.NewReader("archive"), strings.Repeat("a", 64), "deploy-1", "cli",
	)
	if err != nil || !created {
		t.Fatalf("deployment=%#v created=%v err=%v", deployment, created, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	deployment, err = client.WaitDeployment(ctx, deployment, time.Millisecond)
	if err != nil || deployment.Status != domain.DeploymentSucceeded {
		t.Fatalf("deployment=%#v err=%v", deployment, err)
	}
}

func TestCreateDeploymentDoesNotCloseCallerReader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		_ = json.NewEncoder(response).Encode(map[string]any{
			"deployment": domain.Deployment{ID: "dep_1", Status: domain.DeploymentQueued},
			"created":    true,
		})
	}))
	defer server.Close()
	client, err := New(server.URL, "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	body := &trackedReader{Reader: strings.NewReader("archive")}
	if _, _, err := client.CreateDeployment(context.Background(), body, strings.Repeat("a", 64), "deploy-1", "cli"); err != nil {
		t.Fatal(err)
	}
	if body.closed {
		t.Fatal("CreateDeployment closed a caller-owned reader")
	}
}

func TestRetentionClientPreservesPlanThenApplyBoundary(t *testing.T) {
	var planned domain.RetentionPolicy
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Werkt-Actor") != "agent:operator" && request.Method == http.MethodPost {
			t.Errorf("actor=%q", request.Header.Get("X-Werkt-Actor"))
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/retention/plans":
			if err := json.NewDecoder(request.Body).Decode(&planned); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(response).Encode(domain.RetentionPlan{ID: "ret_1", Status: domain.RetentionPlanPlanned, Policy: planned})
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/retention/plans/ret_1":
			_ = json.NewEncoder(response).Encode(domain.RetentionPlan{ID: "ret_1", Status: domain.RetentionPlanPlanned})
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/retention/plans/ret_1/apply":
			_ = json.NewEncoder(response).Encode(domain.RetentionPlan{ID: "ret_1", Status: domain.RetentionPlanApplied})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client, err := New(server.URL, "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	policy := domain.RetentionPolicy{SourceMaxAge: "24h", ArtifactMaxAge: "168h", KeepRetryableSources: 2, KeepInactiveRevisions: 4}
	plan, err := client.CreateRetentionPlan(context.Background(), policy, "agent:operator")
	if err != nil || plan.Status != domain.RetentionPlanPlanned || planned.KeepInactiveRevisions != 4 {
		t.Fatalf("plan=%#v sent=%#v err=%v", plan, planned, err)
	}
	if _, err := client.GetRetentionPlan(context.Background(), plan.ID); err != nil {
		t.Fatal(err)
	}
	applied, err := client.ApplyRetentionPlan(context.Background(), plan.ID, "agent:operator")
	if err != nil || applied.Status != domain.RetentionPlanApplied {
		t.Fatalf("applied=%#v err=%v", applied, err)
	}
}

type trackedReader struct {
	io.Reader
	closed bool
}

func (r *trackedReader) Close() error {
	r.closed = true
	return nil
}
