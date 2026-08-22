package database

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rvben/werkt/internal/domain"
)

func TestDeploymentLifecycleIntegration(t *testing.T) {
	databaseURL := os.Getenv("WERKT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("WERKT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `TRUNCATE deployments, audit_events, runs, events, triggers, revisions, automations CASCADE`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = store.pool.Exec(context.Background(), `TRUNCATE deployments, audit_events, runs, events, triggers, revisions, automations CASCADE`)
	}()

	packageDigest := strings.Repeat("a", 64)
	deployment, created, err := store.CreateDeployment(ctx, "dep_success", "deployment-key", packageDigest, t.TempDir(), "agent:builder")
	if err != nil || !created || deployment.Status != domain.DeploymentQueued {
		t.Fatalf("deployment=%#v created=%v err=%v", deployment, created, err)
	}
	duplicate, created, err := store.CreateDeployment(ctx, "dep_ignored", "deployment-key", packageDigest, t.TempDir(), "agent:other")
	if err != nil || created || duplicate.ID != deployment.ID {
		t.Fatalf("duplicate=%#v created=%v err=%v", duplicate, created, err)
	}
	if _, _, err := store.CreateDeployment(ctx, "dep_conflict", "deployment-key", strings.Repeat("b", 64), t.TempDir(), "agent:builder"); !errors.Is(err, ErrDeploymentIdempotencyConflict) {
		t.Fatalf("conflict error=%v", err)
	}

	claimed, err := store.AcquireDeployment(ctx, "worker-1", time.Minute)
	if err != nil || claimed == nil || claimed.ID != deployment.ID || claimed.Status != domain.DeploymentValidating {
		t.Fatalf("claimed=%#v err=%v", claimed, err)
	}
	value := domain.Manifest{
		APIVersion: "werkt.dev/v1",
		Kind:       "Automation",
		Metadata:   domain.Metadata{Name: "remote-example", Project: "integration"},
		Triggers: []domain.Trigger{{
			ID: "minute", Type: "schedule", Config: map[string]any{"cron": "* * * * *", "timezone": "UTC"},
		}},
		Runtime:   domain.Runtime{Language: "python", Command: []string{"python3", "main.py"}},
		Execution: domain.Execution{Timeout: "30s"},
	}
	contentHash := strings.Repeat("c", 64)
	if err := store.SetDeploymentStage(ctx, deployment.ID, "worker-1", domain.DeploymentActivating, value.Metadata.Name, contentHash); err != nil {
		t.Fatal(err)
	}
	revisionID, err := store.ActivateDeployment(ctx, deployment.ID, "worker-1", value, contentHash, t.TempDir(), "agent:builder")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := store.GetDeployment(ctx, deployment.ID)
	if err != nil || completed.Status != domain.DeploymentSucceeded || completed.RevisionID != revisionID || completed.AutomationID != value.Metadata.Name {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	if err := store.SetDeploymentStage(ctx, deployment.ID, "worker-1", domain.DeploymentBuilding, value.Metadata.Name, contentHash); !errors.Is(err, ErrDeploymentLeaseLost) {
		t.Fatalf("completed deployment accepted stage update: %v", err)
	}

	failed, created, err := store.CreateDeployment(ctx, "dep_failure", "failure-key", strings.Repeat("d", 64), t.TempDir(), "agent:builder")
	if err != nil || !created {
		t.Fatalf("failed setup=%#v created=%v err=%v", failed, created, err)
	}
	claimed, err = store.AcquireDeployment(ctx, "worker-2", time.Minute)
	if err != nil || claimed == nil || claimed.ID != failed.ID {
		t.Fatalf("failed claim=%#v err=%v", claimed, err)
	}
	if err := store.SetDeploymentStage(ctx, failed.ID, "worker-2", domain.DeploymentBuilding, value.Metadata.Name, strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	if err := store.FailDeployment(ctx, failed.ID, "worker-2", errors.New("compiler exited with code 1")); err != nil {
		t.Fatal(err)
	}
	failed, err = store.GetDeployment(ctx, failed.ID)
	if err != nil || failed.Status != domain.DeploymentFailed || !strings.Contains(failed.Error, "compiler exited") {
		t.Fatalf("failed=%#v err=%v", failed, err)
	}

	deployments, err := store.ListDeploymentsFiltered(ctx, value.Metadata.Name, "", 10)
	if err != nil || len(deployments) != 2 {
		t.Fatalf("deployments=%#v err=%v", deployments, err)
	}
	audit, err := store.ListAuditEvents(ctx, value.Metadata.Name, 20)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, event := range audit {
		if event.Actor != "agent:builder" {
			t.Errorf("audit actor=%q", event.Actor)
		}
		actions[event.Action] = true
	}
	for _, action := range []string{"automation.deployed", "deployment.succeeded", "deployment.failed"} {
		if !actions[action] {
			t.Errorf("missing audit action %q in %#v", action, actions)
		}
	}
}
