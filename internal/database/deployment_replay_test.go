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

// A deploy replaying a key whose deployment failed must report what that
// package's deployment came to after an operator retried it, not the failure
// the retry already repaired. Otherwise every replay of the key reports a
// failure for a package that is live.
func TestDeploymentReplayFollowsRetryIntegration(t *testing.T) {
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
	if _, err := store.pool.Exec(ctx, `TRUNCATE deployments, audit_events, runs, events, triggers, revisions, automations CASCADE`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = store.pool.Exec(context.Background(), `TRUNCATE deployments, audit_events, runs, events, triggers, revisions, automations CASCADE`)
	}()

	packageDigest := strings.Repeat("a", 64)
	contentDigest := "sha256:" + strings.Repeat("1", 64)
	replay := func(attempt string) domain.Deployment {
		t.Helper()
		value, created, err := store.CreateDeployment(ctx, "dep_replay_"+attempt, "catalog-key", packageDigest, contentDigest, testStorageFixture(t, dataDir, "deployment-sources", "dep_replay_"+attempt), "agent:catalog")
		if err != nil || created {
			t.Fatalf("replay %s: deployment=%#v created=%v err=%v", attempt, value, created, err)
		}
		return value
	}
	fail := func(id, worker string) {
		t.Helper()
		claimed, err := store.AcquireDeployment(ctx, worker, time.Minute)
		if err != nil || claimed == nil || claimed.ID != id {
			t.Fatalf("claim %s: claimed=%#v err=%v", id, claimed, err)
		}
		if err := store.FailDeployment(ctx, id, worker, errors.New("husker VM timed out")); err != nil {
			t.Fatal(err)
		}
	}

	original, created, err := store.CreateDeployment(ctx, "dep_original", "catalog-key", packageDigest, contentDigest, testStorageFixture(t, dataDir, "deployment-sources", "dep_original"), "agent:catalog")
	if err != nil || !created {
		t.Fatalf("original=%#v created=%v err=%v", original, created, err)
	}
	fail(original.ID, "worker-original")

	// Control: with nothing retried, the replay reports the failure as it is.
	if got := replay("unretried"); got.ID != original.ID || got.Status != domain.DeploymentFailed {
		t.Fatalf("unretried replay=%#v", got)
	}

	firstRetry, created, err := store.RetryDeployment(ctx, original.ID, "dep_retry_first", "retry-first", "agent:operator")
	if err != nil || !created {
		t.Fatalf("first retry=%#v created=%v err=%v", firstRetry, created, err)
	}
	if got := replay("queued"); got.ID != firstRetry.ID || got.Status != domain.DeploymentQueued {
		t.Fatalf("replay with a queued retry=%#v", got)
	}
	fail(firstRetry.ID, "worker-first-retry")
	if got := replay("retry-failed"); got.ID != original.ID || got.Status != domain.DeploymentFailed {
		t.Fatalf("replay with only failed retries=%#v", got)
	}

	// A retry of the retry is still the same package: the chain is followed to
	// its end, not just one link.
	secondRetry, created, err := store.RetryDeployment(ctx, firstRetry.ID, "dep_retry_second", "retry-second", "agent:operator")
	if err != nil || !created || secondRetry.RetryOf != firstRetry.ID {
		t.Fatalf("second retry=%#v created=%v err=%v", secondRetry, created, err)
	}
	claimed, err := store.AcquireDeployment(ctx, "worker-second-retry", time.Minute)
	if err != nil || claimed == nil || claimed.ID != secondRetry.ID {
		t.Fatalf("second retry claim=%#v err=%v", claimed, err)
	}
	manifest := domain.Manifest{
		APIVersion: "werkt.dev/v1",
		Kind:       "Automation",
		Metadata:   domain.Metadata{Name: "replayed-example", Project: "integration"},
		Triggers: []domain.Trigger{{
			ID: "minute", Type: "schedule", Config: map[string]any{"cron": "* * * * *", "timezone": "UTC"},
		}},
		Runtime:   domain.Runtime{Language: "python", Command: []string{"python3", "main.py"}},
		Execution: domain.Execution{Timeout: "30s"},
	}
	contentHash := strings.Repeat("c", 64)
	if err := store.SetDeploymentStage(ctx, secondRetry.ID, "worker-second-retry", domain.DeploymentActivating, manifest.Metadata.Name, contentHash); err != nil {
		t.Fatal(err)
	}
	revisionID, err := store.ActivateDeployment(ctx, secondRetry.ID, "worker-second-retry", manifest, contentHash, testStorageFixture(t, dataDir, "artifacts", contentHash), testArtifactProvenance(manifest, contentHash), "agent:operator")
	if err != nil {
		t.Fatal(err)
	}
	if got := replay("succeeded"); got.ID != secondRetry.ID || got.Status != domain.DeploymentSucceeded || got.RevisionID != revisionID {
		t.Fatalf("replay after a successful retry=%#v", got)
	}

	// A later retry that was abandoned does not hide the one that succeeded.
	abandoned, created, err := store.RetryDeployment(ctx, original.ID, "dep_retry_abandoned", "retry-abandoned", "agent:operator")
	if err != nil || !created {
		t.Fatalf("abandoned retry=%#v created=%v err=%v", abandoned, created, err)
	}
	if _, err := store.RequestDeploymentCancellation(ctx, abandoned.ID, "agent:operator"); err != nil {
		t.Fatal(err)
	}
	if got := replay("after-cancel"); got.ID != secondRetry.ID || got.Status != domain.DeploymentSucceeded {
		t.Fatalf("replay after a cancelled later retry=%#v", got)
	}

	// The replay reports the retry; the original's own record keeps its failure.
	if got, err := store.GetDeployment(ctx, original.ID); err != nil || got.Status != domain.DeploymentFailed {
		t.Fatalf("original rewritten=%#v err=%v", got, err)
	}
}
