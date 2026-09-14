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

func TestRetentionLifecycleIntegration(t *testing.T) {
	databaseURL := os.Getenv("WERKT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("WERKT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dataDir := t.TempDir()
	store, err := Open(ctx, databaseURL, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	reset := func() {
		if _, err := store.pool.Exec(ctx, `TRUNCATE retention_items, retention_plans, deployments, audit_events, runs, events, triggers, revisions, automations CASCADE`); err != nil {
			t.Errorf("reset database: %v", err)
		}
	}
	reset()
	defer reset()

	manifest := domain.Manifest{
		APIVersion: "werkt.dev/v1", Kind: "Automation",
		Metadata: domain.Metadata{Name: "retention-example", Project: "integration"},
		Runtime:  domain.Runtime{Language: "go", Command: []string{"./automation"}},
	}
	inactiveHash := strings.Repeat("a", 64)
	inactivePath := testStorageFixture(t, dataDir, "artifacts", inactiveHash)
	inactiveRevision, err := store.Deploy(ctx, manifest, inactiveHash, inactivePath, testArtifactProvenance(manifest, inactiveHash))
	if err != nil {
		t.Fatal(err)
	}
	activeHash := strings.Repeat("b", 64)
	activePath := testStorageFixture(t, dataDir, "artifacts", activeHash)
	activeRevision, err := store.Deploy(ctx, manifest, activeHash, activePath, testArtifactProvenance(manifest, activeHash))
	if err != nil {
		t.Fatal(err)
	}
	if detached, err := store.DetachRetentionItem(ctx, domain.RetentionKindArtifact, activePath); err != nil || detached {
		t.Fatalf("active artifact detached=%v err=%v", detached, err)
	}
	deploying, created, err := store.CreateDeployment(ctx, "dep_existing_artifact", "existing-artifact", strings.Repeat("d", 64), testStorageFixture(t, dataDir, "deployment-sources", "dep_existing_artifact"), "agent:test")
	if err != nil || !created {
		t.Fatalf("deploying=%#v created=%v err=%v", deploying, created, err)
	}
	activeDeployment, err := store.AcquireDeployment(ctx, "worker-existing-artifact", time.Minute)
	if err != nil || activeDeployment == nil || activeDeployment.ID != deploying.ID {
		t.Fatalf("claimed=%#v err=%v", activeDeployment, err)
	}
	if err := store.SetDeploymentStage(ctx, deploying.ID, "worker-existing-artifact", domain.DeploymentBuilding, manifest.Metadata.Name, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if detached, err := store.DetachRetentionItem(ctx, domain.RetentionKindArtifact, inactivePath); err != nil || detached {
		t.Fatalf("artifact used by deployment detached=%v err=%v", detached, err)
	}
	if _, err := store.RequestDeploymentCancellation(ctx, deploying.ID, "agent:test"); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteDeploymentCancellation(ctx, deploying.ID, "worker-existing-artifact"); err != nil {
		t.Fatal(err)
	}
	if detached, err := store.DetachRetentionItem(ctx, domain.RetentionKindArtifact, inactivePath); err != nil || !detached {
		t.Fatalf("inactive artifact detached=%v err=%v", detached, err)
	}
	if _, err := store.RollbackAutomation(ctx, manifest.Metadata.Name, inactiveRevision, "agent:test"); !errors.Is(err, ErrRevisionArtifactUnavailable) {
		t.Fatalf("rollback after retention error = %v", err)
	}
	artifactRecords, err := store.ListRevisionArtifactRecords(ctx)
	if err != nil || len(artifactRecords) != 1 || artifactRecords[0].ID != activeRevision || !artifactRecords[0].Active {
		t.Fatalf("artifact records = %#v, err = %v", artifactRecords, err)
	}

	sourcePath := testStorageFixture(t, dataDir, "deployment-sources", "dep_retention")
	deployment, created, err := store.CreateDeployment(ctx, "dep_retention", "retention-source", strings.Repeat("c", 64), sourcePath, "agent:test")
	if err != nil || !created {
		t.Fatalf("deployment=%#v created=%v err=%v", deployment, created, err)
	}
	if detached, err := store.DetachRetentionItem(ctx, domain.RetentionKindSource, sourcePath); err != nil || detached {
		t.Fatalf("queued source detached=%v err=%v", detached, err)
	}
	if _, err := store.RequestDeploymentCancellation(ctx, deployment.ID, "agent:test"); err != nil {
		t.Fatal(err)
	}
	if detached, err := store.DetachRetentionItem(ctx, domain.RetentionKindSource, sourcePath); err != nil || !detached {
		t.Fatalf("terminal source detached=%v err=%v", detached, err)
	}
	if _, _, err := store.RetryDeployment(ctx, deployment.ID, "dep_retry", "retry-retained-source", "agent:test"); !errors.Is(err, ErrDeploymentSourceUnavailable) {
		t.Fatalf("retry after retention error = %v", err)
	}

	expiresAt := time.Now().Add(15 * time.Minute)
	plan := domain.RetentionPlan{
		ID: "ret_integration", Actor: "agent:planner", ExpiresAt: expiresAt,
		Policy:  domain.RetentionPolicy{SourceMaxAge: "24h0m0s", ArtifactMaxAge: "168h0m0s", KeepRetryableSources: 1, KeepInactiveRevisions: 2},
		Summary: domain.RetentionSummary{Items: 1, EstimatedBytes: 42},
		Items: []domain.RetentionItem{{
			Position: 0, Kind: domain.RetentionKindSource, StorageKey: "deployment-sources/dep_retention",
			StoragePath: sourcePath, ResourceIDs: []string{deployment.ID}, Reason: "integration", EstimatedBytes: 42,
		}},
	}
	if err := store.CreateRetentionPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimRetentionPlan(ctx, plan.ID, time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
	if err := store.RecordRetentionItem(ctx, plan.ID, 0, domain.RetentionItemDeleted, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteRetentionPlan(ctx, plan.ID, "agent:operator"); err != nil {
		t.Fatal(err)
	}
	completed, err := store.GetRetentionPlan(ctx, plan.ID)
	if err != nil || completed.Status != domain.RetentionPlanApplied || completed.Summary.Deleted != 1 || completed.Summary.EstimatedBytes != 42 {
		t.Fatalf("completed plan = %#v, err = %v", completed, err)
	}
	claimed, err = store.ClaimRetentionPlan(ctx, plan.ID, time.Minute)
	if err != nil || claimed {
		t.Fatalf("reclaimed applied plan=%v err=%v", claimed, err)
	}
	audit, err := store.ListAuditEvents(ctx, "", 20)
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, event := range audit {
		actions[event.Action] = true
	}
	for _, action := range []string{"retention.planned", "retention.applied"} {
		if !actions[action] {
			t.Errorf("missing audit action %q", action)
		}
	}
}
