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

func TestTransactionalAutomationStateIntegration(t *testing.T) {
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
	if _, err := store.pool.Exec(ctx, `TRUNCATE deployments, audit_events, automation_state, runs, events, triggers, revisions, automations CASCADE`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = store.pool.Exec(context.Background(), `TRUNCATE deployments, audit_events, automation_state, runs, events, triggers, revisions, automations CASCADE`)
	}()

	manifest := domain.Manifest{
		APIVersion: "werkt.dev/v1",
		Kind:       "Automation",
		Metadata:   domain.Metadata{Name: "stateful-example", Project: "integration"},
		Triggers: []domain.Trigger{{
			ID: "hourly", Type: "schedule", Config: map[string]any{"cron": "0 * * * *", "timezone": "UTC"},
		}},
		Runtime: domain.Runtime{Language: "go", Command: []string{"./automation"}},
		Execution: domain.Execution{
			Concurrency: "forbid",
			State:       domain.StatePolicy{Enabled: true},
		},
	}
	contentHash := strings.Repeat("a", 64)
	if _, err := store.Deploy(ctx, manifest, contentHash, testStorageFixture(t, dataDir, "artifacts", contentHash), testArtifactProvenance(manifest, contentHash)); err != nil {
		t.Fatal(err)
	}

	firstID, created, err := store.EnqueueManualRun(ctx, manifest.Metadata.Name, "state-1", "", json.RawMessage(`{}`), "test")
	if err != nil || !created {
		t.Fatalf("first run id=%q created=%v err=%v", firstID, created, err)
	}
	first, err := store.AcquireRun(ctx, "worker-1", time.Minute)
	if err != nil || first == nil {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	if first.StateVersion != 0 || string(first.State) != `{}` {
		t.Fatalf("initial state version=%d value=%s", first.StateVersion, first.State)
	}
	if err := store.CompleteRun(ctx, *first, "worker-1", "", json.RawMessage(`{"ok":true}`), json.RawMessage(`{"processed":{"issue-1":true}}`), domain.RunControl{}); err != nil {
		t.Fatal(err)
	}

	secondID, created, err := store.EnqueueManualRun(ctx, manifest.Metadata.Name, "state-2", "", json.RawMessage(`{}`), "test")
	if err != nil || !created {
		t.Fatalf("second run id=%q created=%v err=%v", secondID, created, err)
	}
	second, err := store.AcquireRun(ctx, "worker-2", time.Minute)
	if err != nil || second == nil {
		t.Fatalf("second claim=%#v err=%v", second, err)
	}
	var persisted map[string]map[string]bool
	if err := json.Unmarshal(second.State, &persisted); err != nil {
		t.Fatal(err)
	}
	if second.StateVersion != 1 || !persisted["processed"]["issue-1"] {
		t.Fatalf("persisted state version=%d value=%s", second.StateVersion, second.State)
	}

	if _, err := store.pool.Exec(ctx, `UPDATE automation_state SET version = version + 1 WHERE automation_id = $1`, manifest.Metadata.Name); err != nil {
		t.Fatal(err)
	}
	err = store.CompleteRun(ctx, *second, "worker-2", "", json.RawMessage(`{}`), json.RawMessage(`{"processed":{}}`), domain.RunControl{})
	if !errors.Is(err, ErrAutomationStateConflict) {
		t.Fatalf("conflicting completion error=%v", err)
	}
	var status string
	if err := store.pool.QueryRow(ctx, `SELECT status FROM runs WHERE id = $1`, second.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != domain.RunRunning {
		t.Fatalf("conflicting completion partially committed run status=%q", status)
	}
}
