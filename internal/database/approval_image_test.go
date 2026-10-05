package database

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rvben/werkt/internal/domain"
)

func TestApprovalImageIsListedWithoutValueAndNotEchoedIntegration(t *testing.T) {
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
		if _, err := store.pool.Exec(ctx, `TRUNCATE notification_deliveries, notification_events, approvals, deployments, audit_events, runs, events, triggers, revisions, automations CASCADE`); err != nil {
			t.Fatal(err)
		}
	}
	reset()
	defer reset()

	manifest := domain.Manifest{
		APIVersion: "werkt.dev/v1", Kind: "Automation",
		Metadata: domain.Metadata{Name: "approval-image", Project: "integration"},
		Runtime:  domain.Runtime{Language: "go", Command: []string{"./automation"}},
	}
	contentHash := strings.Repeat("c", 64)
	revisionID, err := store.Deploy(ctx, manifest, contentHash, testStorageFixture(t, dataDir, "artifacts", contentHash), testArtifactProvenance(manifest, contentHash))
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.EnqueueManualRun(ctx, manifest.Metadata.Name, "image-run", revisionID, json.RawMessage(`{}`), "test"); err != nil || !created {
		t.Fatalf("enqueue created=%v err=%v", created, err)
	}
	run, err := store.AcquireRun(ctx, "worker-1", time.Minute)
	if err != nil || run == nil {
		t.Fatalf("acquire run=%#v err=%v", run, err)
	}
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 96<<10)...)
	image := domain.ApprovalImagePrefix + base64.StdEncoding.EncodeToString(jpeg)
	if err := store.CompleteRun(ctx, *run, "worker-1", "", json.RawMessage(`{}`), nil, domain.RunControl{
		Approval: &domain.ApprovalRequest{
			Key: "publish-image", Title: "Publish?", ExpiresAt: time.Now().UTC().Add(time.Hour),
			Fields: []domain.ApprovalField{
				{ID: "title", Label: "Title", Type: "text", Value: "Sunday service"},
				{ID: "slide", Label: "Slide", Type: "image", Value: image},
				{ID: "notes", Label: "Notes", Type: "textarea"},
			},
			Actions: []domain.ApprovalAction{{ID: "approve", Label: "Approve"}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	listed, err := store.ListApprovalsPage(ctx, manifest.Metadata.Name, "", ListCursor{}, 10)
	if err != nil || len(listed) != 1 {
		t.Fatalf("listed=%d err=%v", len(listed), err)
	}
	fields := listed[0].Fields
	if len(fields) != 3 || fields[0].ID != "title" || fields[1].ID != "slide" || fields[2].ID != "notes" {
		t.Fatalf("listed fields lost their order: %#v", fields)
	}
	if fields[1].Value != nil || fields[1].Type != "image" || fields[1].Label != "Slide" {
		t.Fatalf("listed image field=%#v, want its declaration without the value", fields[1])
	}
	if fields[0].Value != "Sunday service" {
		t.Fatalf("listed text value=%#v, want it kept", fields[0].Value)
	}

	detail, err := store.GetApproval(ctx, listed[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Fields[1].Value != image {
		t.Fatal("approval detail does not carry the image the reviewer needs")
	}

	if _, created, err := store.ResolveApproval(ctx, detail.ID, "decision-image", revisionID, "approve", map[string]any{}, "user:test"); err != nil || !created {
		t.Fatalf("resolve created=%v err=%v", created, err)
	}
	var envelope string
	if err := store.pool.QueryRow(ctx, `SELECT envelope::text FROM events WHERE envelope->'trigger'->>'type' = 'approval'`).Scan(&envelope); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(envelope, "slide") || !strings.Contains(envelope, "Sunday service") {
		t.Fatalf("continuation event=%s, want the title without the image", envelope)
	}
}
