package database

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rvben/werkt/internal/domain"
)

func TestManagementLifecycleIntegration(t *testing.T) {
	const runtimeSecretValue = "integration-runtime-secret-value-never-persist"
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
	reset := func() {
		if _, err := store.pool.Exec(ctx, `TRUNCATE deployments, audit_events, runs, events, triggers, revisions, automations, secrets CASCADE`); err != nil {
			t.Errorf("reset database: %v", err)
		}
	}
	reset()
	defer reset()
	for _, name := range []string{"tests/webhook", "tests/runtime-token"} {
		if _, _, err := store.PutEncryptedSecret(ctx, name, "fixture", []byte("encrypted-fixture"), "test-key", "test"); err != nil {
			t.Fatal(err)
		}
	}

	enabled := true
	value := domain.Manifest{
		APIVersion: "werkt.dev/v1",
		Kind:       "Automation",
		Metadata: domain.Metadata{
			Name: "managed-example", Project: "operations", Folder: "alerts", Labels: []string{"critical"},
		},
		Triggers: []domain.Trigger{
			{ID: "incoming", Type: "webhook", Enabled: &enabled, Config: map[string]any{"secret": "tests/webhook", "deliveryDelay": "5m"}},
			{ID: "minute", Type: "schedule", Enabled: &enabled, Config: map[string]any{"cron": "* * * * *", "timezone": "UTC"}},
		},
		Runtime: domain.Runtime{
			Language: "go",
			Command:  []string{"./automation"},
			Secrets:  map[string]string{"SERVICE_TOKEN": "tests/runtime-token"},
		},
		Execution: domain.Execution{Retries: 1, Concurrency: "forbid"},
	}
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	revisionID, err := store.Deploy(ctx, value, hash, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runID, created, err := store.EnqueueManualRun(ctx, value.Metadata.Name, "summary-latest-run", revisionID, json.RawMessage(`{"source":"summary"}`), "agent:test")
	if err != nil || !created {
		t.Fatalf("enqueue summary run created=%v err=%v", created, err)
	}

	onlyEnabled := true
	automations, err := store.ListAutomations(ctx, AutomationFilter{
		Project: "operations", Folder: "alerts", Label: "critical", Query: "managed", Enabled: &onlyEnabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(automations) != 1 || automations[0].ActiveRevisionID != revisionID {
		t.Fatalf("automations = %#v", automations)
	}
	if automations[0].LatestRun == nil || automations[0].LatestRun.ID != runID || automations[0].LatestRun.Status != domain.RunQueued {
		t.Fatalf("latest run summary = %#v", automations[0].LatestRun)
	}
	detail, err := store.GetAutomation(ctx, value.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Triggers) != 2 || len(detail.Revisions) != 1 || !detail.Enabled {
		t.Fatalf("detail = %#v", detail)
	}
	if detail.LatestRun == nil || detail.LatestRun.ID != runID {
		t.Fatalf("detail latest run = %#v", detail.LatestRun)
	}
	if detail.Manifest.Runtime.Secrets["SERVICE_TOKEN"] != "tests/runtime-token" {
		t.Fatalf("runtime secret reference = %#v", detail.Manifest.Runtime.Secrets)
	}
	var persistedManifest string
	if err := store.pool.QueryRow(ctx, `SELECT manifest::text FROM revisions WHERE id = $1`, revisionID).Scan(&persistedManifest); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(persistedManifest, runtimeSecretValue) || !strings.Contains(persistedManifest, "tests/runtime-token") {
		t.Fatalf("persisted manifest crossed secret boundary: %s", persistedManifest)
	}
	policy, err := store.GetTriggerIngressPolicy(ctx, value.Metadata.Name, "incoming", "webhook")
	if err != nil || !json.Valid(policy.Config) {
		t.Fatalf("ingress policy = %s err=%v", policy.Config, err)
	}
	changed, err := store.SetAutomationEnabled(ctx, value.Metadata.Name, false, "agent:test")
	if err != nil || !changed {
		t.Fatalf("pause changed=%v err=%v", changed, err)
	}
	if _, err := store.GetTriggerIngressPolicy(ctx, value.Metadata.Name, "incoming", "webhook"); err != ErrTriggerNotFound {
		t.Fatalf("paused ingress policy error = %v", err)
	}
	if _, _, err := store.IngestEvent(ctx, value.Metadata.Name, "incoming", "webhook", "paused-hook", time.Now(), json.RawMessage(`{}`), nil); err == nil {
		t.Fatal("paused automation accepted webhook")
	}
	if count, err := store.EnqueueDueSchedules(ctx, time.Now().Add(5*time.Minute), 10); err != nil || count != 0 {
		t.Fatalf("paused schedules count=%d err=%v", count, err)
	}

	manualID, created, err := store.EnqueueManualRun(ctx, value.Metadata.Name, "manual-1", revisionID, json.RawMessage(`{"diagnostic":true}`), "agent:test")
	if err != nil || !created {
		t.Fatalf("manual run id=%q created=%v err=%v", manualID, created, err)
	}
	duplicateID, created, err := store.EnqueueManualRun(ctx, value.Metadata.Name, "manual-1", "stale-revision", json.RawMessage(`{"ignored":true}`), "agent:test")
	if err != nil || created || duplicateID != manualID {
		t.Fatalf("duplicate id=%q created=%v err=%v", duplicateID, created, err)
	}
	if _, _, err := store.EnqueueManualRun(ctx, value.Metadata.Name, "manual-stale-target", "rev-stale", json.RawMessage(`{}`), "agent:test"); err != ErrAutomationRevisionChanged {
		t.Fatalf("stale manual-run revision error = %v", err)
	}
	manual, err := store.GetRun(ctx, manualID)
	if err != nil || manual.Status != domain.RunQueued {
		t.Fatalf("manual run = %#v err=%v", manual, err)
	}

	changed, err = store.SetAutomationEnabled(ctx, value.Metadata.Name, true, "agent:test")
	if err != nil || !changed {
		t.Fatalf("resume changed=%v err=%v", changed, err)
	}
	if _, err := store.GetTriggerIngressPolicy(ctx, value.Metadata.Name, "incoming", "webhook"); err != nil {
		t.Fatalf("resumed ingress policy error = %v", err)
	}
	detail, err = store.GetAutomation(ctx, value.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	for _, trigger := range detail.Triggers {
		if trigger.Type == "schedule" && (trigger.NextFireAt == nil || !trigger.NextFireAt.After(time.Now())) {
			t.Fatalf("resumed schedule nextFireAt = %v", trigger.NextFireAt)
		}
	}
	if _, created, err := store.IngestEvent(ctx, value.Metadata.Name, "incoming", "webhook", "resumed-hook", time.Now(), json.RawMessage(`{}`), nil); err != nil || !created {
		t.Fatalf("resumed webhook created=%v err=%v", created, err)
	}
	var webhookAvailableAt time.Time
	if err := store.pool.QueryRow(ctx, `SELECT available_at FROM runs r JOIN events e ON e.id = r.event_id WHERE e.external_id = 'resumed-hook'`).Scan(&webhookAvailableAt); err != nil {
		t.Fatal(err)
	}
	if webhookAvailableAt.Before(time.Now().Add(4 * time.Minute)) {
		t.Fatalf("delayed webhook available_at=%s", webhookAvailableAt)
	}

	runs, err := store.ListRunsFiltered(ctx, value.Metadata.Name, domain.RunQueued, 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("queued runs=%d err=%v", len(runs), err)
	}
	audit, err := store.ListAuditEvents(ctx, value.Metadata.Name, 10)
	if err != nil {
		t.Fatal(err)
	}
	actions := make(map[string]bool)
	for _, event := range audit {
		actions[event.Action] = true
	}
	for _, action := range []string{"automation.deployed", "automation.paused", "run.queued_manually", "automation.resumed"} {
		if !actions[action] {
			t.Errorf("audit action %q missing from %#v", action, actions)
		}
	}

	const concurrentRevisionID = "rev_concurrent_activation"
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO revisions (id, automation_id, content_hash, manifest, artifact_path)
		SELECT $1, automation_id, $2, manifest, artifact_path FROM revisions WHERE id = $3`,
		concurrentRevisionID, strings.Repeat("a", 64), revisionID); err != nil {
		t.Fatal(err)
	}
	activation, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer activation.Rollback(ctx) //nolint:errcheck
	if _, err := activation.Exec(ctx, `
		UPDATE automations SET active_revision_id = $2, updated_at = now()
		WHERE id = $1`, value.Metadata.Name, concurrentRevisionID); err != nil {
		t.Fatal(err)
	}
	type enqueueResult struct{ err error }
	result := make(chan enqueueResult, 1)
	go func() {
		_, _, enqueueErr := store.EnqueueManualRun(ctx, value.Metadata.Name, "manual-concurrent-target", revisionID, json.RawMessage(`{}`), "agent:test")
		result <- enqueueResult{err: enqueueErr}
	}()
	waitForLockWaiters := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var count int
			if err := store.pool.QueryRow(ctx, `
				SELECT count(*) FROM pg_stat_activity
				WHERE datname = current_database() AND pid <> pg_backend_pid()
					AND wait_event_type = 'Lock'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count >= want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %d blocked database request(s)", want)
	}
	waitForLockWaiters(1)
	if err := activation.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.err != ErrAutomationRevisionChanged {
			t.Fatalf("concurrent activation manual-run error = %v", got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("manual run did not resume after concurrent activation committed")
	}

	original, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Rollback(ctx) //nolint:errcheck
	var replayExpectedRevisionID string
	if err := original.QueryRow(ctx, `SELECT active_revision_id FROM automations WHERE id = $1 FOR UPDATE`, value.Metadata.Name).Scan(&replayExpectedRevisionID); err != nil {
		t.Fatal(err)
	}
	const concurrentExternalID = "manual-concurrent-replay"
	const concurrentEventID = "evt_concurrent_replay"
	const concurrentRunID = "run_concurrent_replay"
	now := time.Now().UTC()
	if _, err := original.Exec(ctx, `
		INSERT INTO events (id, trigger_key, external_id, envelope, occurred_at, received_at)
		VALUES ($1, $2, $3, '{}'::jsonb, $4, $4)`,
		concurrentEventID, value.Metadata.Name+":manual", concurrentExternalID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := original.Exec(ctx, `
		INSERT INTO runs (id, automation_id, revision_id, event_id, status, max_attempts, concurrency_policy)
		VALUES ($1, $2, $3, $4, $5, 1, 'allow')`,
		concurrentRunID, value.Metadata.Name, replayExpectedRevisionID, concurrentEventID, domain.RunQueued); err != nil {
		t.Fatal(err)
	}
	activationResult := make(chan error, 1)
	go func() {
		_, rollbackErr := store.RollbackAutomation(ctx, value.Metadata.Name, revisionID, "agent:test")
		activationResult <- rollbackErr
	}()
	waitForLockWaiters(1)
	type replayResult struct {
		id      string
		created bool
		err     error
	}
	replay := make(chan replayResult, 1)
	go func() {
		id, wasCreated, replayErr := store.EnqueueManualRun(ctx, value.Metadata.Name, concurrentExternalID, replayExpectedRevisionID, json.RawMessage(`{}`), "agent:test")
		replay <- replayResult{id: id, created: wasCreated, err: replayErr}
	}()
	waitForLockWaiters(2)
	if err := original.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-activationResult; err != nil {
		t.Fatalf("concurrent rollback: %v", err)
	}
	select {
	case got := <-replay:
		if got.err != nil || got.created || got.id != concurrentRunID {
			t.Fatalf("concurrent replay id=%q created=%v err=%v", got.id, got.created, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("idempotent replay did not resume after concurrent run and activation committed")
	}
}
