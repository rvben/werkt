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

func TestSecretStorageAndRevisionGuardsIntegration(t *testing.T) {
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
		_, _ = store.pool.Exec(context.Background(), `TRUNCATE deployments, audit_events, runs, events, triggers, revisions, automations, secrets CASCADE`)
	}
	reset()
	defer reset()

	metadata, created, err := store.PutEncryptedSecret(ctx, "ops/runtime-token", "runtime", []byte("ciphertext-v1"), "key-one", "agent:test")
	if err != nil || !created || metadata.Version != 1 {
		t.Fatalf("create metadata=%+v created=%v err=%v", metadata, created, err)
	}
	metadata, created, err = store.PutEncryptedSecret(ctx, "ops/runtime-token", "rotated", []byte("ciphertext-v2"), "key-one", "agent:test")
	if err != nil || created || metadata.Version != 2 {
		t.Fatalf("rotate metadata=%+v created=%v err=%v", metadata, created, err)
	}
	encrypted, err := store.GetEncryptedSecret(ctx, "ops/runtime-token")
	if err != nil || string(encrypted.Ciphertext) != "ciphertext-v2" || encrypted.KeyID != "key-one" {
		t.Fatalf("encrypted=%+v err=%v", encrypted, err)
	}

	manifest := domain.Manifest{
		APIVersion: "werkt.dev/v1", Kind: "Automation",
		Metadata: domain.Metadata{Name: "secret-consumer", Project: "ops"},
		Triggers: []domain.Trigger{{ID: "minute", Type: "schedule", Config: map[string]any{"cron": "* * * * *"}}},
		Runtime:  domain.Runtime{Language: "shell", Command: []string{"true"}, Secrets: map[string]string{"TOKEN": "ops/runtime-token"}},
	}
	if _, err := store.Deploy(ctx, manifest, strings.Repeat("a", 64), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSecret(ctx, "ops/runtime-token", "agent:test"); !errors.Is(err, ErrSecretInUse) {
		t.Fatalf("DeleteSecret() error=%v, want in-use guard", err)
	}

	manifest.Metadata.Name = "missing-consumer"
	manifest.Runtime.Secrets["TOKEN"] = "ops/missing"
	if _, err := store.Deploy(ctx, manifest, strings.Repeat("b", 64), t.TempDir()); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("Deploy() error=%v, want missing secret", err)
	}
	if _, err := store.pool.Exec(ctx, `DELETE FROM automations WHERE id = 'secret-consumer'`); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSecret(ctx, "ops/runtime-token", "agent:test"); err != nil {
		t.Fatal(err)
	}
	audit, err := store.ListAuditEvents(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	actions := make(map[string]bool)
	for _, event := range audit {
		actions[event.Action] = true
	}
	for _, action := range []string{"secret.created", "secret.rotated", "secret.deleted"} {
		if !actions[action] {
			t.Errorf("audit action %s missing", action)
		}
	}
}
