package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/domain"
)

func TestSelectRetentionCandidatesProtectsActiveAndRecentStorage(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	old := now.Add(-10 * 24 * time.Hour)
	recent := now.Add(-time.Hour)
	policy := domain.RetentionPolicy{KeepRetryableSources: 1, KeepInactiveRevisions: 1}
	sources := []database.DeploymentSourceRecord{
		{ID: "active", Status: domain.DeploymentBuilding, AutomationID: "alpha", Path: "/data/deployment-sources/active", CreatedAt: old},
		{ID: "recent", Status: domain.DeploymentSucceeded, AutomationID: "alpha", Path: "/data/deployment-sources/recent", CreatedAt: recent},
		{ID: "success-old", Status: domain.DeploymentSucceeded, AutomationID: "alpha", Path: "/data/deployment-sources/success-old", CreatedAt: old},
		{ID: "retry-new", Status: domain.DeploymentFailed, AutomationID: "alpha", Path: "/data/deployment-sources/retry-new", CreatedAt: old.Add(time.Hour)},
		{ID: "retry-old", Status: domain.DeploymentCancelled, AutomationID: "alpha", Path: "/data/deployment-sources/retry-old", CreatedAt: old},
		{ID: "shared-failed", Status: domain.DeploymentFailed, AutomationID: "alpha", Path: "/data/deployment-sources/shared", CreatedAt: old},
		{ID: "shared-active", Status: domain.DeploymentQueued, AutomationID: "alpha", Path: "/data/deployment-sources/shared", CreatedAt: old},
	}
	artifacts := []database.RevisionArtifactRecord{
		{ID: "active-revision", AutomationID: "alpha", Path: "/data/artifacts/active", CreatedAt: old, Active: true},
		{ID: "running-revision", AutomationID: "alpha", Path: "/data/artifacts/running", CreatedAt: old, ReferencedByRun: true},
		{ID: "deploying-revision", AutomationID: "alpha", Path: "/data/artifacts/deploying", CreatedAt: old, ReferencedByDeployment: true},
		{ID: "inactive-new", AutomationID: "alpha", Path: "/data/artifacts/inactive-new", CreatedAt: old.Add(time.Hour)},
		{ID: "inactive-old", AutomationID: "alpha", Path: "/data/artifacts/inactive-old", CreatedAt: old},
	}

	items := selectRetentionCandidates(now, policy, 24*time.Hour, 24*time.Hour, sources, artifacts)
	want := map[string]bool{
		retentionItemKey(domain.RetentionKindSource, "/data/deployment-sources/success-old"): true,
		retentionItemKey(domain.RetentionKindSource, "/data/deployment-sources/retry-old"):   true,
		retentionItemKey(domain.RetentionKindArtifact, "/data/artifacts/inactive-old"):       true,
	}
	if len(items) != len(want) {
		t.Fatalf("candidates = %#v, want %d", items, len(want))
	}
	for _, item := range items {
		key := retentionItemKey(item.Kind, item.StoragePath)
		if !want[key] {
			t.Errorf("unexpected candidate %s", key)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("missing candidates: %#v", want)
	}
}

func TestSelectRetentionCandidatesTreatsSharedPathsAsOneItem(t *testing.T) {
	now := time.Now().UTC()
	path := "/data/deployment-sources/shared"
	items := selectRetentionCandidates(now, domain.RetentionPolicy{}, time.Hour, time.Hour, []database.DeploymentSourceRecord{
		{ID: "original", Status: domain.DeploymentFailed, AutomationID: "alpha", Path: path, CreatedAt: now.Add(-3 * time.Hour)},
		{ID: "retry", Status: domain.DeploymentFailed, AutomationID: "alpha", Path: path, CreatedAt: now.Add(-2 * time.Hour)},
	}, nil)
	if len(items) != 1 {
		t.Fatalf("items = %#v, want one shared source", items)
	}
	if got := items[0].ResourceIDs; len(got) != 2 || got[0] != "original" || got[1] != "retry" {
		t.Fatalf("resource IDs = %#v", got)
	}
}

func TestRetentionPolicyValidationNormalizesDurations(t *testing.T) {
	policy, sourceAge, artifactAge, err := validateRetentionPolicy(domain.RetentionPolicy{
		SourceMaxAge: "48h", ArtifactMaxAge: "168h", KeepRetryableSources: 2, KeepInactiveRevisions: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if policy.SourceMaxAge != "48h0m0s" || policy.ArtifactMaxAge != "168h0m0s" {
		t.Fatalf("normalized policy = %#v", policy)
	}
	if sourceAge != 48*time.Hour || artifactAge != 168*time.Hour {
		t.Fatalf("ages = %s, %s", sourceAge, artifactAge)
	}
	if _, _, _, err := validateRetentionPolicy(domain.RetentionPolicy{SourceMaxAge: "now", ArtifactMaxAge: "1h"}); err == nil {
		t.Fatal("invalid duration was accepted")
	}
	if _, _, _, err := validateRetentionPolicy(domain.RetentionPolicy{SourceMaxAge: "1h", ArtifactMaxAge: "1h", KeepInactiveRevisions: -1}); err == nil {
		t.Fatal("negative count was accepted")
	}
}

func TestRetentionStorageInspectionStaysInsideOwnedDirectories(t *testing.T) {
	dataDir := t.TempDir()
	artifact := filepath.Join(dataDir, "artifacts", "abc123")
	if err := writeFixtureFile(artifact, "bin/run", "payload"); err != nil {
		t.Fatal(err)
	}
	manager := NewRetentionManager(nil, dataDir)
	key, size, err := manager.inspectStorage(domain.RetentionKindArtifact, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if key != "artifacts/abc123" || size != int64(len("payload")) {
		t.Fatalf("key=%q size=%d", key, size)
	}
	if _, _, err := manager.inspectStorage(domain.RetentionKindArtifact, filepath.Join(dataDir, "artifacts", "abc123", "nested")); err == nil {
		t.Fatal("nested target was accepted")
	}
	if _, _, err := manager.inspectStorage(domain.RetentionKindArtifact, filepath.Join(dataDir, "outside")); err == nil {
		t.Fatal("outside target was accepted")
	}
}

func writeFixtureFile(root, relative, contents string) error {
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(contents), 0o640)
}
