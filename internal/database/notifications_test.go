package database

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rvben/werkt/internal/domain"
)

func TestNotificationOutboxIntegration(t *testing.T) {
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
		Metadata:  domain.Metadata{Name: "approval-example", Project: "integration"},
		Runtime:   domain.Runtime{Language: "go", Command: []string{"./automation"}},
		Execution: domain.Execution{Concurrency: "forbid"},
	}
	contentHash := strings.Repeat("b", 64)
	revisionID, err := store.Deploy(ctx, manifest, contentHash, testStorageFixture(t, dataDir, "artifacts", contentHash), testArtifactProvenance(manifest, contentHash))
	if err != nil {
		t.Fatal(err)
	}
	_, created, err := store.EnqueueManualRun(ctx, manifest.Metadata.Name, "approval-run", revisionID, json.RawMessage(`{}`), "test")
	if err != nil || !created {
		t.Fatalf("enqueue created=%v err=%v", created, err)
	}
	run, err := store.AcquireRun(ctx, "worker-1", time.Minute)
	if err != nil || run == nil {
		t.Fatalf("acquire run=%#v err=%v", run, err)
	}
	expiresAt := time.Now().UTC().Add(time.Hour)
	if err := store.CompleteRun(ctx, *run, "worker-1", "", json.RawMessage(`{"ready":true}`), nil, domain.RunControl{
		Approval: &domain.ApprovalRequest{
			Key: "publish-1", Title: "Publish result", Description: "Verify the evidence.", ExpiresAt: expiresAt,
			Fields:  []domain.ApprovalField{},
			Actions: []domain.ApprovalAction{{ID: "approve", Label: "Approve"}, {ID: "reject", Label: "Reject"}},
		},
		Notifications: []domain.NotificationRequest{{Key: "ready", Title: "Result ready", Body: "The result is ready for review.", Priority: "default"}},
	}); err != nil {
		t.Fatal(err)
	}
	approvals, err := store.ListApprovalsPage(ctx, manifest.Metadata.Name, "pending", ListCursor{}, 10)
	if err != nil || len(approvals) != 1 {
		t.Fatalf("approvals=%#v err=%v", approvals, err)
	}
	if _, err := store.ResendPendingApprovalNotification(ctx, approvals[0].ID, "operator:test"); err != nil {
		t.Fatal(err)
	}
	if count, err := store.EnqueueExpiringApprovalNotifications(ctx, time.Now().UTC().Add(2*time.Hour), 10); err != nil || count != 1 {
		t.Fatalf("expiring count=%d err=%v", count, err)
	}
	if count, err := store.EnqueueExpiringApprovalNotifications(ctx, time.Now().UTC().Add(2*time.Hour), 10); err != nil || count != 0 {
		t.Fatalf("repeated expiring count=%d err=%v", count, err)
	}
	if _, created, err := store.ResolveApproval(ctx, approvals[0].ID, "decision-1", revisionID, "approve", map[string]any{}, "user:test"); err != nil || !created {
		t.Fatalf("resolve created=%v err=%v", created, err)
	}

	var eventCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM notification_events WHERE automation_id = $1`, manifest.Metadata.Name).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 5 {
		t.Fatalf("notification event count=%d, want requested, automation, resend, expiring, resolved", eventCount)
	}

	for index := 0; index < eventCount; index++ {
		event, err := store.AcquireNotificationEvent(ctx)
		if err != nil || event == nil {
			t.Fatalf("acquire event=%#v err=%v", event, err)
		}
		var targets []NotificationTarget
		if index == 0 {
			targets = []NotificationTarget{{
				DestinationID: "operator", Provider: "ntfy", Config: json.RawMessage(`{"id":"operator","provider":"ntfy","server":"https://notify.example","topic":"ops"}`),
			}}
		}
		if err := store.ExpandNotificationEvent(ctx, event.ID, targets); err != nil {
			t.Fatal(err)
		}
	}
	delivery, err := store.AcquireNotificationDelivery(ctx, "notification-worker")
	if err != nil || delivery == nil || delivery.Attempt != 1 {
		t.Fatalf("delivery=%#v err=%v", delivery, err)
	}
	if err := store.FailNotificationDelivery(ctx, *delivery, "notification-worker", "ntfy notification returned HTTP 503", true); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := store.pool.QueryRow(ctx, `SELECT status FROM notification_deliveries WHERE id = $1`, delivery.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "queued" {
		t.Fatalf("retry status=%q", status)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE notification_deliveries SET available_at = now() WHERE id = $1`, delivery.ID); err != nil {
		t.Fatal(err)
	}
	delivery, err = store.AcquireNotificationDelivery(ctx, "notification-worker")
	if err != nil || delivery == nil || delivery.Attempt != 2 {
		t.Fatalf("retry delivery=%#v err=%v", delivery, err)
	}
	if err := store.CompleteNotificationDelivery(ctx, *delivery, "notification-worker", 200); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteNotificationDelivery(ctx, *delivery, "notification-worker", 200); !errors.Is(err, ErrNotificationLeaseLost) {
		t.Fatalf("duplicate completion error=%v", err)
	}
	var deliveredAudit int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'notification.delivered'`).Scan(&deliveredAudit); err != nil {
		t.Fatal(err)
	}
	if deliveredAudit != 1 {
		t.Fatalf("delivered audit count=%d", deliveredAudit)
	}
	testEventID, err := store.EnqueueNotificationTest(ctx, "operator:test")
	if err != nil {
		t.Fatal(err)
	}
	var testEventType, testEventStatus string
	if err := store.pool.QueryRow(ctx, `
		SELECT event_type, status FROM notification_events WHERE id = $1`, testEventID,
	).Scan(&testEventType, &testEventStatus); err != nil {
		t.Fatal(err)
	}
	if testEventType != "notification.test" || testEventStatus != "pending" {
		t.Fatalf("test notification type=%q status=%q", testEventType, testEventStatus)
	}
	var testAudit int
	if err := store.pool.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE action = 'notification.test_enqueued' AND actor = 'operator:test'`,
	).Scan(&testAudit); err != nil {
		t.Fatal(err)
	}
	if testAudit != 1 {
		t.Fatalf("test notification audit count=%d", testAudit)
	}
}
