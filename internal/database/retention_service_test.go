package database_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/domain"
	"github.com/rvben/werkt/internal/service"
)

func TestRetentionManagerRevalidatesPlanBeforeDeleting(t *testing.T) {
	databaseURL := os.Getenv("WERKT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("WERKT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dataDir := t.TempDir()
	store, err := database.Open(ctx, databaseURL, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	reset := func() {
		if _, err := admin.Exec(ctx, `TRUNCATE retention_items, retention_plans, deployments, audit_events, runs, events, triggers, revisions, automations CASCADE`); err != nil {
			t.Errorf("reset database: %v", err)
		}
	}
	reset()
	defer reset()

	deletedSource := filepath.Join(dataDir, "deployment-sources", "dep_delete")
	protectedSource := filepath.Join(dataDir, "deployment-sources", "dep_protect")
	writeRetentionFixture(t, deletedSource)
	writeRetentionFixture(t, protectedSource)
	createFailedDeployment(t, ctx, store, "dep_delete", "delete-key", deletedSource, "worker-delete")
	createFailedDeployment(t, ctx, store, "dep_protect", "protect-key", protectedSource, "worker-protect")

	manifest := domain.Manifest{
		APIVersion: "werkt.dev/v1", Kind: "Automation",
		Metadata: domain.Metadata{Name: "retention-race", Project: "integration"},
		Runtime:  domain.Runtime{Language: "go", Command: []string{"./automation"}},
	}
	inactiveHash := strings.Repeat("d", 64)
	inactiveArtifact := filepath.Join(dataDir, "artifacts", inactiveHash)
	writeRetentionFixture(t, inactiveArtifact)
	inactiveRevision, err := store.Deploy(ctx, manifest, inactiveHash, inactiveArtifact, retentionArtifactProvenance(manifest, inactiveHash))
	if err != nil {
		t.Fatal(err)
	}
	activeHash := strings.Repeat("e", 64)
	activeArtifact := filepath.Join(dataDir, "artifacts", activeHash)
	writeRetentionFixture(t, activeArtifact)
	if _, err := store.Deploy(ctx, manifest, activeHash, activeArtifact, retentionArtifactProvenance(manifest, activeHash)); err != nil {
		t.Fatal(err)
	}

	manager := service.NewRetentionManager(store, dataDir)
	plan, err := manager.Plan(ctx, domain.RetentionPolicy{
		SourceMaxAge: "1ns", ArtifactMaxAge: "1ns", KeepRetryableSources: 0, KeepInactiveRevisions: 0,
	}, "agent:planner")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary.Items != 3 {
		t.Fatalf("planned items = %#v", plan.Items)
	}

	// Both resources become protected after the dry run: a retry starts using
	// the shared source and rollback makes the inactive artifact active.
	if _, created, err := store.RetryDeployment(ctx, "dep_protect", "dep_retry", "retry-after-plan", "agent:repair"); err != nil || !created {
		t.Fatalf("retry created=%v err=%v", created, err)
	}
	if changed, err := store.RollbackAutomation(ctx, manifest.Metadata.Name, inactiveRevision, "agent:operator"); err != nil || !changed {
		t.Fatalf("rollback changed=%v err=%v", changed, err)
	}

	applied, err := manager.Apply(ctx, plan.ID, "agent:operator")
	if err != nil {
		t.Fatal(err)
	}
	if applied.Status != domain.RetentionPlanApplied || applied.Summary.Deleted != 1 || applied.Summary.Skipped != 2 || applied.Summary.Failed != 0 {
		t.Fatalf("applied plan = %#v", applied)
	}
	if _, err := os.Stat(deletedSource); !os.IsNotExist(err) {
		t.Fatalf("eligible source still exists: %v", err)
	}
	for _, path := range []string{protectedSource, inactiveArtifact} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("protected storage %s: %v", path, err)
		}
	}

	repeated, err := manager.Apply(ctx, plan.ID, "agent:operator")
	if err != nil || repeated.Summary.Deleted != 1 || repeated.Summary.Skipped != 2 {
		t.Fatalf("repeated apply = %#v, err = %v", repeated, err)
	}
}

func retentionArtifactProvenance(value domain.Manifest, contentHash string) domain.ArtifactProvenance {
	return domain.ArtifactProvenance{
		Version: 1, ArtifactDigest: "sha256:" + strings.Repeat("f", 64), ContentHash: contentHash,
		AutomationID: value.Metadata.Name, Algorithm: "ed25519", SigningKeyID: "sha256:" + strings.Repeat("1", 64),
		PublicKey: "public", Signature: "signature",
	}
}

func createFailedDeployment(t *testing.T, ctx context.Context, store *database.Store, id, key, sourcePath, worker string) {
	t.Helper()
	contentDigest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(id)))
	if _, created, err := store.CreateDeployment(ctx, id, key, strings.Repeat("f", 64), contentDigest, sourcePath, "agent:test"); err != nil || !created {
		t.Fatalf("create %s: created=%v err=%v", id, created, err)
	}
	claimed, err := store.AcquireDeployment(ctx, worker, time.Minute)
	if err != nil || claimed == nil || claimed.ID != id {
		t.Fatalf("claim %s: %#v err=%v", id, claimed, err)
	}
	if err := store.FailDeployment(ctx, id, worker, os.ErrInvalid); err != nil {
		t.Fatalf("fail %s: %v", id, err)
	}
}

func writeRetentionFixture(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "fixture"), []byte("retained bytes"), 0o640); err != nil {
		t.Fatal(err)
	}
}
