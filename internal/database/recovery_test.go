package database_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/domain"
	"github.com/rvben/werkt/internal/provenance"
	"github.com/rvben/werkt/internal/service"
)

// A recovery bundle is restored somewhere other than the directory its rows
// were written under: into a staging directory by the restore drill, or onto a
// replacement host after a loss. Recorded absolute, every artifact path in the
// bundle still named the original directory, so the drill verified whatever the
// running host happened to hold at those paths and never looked at the tree it
// had just restored. Werkt's own data directory moved in September 2026 and the
// drill failed on revisions whose artifacts were intact and present.
func TestRecoveryVerificationReadsTheRestoredDataDirectory(t *testing.T) {
	databaseURL := os.Getenv("WERKT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("WERKT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	reset := func() {
		if _, err := admin.Exec(context.Background(), `TRUNCATE retention_items, retention_plans, deployments, audit_events, runs, events, triggers, revisions, automations CASCADE`); err != nil {
			t.Errorf("reset database: %v", err)
		}
	}

	attestor, err := provenance.NewAttestor(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	manifest := domain.Manifest{
		APIVersion: "werkt.dev/v1", Kind: "Automation",
		Metadata: domain.Metadata{Name: "restore-drill", Project: "integration"},
		Runtime:  domain.Runtime{Language: "go", Command: []string{"./automation"}},
	}
	contentHash := strings.Repeat("a", 64)

	original := t.TempDir()
	store, err := database.Open(ctx, databaseURL, original)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		store.Close()
		t.Fatal(err)
	}
	reset()
	defer reset()

	artifact := filepath.Join(original, "artifacts", contentHash)
	if err := os.MkdirAll(artifact, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifact, "automation.py"), []byte("print('hello')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	attestation, err := attestor.Attest(artifact, manifest, contentHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := provenance.Write(artifact, attestation); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Deploy(ctx, manifest, contentHash, artifact, attestation); err != nil {
		t.Fatal(err)
	}
	store.Close()

	// Restore the bundle's data directory somewhere else, and leave nothing at
	// the original location for a resolved path to land on by accident.
	restored := filepath.Join(t.TempDir(), "data")
	if err := os.Rename(original, restored); err != nil {
		t.Fatal(err)
	}

	restoredStore, err := database.Open(ctx, databaseURL, restored)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredStore.Close()
	custodian := service.NewArtifactCustodian(restoredStore, attestor)
	verified, err := custodian.VerifyAll(ctx)
	if err != nil {
		t.Fatalf("verify restored bundle: %v", err)
	}
	if verified != 1 {
		t.Fatalf("verified %d artifacts, want 1", verified)
	}

	// The verification has to be reading the restored tree, not passing because
	// it found nothing to read: damaging that tree must fail it.
	if err := os.WriteFile(filepath.Join(restored, "artifacts", contentHash, "automation.py"), []byte("print('tampered')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := custodian.VerifyAll(ctx); !errors.Is(err, provenance.ErrArtifactDigestMismatch) {
		t.Fatalf("tampered restored artifact verified: %v", err)
	}
}

// A revision recorded before storage locations were relativized names a
// directory that the data directory no longer owns. Resolving it would join it
// onto the data directory and produce a path that has never existed, so the
// store refuses it and the drill reports the row instead of a missing file.
func TestAbsoluteStorageLocationsAreReportedRatherThanResolved(t *testing.T) {
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
		if _, err := admin.Exec(context.Background(), `TRUNCATE retention_items, retention_plans, deployments, audit_events, runs, events, triggers, revisions, automations CASCADE`); err != nil {
			t.Errorf("reset database: %v", err)
		}
	}
	reset()
	defer reset()

	manifest := domain.Manifest{
		APIVersion: "werkt.dev/v1", Kind: "Automation",
		Metadata: domain.Metadata{Name: "legacy-row", Project: "integration"},
		Runtime:  domain.Runtime{Language: "go", Command: []string{"./automation"}},
	}
	contentHash := strings.Repeat("b", 64)
	artifact := filepath.Join(dataDir, "artifacts", contentHash)
	if err := os.MkdirAll(artifact, 0o750); err != nil {
		t.Fatal(err)
	}
	revisionID, err := store.Deploy(ctx, manifest, contentHash, artifact, legacyRowProvenance(manifest, contentHash))
	if err != nil {
		t.Fatal(err)
	}
	legacy := "/var/lib/werkt/previous-root/artifacts/" + contentHash
	if _, err := admin.Exec(ctx, `UPDATE revisions SET artifact_path = $2 WHERE id = $1`, revisionID, legacy); err != nil {
		t.Fatal(err)
	}

	_, err = store.ListRevisionArtifacts(ctx)
	if err == nil || !strings.Contains(err.Error(), legacy) {
		t.Fatalf("ListRevisionArtifacts() error = %v, want one naming %s", err, legacy)
	}
	if _, err := store.GetRevisionArtifact(ctx, manifest.Metadata.Name, revisionID); err == nil || !strings.Contains(err.Error(), legacy) {
		t.Fatalf("GetRevisionArtifact() error = %v, want one naming %s", err, legacy)
	}
}

// Every row written before this release holds an absolute path, and the hosts
// carrying them are the ones the change exists for. The migration has to move
// those rows itself; a row it cannot place is left absolute, where the store
// reports it, rather than rewritten into something that resolves somewhere
// plausible and wrong.
func TestMigrationRelativizesStorageLocationsWrittenAsAbsolutePaths(t *testing.T) {
	databaseURL := os.Getenv("WERKT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("WERKT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// A data directory that itself contains an "artifacts" segment. The rewrite
	// is greedy, so it has to take the last occurrence and not the host's own.
	dataDir := filepath.Join(t.TempDir(), "artifacts", "host", "data")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		t.Fatal(err)
	}
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
		if _, err := admin.Exec(context.Background(), `TRUNCATE retention_items, retention_plans, deployments, audit_events, runs, events, triggers, revisions, automations CASCADE`); err != nil {
			t.Errorf("reset database: %v", err)
		}
	}
	reset()
	defer reset()

	storage := func(kind, name string) string {
		path := filepath.Join(dataDir, kind, name)
		if err := os.MkdirAll(path, 0o750); err != nil {
			t.Fatal(err)
		}
		return path
	}
	manifest := func(name string) domain.Manifest {
		return domain.Manifest{
			APIVersion: "werkt.dev/v1", Kind: "Automation",
			Metadata: domain.Metadata{Name: name, Project: "integration"},
			Runtime:  domain.Runtime{Language: "go", Command: []string{"./automation"}},
		}
	}

	movedHash := strings.Repeat("c", 64)
	moved := manifest("moved-revision")
	movedID, err := store.Deploy(ctx, moved, movedHash, storage("artifacts", movedHash), legacyRowProvenance(moved, movedHash))
	if err != nil {
		t.Fatal(err)
	}
	strandedHash := strings.Repeat("d", 64)
	stranded := manifest("stranded-revision")
	strandedID, err := store.Deploy(ctx, stranded, strandedHash, storage("artifacts", strandedHash), legacyRowProvenance(stranded, strandedHash))
	if err != nil {
		t.Fatal(err)
	}
	deployment, created, err := store.CreateDeployment(ctx, "dep_migrated", "migration-key", strings.Repeat("e", 64), "sha256:"+strings.Repeat("6", 64), storage("deployment-sources", "dep_migrated"), "agent:test")
	if err != nil || !created {
		t.Fatalf("CreateDeployment() created=%v err=%v", created, err)
	}
	plan := domain.RetentionPlan{
		ID: "ret_migrated", Actor: "agent:planner", ExpiresAt: time.Now().Add(15 * time.Minute),
		Policy:  domain.RetentionPolicy{SourceMaxAge: "24h0m0s", ArtifactMaxAge: "168h0m0s", KeepRetryableSources: 1, KeepInactiveRevisions: 2},
		Summary: domain.RetentionSummary{Items: 1, EstimatedBytes: 42},
		Items: []domain.RetentionItem{{
			Position: 0, Kind: domain.RetentionKindSource, StorageKey: "deployment-sources/dep_migrated",
			StoragePath: storage("deployment-sources", "dep_migrated"), ResourceIDs: []string{deployment.ID}, Reason: "integration", EstimatedBytes: 42,
		}},
	}
	if err := store.CreateRetentionPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}

	// Put the rows back the way every pre-010 host holds them. The revision
	// keeps the real directory, so the rewrite has to survive a data directory
	// that carries the segment it keys on; the others name a host this tree
	// never lived on, which is what a restored bundle looks like.
	//
	// The stranded revision is the control: absolute, but carrying neither
	// segment, so nothing in the migration can place it.
	strandedPath := "/srv/werkt-legacy/" + strandedHash
	for _, legacy := range []struct {
		statement string
		arguments []any
	}{
		{`UPDATE revisions SET artifact_path = $2 WHERE id = $1`, []any{movedID, filepath.Join(dataDir, "artifacts", movedHash)}},
		{`UPDATE revisions SET artifact_path = $2 WHERE id = $1`, []any{strandedID, strandedPath}},
		{`UPDATE deployments SET source_path = $2 WHERE id = $1`, []any{deployment.ID, "/var/lib/werkt/data/deployment-sources/dep_migrated"}},
		{`UPDATE retention_items SET storage_path = $2 WHERE plan_id = $1 AND position = 0`, []any{plan.ID, "/var/lib/werkt/data/deployment-sources/dep_migrated"}},
	} {
		if _, err := admin.Exec(ctx, legacy.statement, legacy.arguments...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.Exec(ctx, `DELETE FROM schema_migrations WHERE name = '010_relative_storage_paths.sql'`); err != nil {
		t.Fatal(err)
	}

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("re-run migration: %v", err)
	}

	// Read the columns directly: what matters is the value the migration left
	// behind, not what a resolver would make of it.
	for _, want := range []struct {
		name      string
		statement string
		argument  any
		value     string
	}{
		{"revision", `SELECT artifact_path FROM revisions WHERE id = $1`, movedID, "artifacts/" + movedHash},
		{"unplaceable revision", `SELECT artifact_path FROM revisions WHERE id = $1`, strandedID, strandedPath},
		{"deployment source", `SELECT source_path FROM deployments WHERE id = $1`, deployment.ID, "deployment-sources/dep_migrated"},
		{"retention item", `SELECT storage_path FROM retention_items WHERE plan_id = $1 AND position = 0`, plan.ID, "deployment-sources/dep_migrated"},
	} {
		var recorded string
		if err := admin.QueryRow(ctx, want.statement, want.argument).Scan(&recorded); err != nil {
			t.Fatal(err)
		}
		if recorded != want.value {
			t.Errorf("%s recorded = %q, want %q", want.name, recorded, want.value)
		}
	}

	// And the rewritten row now resolves to storage that is actually there.
	artifact, err := store.GetRevisionArtifact(ctx, moved.Metadata.Name, movedID)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Path != filepath.Join(dataDir, "artifacts", movedHash) {
		t.Fatalf("resolved artifact = %q, want %q", artifact.Path, filepath.Join(dataDir, "artifacts", movedHash))
	}
	if _, err := os.Stat(artifact.Path); err != nil {
		t.Fatalf("resolved artifact does not exist: %v", err)
	}
	if _, err := store.GetRevisionArtifact(ctx, stranded.Metadata.Name, strandedID); err == nil || !strings.Contains(err.Error(), strandedPath) {
		t.Fatalf("unplaceable revision error = %v, want one naming %s", err, strandedPath)
	}
}

func legacyRowProvenance(value domain.Manifest, contentHash string) domain.ArtifactProvenance {
	return domain.ArtifactProvenance{
		Version: 1, ArtifactDigest: "sha256:" + strings.Repeat("f", 64), ContentHash: contentHash,
		AutomationID: value.Metadata.Name, Algorithm: "ed25519", SigningKeyID: "sha256:" + strings.Repeat("1", 64),
		PublicKey: "public", Signature: "signature",
	}
}
