package manifest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rvben/werkt/internal/domain"
	"github.com/rvben/werkt/internal/manifest"
)

func TestLoadValidManifest(t *testing.T) {
	directory := t.TempDir()
	contents := []byte(`apiVersion: werkt.dev/v1
kind: Automation
metadata:
  name: example
  project: personal
triggers:
  - id: every-hour
    type: schedule
    config:
      cron: "0 * * * *"
runtime:
  language: python
  image: python:3.13-alpine
  buildImage: python:3.13-alpine
  command: [python3, main.py]
deployment:
  checks:
    - id: unit
      command: [python3, -m, unittest]
      timeout: 2m
execution:
  timeout: 30s
  retries: 2
`)
	if err := os.WriteFile(filepath.Join(directory, manifest.Filename), contents, 0o600); err != nil {
		t.Fatal(err)
	}

	value, err := manifest.Load(directory)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if value.Metadata.Name != "example" {
		t.Fatalf("name = %q, want example", value.Metadata.Name)
	}
	if value.Execution.TimeoutDuration().String() != "30s" {
		t.Fatalf("timeout = %s, want 30s", value.Execution.TimeoutDuration())
	}
	if value.Runtime.Image != "python:3.13-alpine" {
		t.Fatalf("image = %q, want python:3.13-alpine", value.Runtime.Image)
	}
	if value.Runtime.BuildImage != "python:3.13-alpine" {
		t.Fatalf("build image = %q, want python:3.13-alpine", value.Runtime.BuildImage)
	}
	if len(value.Deployment.Checks) != 1 || value.Deployment.Checks[0].TimeoutDuration() != 2*time.Minute {
		t.Fatalf("deployment checks = %#v", value.Deployment.Checks)
	}
}

func TestValidateRejectsInvalidDeploymentChecks(t *testing.T) {
	value := domain.Manifest{
		APIVersion: "werkt.dev/v1",
		Kind:       "Automation",
		Metadata:   domain.Metadata{Name: "example", Project: "personal"},
		Triggers:   []domain.Trigger{{ID: "hourly", Type: "schedule", Config: map[string]any{"cron": "0 * * * *"}}},
		Runtime:    domain.Runtime{Language: "go", Command: []string{"./example"}},
		Deployment: domain.DeploymentPolicy{Checks: []domain.DeploymentCheck{
			{ID: "unit", Command: []string{"go", "test", "./..."}, Timeout: "1m"},
			{ID: "unit", Command: []string{" "}, Timeout: "never"},
		}},
	}

	err := manifest.Validate(value)
	if err == nil {
		t.Fatal("Validate() error = nil")
	}
	for _, want := range []string{"id must be unique", "command must contain an executable", "timeout must be a positive duration"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error = %v, want %q", err, want)
		}
	}
}

func TestValidateRejectsEmptyBuildExecutable(t *testing.T) {
	value := domain.Manifest{
		APIVersion: "werkt.dev/v1",
		Kind:       "Automation",
		Metadata:   domain.Metadata{Name: "example", Project: "personal"},
		Triggers:   []domain.Trigger{{ID: "hook", Type: "webhook"}},
		Runtime: domain.Runtime{
			Language: "go",
			Build:    []string{" ", "build"},
			Command:  []string{"./example"},
		},
	}
	if err := manifest.Validate(value); err == nil {
		t.Fatal("Validate() error = nil, want empty build executable error")
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	directory := t.TempDir()
	contents := []byte(`apiVersion: werkt.dev/v1
kind: Automation
metadata:
  name: example
  project: personal
  typo: true
triggers:
  - id: hook
    type: webhook
runtime:
  language: python
  command: [python3, main.py]
`)
	if err := os.WriteFile(filepath.Join(directory, manifest.Filename), contents, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := manifest.Load(directory); err == nil {
		t.Fatal("Load() error = nil, want unknown field error")
	}
}

func TestValidateRejectsReservedRuntimeEnvironment(t *testing.T) {
	value := domain.Manifest{
		APIVersion: "werkt.dev/v1",
		Kind:       "Automation",
		Metadata:   domain.Metadata{Name: "example", Project: "personal"},
		Triggers:   []domain.Trigger{{ID: "hook", Type: "schedule", Config: map[string]any{"cron": "0 * * * *"}}},
		Runtime: domain.Runtime{
			Language:    "python",
			Command:     []string{"python3", "main.py"},
			Environment: map[string]string{"WERKT_EVENT_PATH": "/tmp/fake"},
		},
	}
	if err := manifest.Validate(value); err == nil {
		t.Fatal("Validate() error = nil, want reserved environment error")
	}
}

func TestValidateRequiresIngressCredentialReferencesAndValidatesRuntimeSecrets(t *testing.T) {
	value := domain.Manifest{
		APIVersion: "werkt.dev/v1",
		Kind:       "Automation",
		Metadata:   domain.Metadata{Name: "secure-example", Project: "personal"},
		Triggers: []domain.Trigger{
			{ID: "hook", Type: "webhook", Config: map[string]any{"secretEnv": "EXAMPLE_WEBHOOK_SECRET"}},
			{ID: "mail", Type: "email", Config: map[string]any{"tokenEnv": "EXAMPLE_EMAIL_TOKEN"}},
		},
		Runtime: domain.Runtime{
			Language: "python",
			Command:  []string{"python3", "main.py"},
			Secrets:  map[string]string{"SERVICE_TOKEN": "EXAMPLE_SERVICE_TOKEN"},
		},
	}
	if err := manifest.Validate(value); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	value.Triggers[0].Config = nil
	value.Runtime.Secrets["MANAGEMENT_TOKEN"] = "WERKT_MANAGEMENT_TOKEN"
	err := manifest.Validate(value)
	if err == nil {
		t.Fatal("Validate() error = nil")
	}
	if !strings.Contains(err.Error(), "config.secretEnv") || !strings.Contains(err.Error(), "non-WERKT environment variable") {
		t.Fatalf("Validate() error = %v", err)
	}
}
