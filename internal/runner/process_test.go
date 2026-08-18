package runner_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/rvben/werkt/internal/domain"
	"github.com/rvben/werkt/internal/runner"
)

func TestProcessRunnerUsesLanguageNeutralContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-specific")
	}
	directory := t.TempDir()
	script := []byte("#!/bin/sh\nset -eu\necho running\nprintf '{\"ok\":true}' > \"$WERKT_RESULT_PATH\"\n")
	path := filepath.Join(directory, "run.sh")
	if err := os.WriteFile(path, script, 0o700); err != nil {
		t.Fatal(err)
	}
	eventData := json.RawMessage(`{"message":"hello"}`)
	value := domain.RunnableRun{
		Run:          domain.Run{ID: "run_test", AutomationID: "example", RevisionID: "rev_test"},
		ArtifactPath: directory,
		Manifest: domain.Manifest{
			Runtime:   domain.Runtime{Language: "shell", Command: []string{"./run.sh"}},
			Execution: domain.Execution{Timeout: "5s"},
		},
		Event: domain.EventEnvelope{ID: "evt_test", Data: eventData},
	}

	result, err := runner.NewProcessRunner().Execute(context.Background(), value)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if string(result.Output) != `{"ok":true}` {
		t.Fatalf("output = %s", result.Output)
	}
	if result.Logs != "[stdout]\nrunning\n" {
		t.Fatalf("logs = %q", result.Logs)
	}
}

func TestProcessRunnerBuildsWithManifestEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is POSIX-specific")
	}
	directory := t.TempDir()
	value := domain.Manifest{Runtime: domain.Runtime{
		Build:       []string{"/bin/sh", "-c", `printf '%s' "$BUILD_VALUE" > built`},
		Environment: map[string]string{"BUILD_VALUE": "from-manifest"},
	}}
	if err := runner.NewProcessRunner().Build(context.Background(), directory, value); err != nil {
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
