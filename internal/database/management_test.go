package database

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/rvben/werkt/internal/domain"
)

func TestManagementLifecycleIntegration(t *testing.T) {
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
		if _, err := store.pool.Exec(ctx, `TRUNCATE audit_events, runs, events, triggers, revisions, automations CASCADE`); err != nil {
			t.Errorf("reset database: %v", err)
		}
	}
	reset()
	defer reset()

	enabled := true
	value := domain.Manifest{
		APIVersion: "werkt.dev/v1",
		Kind:       "Automation",
		Metadata: domain.Metadata{
			Name: "managed-example", Project: "operations", Folder: "alerts", Labels: []string{"critical"},
		},
		Triggers: []domain.Trigger{
			{ID: "incoming", Type: "webhook", Enabled: &enabled},
			{ID: "minute", Type: "schedule", Enabled: &enabled, Config: map[string]any{"cron": "* * * * *", "timezone": "UTC"}},
		},
		Runtime:   domain.Runtime{Language: "go", Command: []string{"./automation"}},
		Execution: domain.Execution{Retries: 1, Concurrency: "forbid"},
	}
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	revisionID, err := store.Deploy(ctx, value, hash, t.TempDir())
	if err != nil {
		t.Fatal(err)
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
	detail, err := store.GetAutomation(ctx, value.Metadata.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Triggers) != 2 || len(detail.Revisions) != 1 || !detail.Enabled {
		t.Fatalf("detail = %#v", detail)
	}
	for _, trigger := range detail.Triggers {
		if trigger.ID == "incoming" && string(trigger.Config) != `{}` {
			t.Fatalf("empty trigger config = %s, want {}", trigger.Config)
		}
	}

	changed, err := store.SetAutomationEnabled(ctx, value.Metadata.Name, false, "agent:test")
	if err != nil || !changed {
		t.Fatalf("pause changed=%v err=%v", changed, err)
	}
	if _, _, err := store.IngestEvent(ctx, value.Metadata.Name, "incoming", "webhook", "paused-hook", time.Now(), json.RawMessage(`{}`), nil); err == nil {
		t.Fatal("paused automation accepted webhook")
	}
	if count, err := store.EnqueueDueSchedules(ctx, time.Now().Add(5*time.Minute), 10); err != nil || count != 0 {
		t.Fatalf("paused schedules count=%d err=%v", count, err)
	}

	manualID, created, err := store.EnqueueManualRun(ctx, value.Metadata.Name, "manual-1", json.RawMessage(`{"diagnostic":true}`), "agent:test")
	if err != nil || !created {
		t.Fatalf("manual run id=%q created=%v err=%v", manualID, created, err)
	}
	duplicateID, created, err := store.EnqueueManualRun(ctx, value.Metadata.Name, "manual-1", json.RawMessage(`{"ignored":true}`), "agent:test")
	if err != nil || created || duplicateID != manualID {
		t.Fatalf("duplicate id=%q created=%v err=%v", duplicateID, created, err)
	}
	manual, err := store.GetRun(ctx, manualID)
	if err != nil || manual.Status != domain.RunQueued {
		t.Fatalf("manual run = %#v err=%v", manual, err)
	}

	changed, err = store.SetAutomationEnabled(ctx, value.Metadata.Name, true, "agent:test")
	if err != nil || !changed {
		t.Fatalf("resume changed=%v err=%v", changed, err)
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
}
