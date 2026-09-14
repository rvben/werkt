package database_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/packageio"
	"github.com/rvben/werkt/internal/provenance"
	"github.com/rvben/werkt/internal/service"
)

// TestDeploymentIdentityIsThePackageNotItsEncoding drives the intake the way a
// deploying client does: one idempotency key, the same package twice, arriving
// as two different compressed streams. That is the shape of a repeated catalog
// deploy, and identifying the package by its compressed bytes rejected it.
func TestDeploymentIdentityIsThePackageNotItsEncoding(t *testing.T) {
	ctx, store, _, dataDir := openDeploymentIdentityStore(t)
	intake := service.NewDeploymentIntake(store, dataDir, packageio.DefaultLimits())

	source := writePackageFixture(t, "kind: Automation\nmetadata:\n  name: catalog\n")
	archive, packageDigest := archivePackage(t, source)
	first, created, err := intake.Accept(ctx, bytes.NewReader(archive), packageDigest, "catalog-key", "agent:test")
	if err != nil || !created {
		t.Fatalf("first deployment: created=%v err=%v", created, err)
	}

	reEncoded, reEncodedDigest := recompressPackage(t, archive)
	if reEncodedDigest == packageDigest {
		t.Fatal("the re-encoded archive is byte identical, so this test cannot observe the encoding change it exists for")
	}
	replayed, created, err := intake.Accept(ctx, bytes.NewReader(reEncoded), reEncodedDigest, "catalog-key", "agent:test")
	if err != nil {
		t.Fatalf("re-encoded package under the same key: %v", err)
	}
	if created {
		t.Fatal("re-encoded package created a second deployment instead of replaying the first")
	}
	if replayed.ID != first.ID {
		t.Fatalf("replayed deployment = %q, want %q", replayed.ID, first.ID)
	}
	if replayed.ContentDigest == "" || replayed.ContentDigest != first.ContentDigest {
		t.Fatalf("content digest = %q, want %q", replayed.ContentDigest, first.ContentDigest)
	}

	// The guarantee the fix must not lose: a key still names one package.
	otherSource := writePackageFixture(t, "kind: Automation\nmetadata:\n  name: something-else\n")
	otherArchive, otherDigest := archivePackage(t, otherSource)
	if _, _, err := intake.Accept(ctx, bytes.NewReader(otherArchive), otherDigest, "catalog-key", "agent:test"); !errors.Is(err, database.ErrDeploymentIdempotencyConflict) {
		t.Fatalf("different contents under the same key: err=%v, want %v", err, database.ErrDeploymentIdempotencyConflict)
	}
}

// TestDeploymentKeysWrittenBeforeContentDigestsStayUsable covers the rows the
// upgrade inherits. They carry no content digest, so the store digests the
// package they kept and records the answer; a row whose package is gone is
// unverifiable rather than equal.
func TestDeploymentKeysWrittenBeforeContentDigestsStayUsable(t *testing.T) {
	ctx, store, admin, dataDir := openDeploymentIdentityStore(t)
	intake := service.NewDeploymentIntake(store, dataDir, packageio.DefaultLimits())
	forgetContentDigest := func(idempotencyKey string) {
		t.Helper()
		command, err := admin.Exec(ctx, `UPDATE deployments SET content_digest = '' WHERE idempotency_key = $1`, idempotencyKey)
		if err != nil {
			t.Fatal(err)
		}
		if command.RowsAffected() != 1 {
			t.Fatalf("blanked %d rows for %q, want 1", command.RowsAffected(), idempotencyKey)
		}
	}

	source := writePackageFixture(t, "kind: Automation\nmetadata:\n  name: legacy\n")
	archive, packageDigest := archivePackage(t, source)
	legacy, created, err := intake.Accept(ctx, bytes.NewReader(archive), packageDigest, "legacy-key", "agent:test")
	if err != nil || !created {
		t.Fatalf("legacy deployment: created=%v err=%v", created, err)
	}
	forgetContentDigest("legacy-key")

	reEncoded, reEncodedDigest := recompressPackage(t, archive)
	replayed, created, err := intake.Accept(ctx, bytes.NewReader(reEncoded), reEncodedDigest, "legacy-key", "agent:test")
	if err != nil || created || replayed.ID != legacy.ID {
		t.Fatalf("legacy replay: id=%q created=%v err=%v", replayed.ID, created, err)
	}
	storedSource := filepath.Join(dataDir, "deployment-sources", legacy.ID)
	backfilled := deploymentContentDigestOf(ctx, t, admin, "legacy-key")
	expected, err := provenance.DigestDirectory(storedSource)
	if err != nil {
		t.Fatal(err)
	}
	if backfilled != expected {
		t.Fatalf("backfilled content digest = %q, want %q", backfilled, expected)
	}

	strandedSource := writePackageFixture(t, "kind: Automation\nmetadata:\n  name: stranded\n")
	strandedArchive, strandedDigest := archivePackage(t, strandedSource)
	stranded, created, err := intake.Accept(ctx, bytes.NewReader(strandedArchive), strandedDigest, "stranded-key", "agent:test")
	if err != nil || !created {
		t.Fatalf("stranded deployment: created=%v err=%v", created, err)
	}
	forgetContentDigest("stranded-key")
	if err := os.RemoveAll(filepath.Join(dataDir, "deployment-sources", stranded.ID)); err != nil {
		t.Fatal(err)
	}
	strandedReEncoded, strandedReEncodedDigest := recompressPackage(t, strandedArchive)
	if _, _, err := intake.Accept(ctx, bytes.NewReader(strandedReEncoded), strandedReEncodedDigest, "stranded-key", "agent:test"); !errors.Is(err, database.ErrDeploymentContentUnverifiable) {
		t.Fatalf("stranded legacy row: err=%v, want %v", err, database.ErrDeploymentContentUnverifiable)
	}
}

func openDeploymentIdentityStore(t *testing.T) (context.Context, *database.Store, *pgxpool.Pool, string) {
	t.Helper()
	databaseURL := os.Getenv("WERKT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("WERKT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	dataDir := t.TempDir()
	store, err := database.Open(ctx, databaseURL, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	reset := func() {
		if _, err := admin.Exec(ctx, `TRUNCATE deployments, audit_events, runs, events, triggers, revisions, automations CASCADE`); err != nil {
			t.Errorf("reset database: %v", err)
		}
	}
	reset()
	t.Cleanup(reset)
	return ctx, store, admin, dataDir
}

func deploymentContentDigestOf(ctx context.Context, t *testing.T, admin *pgxpool.Pool, idempotencyKey string) string {
	t.Helper()
	var recorded string
	if err := admin.QueryRow(ctx, `SELECT content_digest FROM deployments WHERE idempotency_key = $1`, idempotencyKey).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	return recorded
}

func writePackageFixture(t *testing.T, manifest string) string {
	t.Helper()
	source := filepath.Join(t.TempDir(), "package")
	if err := os.MkdirAll(source, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "automation.yaml"), []byte(manifest), 0o640); err != nil {
		t.Fatal(err)
	}
	return source
}

func archivePackage(t *testing.T, source string) ([]byte, string) {
	t.Helper()
	var archive bytes.Buffer
	digest, err := packageio.WriteArchive(source, &archive)
	if err != nil {
		t.Fatal(err)
	}
	return archive.Bytes(), digest
}

// recompressPackage rewrites an archive's gzip stream while leaving the tar
// bytes inside it untouched, which is what one package encoded by two archivers
// looks like to a server reading the compressed bytes.
func recompressPackage(t *testing.T, archive []byte) ([]byte, string) {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tarBytes, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	var reEncoded bytes.Buffer
	writer, err := gzip.NewWriterLevel(&reEncoded, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(tarBytes); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(reEncoded.Bytes())
	return reEncoded.Bytes(), hex.EncodeToString(digest[:])
}
