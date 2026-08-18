package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rvben/werkt/internal/domain"
)

const maxLogs = 1 << 20

type ProcessRunner struct{}

func NewProcessRunner() *ProcessRunner {
	return &ProcessRunner{}
}

func (r *ProcessRunner) Build(ctx context.Context, directory string, value domain.Manifest) error {
	if len(value.Runtime.Build) == 0 {
		return nil
	}
	command := exec.CommandContext(ctx, value.Runtime.Build[0], value.Runtime.Build[1:]...)
	command.Dir = directory
	command.Env = append(os.Environ(), environment(value.Runtime.Environment)...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build automation: %w\n%s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (r *ProcessRunner) Execute(parent context.Context, run domain.RunnableRun) (Result, error) {
	if len(run.Manifest.Runtime.Command) == 0 {
		return Result{}, errors.New("runtime command is empty")
	}
	ctx, cancel := context.WithTimeout(parent, run.Manifest.Execution.TimeoutDuration())
	defer cancel()

	runDirectory, err := os.MkdirTemp("", "werkt-run-")
	if err != nil {
		return Result{}, fmt.Errorf("create run directory: %w", err)
	}
	defer os.RemoveAll(runDirectory) //nolint:errcheck
	eventPath := filepath.Join(runDirectory, "event.json")
	resultPath := filepath.Join(runDirectory, "result.json")
	eventJSON, err := json.Marshal(run.Event)
	if err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(eventPath, eventJSON, 0o600); err != nil {
		return Result{}, fmt.Errorf("write event: %w", err)
	}

	command := exec.CommandContext(ctx, run.Manifest.Runtime.Command[0], run.Manifest.Runtime.Command[1:]...)
	command.Dir = run.ArtifactPath
	command.Env = append(os.Environ(), runtimeEnvironment(run, eventPath, resultPath)...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	commandErr := command.Run()
	logs := formatLogs(stdout.String(), stderr.String())
	if ctx.Err() != nil {
		return Result{Logs: logs}, fmt.Errorf("automation exceeded timeout %s: %w", run.Manifest.Execution.TimeoutDuration(), ctx.Err())
	}
	if commandErr != nil {
		return Result{Logs: logs}, fmt.Errorf("automation process failed: %w", commandErr)
	}

	resultJSON, err := os.ReadFile(resultPath)
	if errors.Is(err, os.ErrNotExist) {
		resultJSON = []byte("{}")
	} else if err != nil {
		return Result{Logs: logs}, fmt.Errorf("read automation result: %w", err)
	}
	if !json.Valid(resultJSON) {
		return Result{Logs: logs}, errors.New("automation result is not valid JSON")
	}
	return Result{Output: resultJSON, Logs: logs}, nil
}

func runtimeEnvironment(run domain.RunnableRun, eventPath, resultPath string) []string {
	values := make(map[string]string, len(run.Manifest.Runtime.Environment)+5)
	for key, value := range run.Manifest.Runtime.Environment {
		values[key] = value
	}
	reserved := map[string]string{
		"WERKT_AUTOMATION_ID": run.AutomationID,
		"WERKT_REVISION_ID":   run.RevisionID,
		"WERKT_RUN_ID":        run.ID,
		"WERKT_EVENT_PATH":    eventPath,
		"WERKT_RESULT_PATH":   resultPath,
	}
	for key, value := range reserved {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func environment(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}

func formatLogs(stdout, stderr string) string {
	var result strings.Builder
	if stdout != "" {
		result.WriteString("[stdout]\n")
		result.WriteString(stdout)
		if !strings.HasSuffix(stdout, "\n") {
			result.WriteByte('\n')
		}
	}
	if stderr != "" {
		result.WriteString("[stderr]\n")
		result.WriteString(stderr)
		if !strings.HasSuffix(stderr, "\n") {
			result.WriteByte('\n')
		}
	}
	value := result.String()
	if len(value) > maxLogs {
		value = value[:maxLogs] + "\n[logs truncated]\n"
	}
	return value
}
