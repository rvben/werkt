package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rvben/werkt/internal/domain"
	"github.com/rvben/werkt/internal/runner"
)

type staticSecrets map[string]string

func (s staticSecrets) Resolve(_ context.Context, names []string) (map[string]string, error) {
	values := make(map[string]string, len(names))
	for _, name := range names {
		value, ok := s[name]
		if !ok {
			return nil, errors.New("secret not found")
		}
		values[name] = value
	}
	return values, nil
}

func TestProcessRunnerUsesLanguageNeutralContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-specific")
	}
	directory := t.TempDir()
	t.Setenv("WERKT_MANAGEMENT_TOKEN", "must-not-be-inherited")
	script := []byte("#!/bin/sh\nset -eu\ntest \"$SERVICE_TOKEN\" = runtime-secret-value\ntest -z \"${WERKT_MANAGEMENT_TOKEN+x}\"\necho \"running $SERVICE_TOKEN\"\nprintf '{\"ok\":true}' > \"$WERKT_RESULT_PATH\"\n")
	path := filepath.Join(directory, "run.sh")
	if err := os.WriteFile(path, script, 0o700); err != nil {
		t.Fatal(err)
	}
	eventData := json.RawMessage(`{"message":"hello"}`)
	value := domain.RunnableRun{
		Run:          domain.Run{ID: "run_test", AutomationID: "example", RevisionID: "rev_test"},
		ArtifactPath: directory,
		Manifest: domain.Manifest{
			Runtime: domain.Runtime{
				Language: "shell",
				Command:  []string{"./run.sh"},
				Secrets:  map[string]string{"SERVICE_TOKEN": "ops/runtime-token"},
			},
			Execution: domain.Execution{Timeout: "5s"},
		},
		Event: domain.EventEnvelope{ID: "evt_test", Data: eventData},
	}

	result, err := runner.NewProcessRunner(staticSecrets{"ops/runtime-token": "runtime-secret-value"}).Execute(context.Background(), value)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if string(result.Output) != `{"ok":true}` {
		t.Fatalf("output = %s", result.Output)
	}
	if result.Logs != "[stdout]\nrunning [REDACTED]\n" {
		t.Fatalf("logs = %q", result.Logs)
	}
}

func TestProcessRunnerRejectsManifestEgressInsteadOfIgnoringIt(t *testing.T) {
	runtime := domain.Runtime{
		Command: []string{"true"},
		Egress:  []domain.EgressRule{{Host: "api.example.com", Port: 443}},
	}
	runner := runner.NewProcessRunner()

	buildErr := runner.Build(context.Background(), t.TempDir(), domain.Manifest{Runtime: runtime}, nil)
	if buildErr == nil || !strings.Contains(buildErr.Error(), "cannot enforce network policy") {
		t.Fatalf("Build() error = %v", buildErr)
	}

	_, executeErr := runner.Execute(context.Background(), domain.RunnableRun{Manifest: domain.Manifest{Runtime: runtime}})
	if executeErr == nil || !strings.Contains(executeErr.Error(), "cannot enforce network policy") {
		t.Fatalf("Execute() error = %v", executeErr)
	}
}

func TestProcessRunnerBuildsWithManifestEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-specific")
	}
	directory := t.TempDir()
	t.Setenv("WERKT_SECRET_KEY", "must-not-reach-build")
	value := domain.Manifest{Runtime: domain.Runtime{
		Build:       []string{"/bin/sh", "-c", `test -z "${WERKT_SECRET_KEY+x}"; printf '%s' "$BUILD_VALUE" > built`},
		Environment: map[string]string{"BUILD_VALUE": "from-manifest"},
	}}
	if err := runner.NewProcessRunner().Build(context.Background(), directory, value, nil); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(directory, "built"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "from-manifest" {
		t.Fatalf("built contents = %q", contents)
	}
}

func TestProcessRunnerRunsPromotionChecksInOrderAndReportsDiagnostics(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-specific")
	}
	directory := t.TempDir()
	value := domain.Manifest{
		Runtime: domain.Runtime{
			Build:       []string{"/bin/sh", "-c", `printf build >> order; echo built`},
			Environment: map[string]string{"CHECK_VALUE": "expected"},
		},
		Deployment: domain.DeploymentPolicy{Checks: []domain.DeploymentCheck{
			{ID: "unit", Command: []string{"/bin/sh", "-c", `test "$CHECK_VALUE" = expected; printf unit >> order; echo checked`}},
		}},
	}
	var updates []domain.DeploymentStepUpdate
	err := runner.NewProcessRunner().Build(context.Background(), directory, value, func(update domain.DeploymentStepUpdate) error {
		updates = append(updates, update)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	order, err := os.ReadFile(filepath.Join(directory, "order"))
	if err != nil {
		t.Fatal(err)
	}
	if string(order) != "buildunit" {
		t.Fatalf("promotion order=%q", order)
	}
	if len(updates) != 4 || updates[0].ID != "build" || updates[1].Logs != "built\n" || updates[2].ID != "check:unit" || updates[3].Status != domain.DeploymentStepSucceeded {
		t.Fatalf("updates=%#v", updates)
	}
}

func TestProcessRunnerStopsAfterFailedPromotionCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-specific")
	}
	directory := t.TempDir()
	value := domain.Manifest{
		Deployment: domain.DeploymentPolicy{Checks: []domain.DeploymentCheck{
			{ID: "unit", Command: []string{"/bin/sh", "-c", `echo failure-output; exit 7`}},
			{ID: "later", Command: []string{"/bin/sh", "-c", `touch should-not-run`}},
		}},
	}
	var updates []domain.DeploymentStepUpdate
	err := runner.NewProcessRunner().Build(context.Background(), directory, value, func(update domain.DeploymentStepUpdate) error {
		updates = append(updates, update)
		return nil
	})
	if err == nil {
		t.Fatal("Build() error = nil")
	}
	if len(updates) != 2 || updates[1].Status != domain.DeploymentStepFailed || updates[1].Logs != "failure-output\n" {
		t.Fatalf("updates=%#v", updates)
	}
	if _, err := os.Stat(filepath.Join(directory, "should-not-run")); !os.IsNotExist(err) {
		t.Fatalf("later check ran: %v", err)
	}
}
