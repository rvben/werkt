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
  egress:
    - host: api.example.com
      port: 443
deployment:
  checks:
    - id: unit
      command: [python3, -m, unittest]
      timeout: 2m
execution:
  timeout: 30s
  retries: 2
  concurrency: forbid
  state:
    enabled: true
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
	if len(value.Runtime.Egress) != 1 || value.Runtime.Egress[0].EffectiveProtocol() != "tcp" {
		t.Fatalf("runtime egress = %#v", value.Runtime.Egress)
	}
	if len(value.Deployment.Checks) != 1 || value.Deployment.Checks[0].TimeoutDuration() != 2*time.Minute {
		t.Fatalf("deployment checks = %#v", value.Deployment.Checks)
	}
	if !value.Execution.State.Enabled {
		t.Fatal("execution state was not enabled")
	}
}

func TestValidateRequiresSerializedRunsForTransactionalState(t *testing.T) {
	value := domain.Manifest{
		APIVersion: "werkt.dev/v1",
		Kind:       "Automation",
		Metadata:   domain.Metadata{Name: "stateful", Project: "personal"},
		Triggers:   []domain.Trigger{{ID: "hourly", Type: "schedule", Config: map[string]any{"cron": "0 * * * *"}}},
		Runtime:    domain.Runtime{Language: "python", Command: []string{"python3", "main.py"}},
		Execution:  domain.Execution{State: domain.StatePolicy{Enabled: true}},
	}
	if err := manifest.Validate(value); err == nil || !strings.Contains(err.Error(), "concurrency: forbid") {
		t.Fatalf("Validate() error = %v", err)
	}
	value.Execution.Concurrency = "forbid"
	if err := manifest.Validate(value); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsUnsafeEgressPolicies(t *testing.T) {
	base := domain.Manifest{
		APIVersion: "werkt.dev/v1",
		Kind:       "Automation",
		Metadata:   domain.Metadata{Name: "example", Project: "personal"},
		Triggers:   []domain.Trigger{{ID: "hourly", Type: "schedule", Config: map[string]any{"cron": "0 * * * *"}}},
		Runtime: domain.Runtime{
			Language: "go",
			Command:  []string{"./example"},
			Egress: []domain.EgressRule{
				{Host: "https://api.example.com/path", Port: 443},
				{Host: "::1", Port: 443},
				{Host: "api.example.com", Port: 0},
				{Host: "api.example.com", Port: 443, Protocol: "sctp"},
			},
		},
	}

	err := manifest.Validate(base)
	if err == nil {
		t.Fatal("Validate() error = nil")
	}
	for _, want := range []string{"without a scheme", "IPv6", "between 1 and 65535", "tcp or udp"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error = %v, want %q", err, want)
		}
	}

	base.Runtime.Egress = make([]domain.EgressRule, 33)
	for index := range base.Runtime.Egress {
		base.Runtime.Egress[index] = domain.EgressRule{Host: "api.example.com", Port: 443}
	}
	if err := manifest.Validate(base); err == nil || !strings.Contains(err.Error(), "at most 32") {
		t.Fatalf("Validate() max-rule error = %v", err)
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
			{ID: "hook", Type: "webhook", Config: map[string]any{"secret": "personal/webhook-secret"}},
			{ID: "mail", Type: "email", Config: map[string]any{"tokenSecret": "personal/email-token"}},
		},
		Runtime: domain.Runtime{
			Language: "python",
			Command:  []string{"python3", "main.py"},
			Secrets:  map[string]string{"SERVICE_TOKEN": "personal/service-token"},
		},
	}
	if err := manifest.Validate(value); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	value.Triggers[0].Config = nil
	value.Runtime.Secrets["MANAGEMENT_TOKEN"] = "UPPER_CASE"
	err := manifest.Validate(value)
	if err == nil {
		t.Fatal("Validate() error = nil")
	}
	if !strings.Contains(err.Error(), "config.secret") || !strings.Contains(err.Error(), "must name a Werkt secret") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateAcceptsGitHubWebhookProviderAndRejectsCustomHeaders(t *testing.T) {
	value := domain.Manifest{
		APIVersion: "werkt.dev/v1",
		Kind:       "Automation",
		Metadata:   domain.Metadata{Name: "github-hook", Project: "personal"},
		Triggers: []domain.Trigger{{ID: "issues", Type: "webhook", Config: map[string]any{
			"provider": "github",
			"secret":   "personal/github-webhook",
		}}},
		Runtime: domain.Runtime{Language: "go", Command: []string{"./automation"}},
	}
	if err := manifest.Validate(value); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	value.Triggers[0].Config["signatureHeader"] = "X-Anything"
	if err := manifest.Validate(value); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("Validate() error = %v", err)
	}
	delete(value.Triggers[0].Config, "signatureHeader")
	value.Triggers[0].Config["provider"] = "unknown"
	if err := manifest.Validate(value); err == nil || !strings.Contains(err.Error(), "werkt or github") {
		t.Fatalf("Validate() error = %v", err)
	}
}
