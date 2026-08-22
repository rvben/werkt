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

type ProcessRunner struct {
	secrets SecretResolver
}

func NewProcessRunner(resolvers ...SecretResolver) *ProcessRunner {
	var resolver SecretResolver
	if len(resolvers) > 0 {
		resolver = resolvers[0]
	}
	return &ProcessRunner{secrets: resolver}
}

func (r *ProcessRunner) Build(ctx context.Context, directory string, value domain.Manifest, reporter domain.DeploymentStepReporter) error {
	if len(value.Runtime.Build) > 0 {
		if err := runPromotionCommand(ctx, directory, value.Runtime.Environment, "build", "build", value.Runtime.Build, reporter); err != nil {
			return fmt.Errorf("build automation: %w", err)
		}
	}
	for _, check := range value.Deployment.Checks {
		checkContext, cancel := context.WithTimeout(ctx, check.TimeoutDuration())
		err := runPromotionCommand(checkContext, directory, value.Runtime.Environment, "check:"+check.ID, "check", check.Command, reporter)
		cancel()
		if err != nil {
			if errors.Is(checkContext.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("check %s exceeded timeout %s: %w", check.ID, check.TimeoutDuration(), checkContext.Err())
			}
			return fmt.Errorf("check %s: %w", check.ID, err)
		}
	}
	return nil
}

func runPromotionCommand(ctx context.Context, directory string, values map[string]string, id, kind string, argv []string, reporter domain.DeploymentStepReporter) error {
	if reporter != nil {
		if err := reporter(domain.DeploymentStepUpdate{ID: id, Kind: kind, Status: domain.DeploymentStepRunning}); err != nil {
			return err
		}
	}
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Dir = directory
	command.Env = append(inheritedRuntimeEnvironment(), environment(values)...)
	var output limitedBuffer
	command.Stdout = &output
	command.Stderr = &output
	commandErr := command.Run()
	logs := output.String()
	status := domain.DeploymentStepSucceeded
	message := ""
	if commandErr != nil {
		status = domain.DeploymentStepFailed
		message = commandErr.Error()
	}
	if reporter != nil {
		if err := reporter(domain.DeploymentStepUpdate{ID: id, Kind: kind, Status: status, Logs: logs, Error: message}); err != nil {
			return errors.Join(commandErr, err)
		}
	}
	if commandErr != nil {
		return fmt.Errorf("process failed: %w%s", commandErr, errorLogs(logs))
	}
	return nil
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	remaining := maxLogs - b.buffer.Len()
	if remaining > 0 {
		written := len(value)
		if written > remaining {
			written = remaining
			b.truncated = true
		}
		_, _ = b.buffer.Write(value[:written])
	} else {
		b.truncated = true
	}
	return len(value), nil
}

func (b *limitedBuffer) String() string {
	value := b.buffer.String()
	if b.truncated {
		value += "\n[logs truncated]\n"
	}
	return value
}

func (r *ProcessRunner) Execute(parent context.Context, run domain.RunnableRun) (Result, error) {
	if len(run.Manifest.Runtime.Command) == 0 {
		return Result{}, errors.New("runtime command is empty")
	}
	ctx, cancel := context.WithTimeout(parent, run.Manifest.Execution.TimeoutDuration())
	defer cancel()
	resolved, err := resolveRuntimeEnvironment(parent, r.secrets, run.Manifest.Runtime)
	if err != nil {
		return Result{}, err
	}
	redactor := newLogRedactor(resolved.secrets)

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
	command.Env = append(inheritedRuntimeEnvironment(), runtimeEnvironment(run, eventPath, resultPath, resolved.values)...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	commandErr := command.Run()
	logs := formatLogs(redactor.Redact(stdout.String()), redactor.Redact(stderr.String()))
	if ctx.Err() != nil {
		return Result{Logs: logs}, fmt.Errorf("automation exceeded timeout %s: %w", run.Manifest.Execution.TimeoutDuration(), ctx.Err())
	}
	if commandErr != nil {
		return Result{Logs: logs}, fmt.Errorf("automation process failed: %w", redactor.Error(commandErr))
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

func runtimeEnvironment(run domain.RunnableRun, eventPath, resultPath string, values map[string]string) []string {
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

func inheritedRuntimeEnvironment() []string {
	keys := []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR", "SYSTEMROOT"}
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		if value, exists := os.LookupEnv(key); exists {
			values = append(values, key+"="+value)
		}
	}
	return values
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
