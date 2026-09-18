package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rvben/werkt/internal/domain"
)

func stateOfSize(size int) json.RawMessage {
	return json.RawMessage(`{"data":"` + strings.Repeat("x", size-11) + `"}`)
}

func TestProcessRunnerStateLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	for _, tc := range []struct {
		name                 string
		limit, input, output int
		wantError            string
	}{
		{"default accepts above old limit", 0, 128 << 10, 128 << 10, ""},
		{"default exact limit", 0, 1 << 20, 1 << 20, ""},
		{"default rejects input overflow", 0, (1 << 20) + 1, 128 << 10, "initialize automation state"},
		{"default rejects output overflow", 0, 128 << 10, (1 << 20) + 1, "read automation state"},
		{"custom larger limit", 2 << 20, 2 << 20, 2 << 20, ""},
		{"custom smaller limit", 128 << 10, 128 << 10, 128 << 10, ""},
		{"custom rejects input overflow", 128 << 10, (128 << 10) + 1, 128 << 10, "initialize automation state"},
		{"custom rejects output overflow", 128 << 10, 128 << 10, (128 << 10) + 1, "read automation state"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			expectedLimit := automationStateLimit(tc.limit)
			script := fmt.Sprintf("#!/bin/sh\nset -eu\ntest \"$WERKT_STATE_MAX_BYTES\" = %d\ncp output.json \"$WERKT_STATE_PATH\"\nprintf '{}' > \"$WERKT_RESULT_PATH\"\n", expectedLimit)
			if err := os.WriteFile(filepath.Join(directory, "run.sh"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			output := stateOfSize(tc.output)
			if err := os.WriteFile(filepath.Join(directory, "output.json"), output, 0600); err != nil {
				t.Fatal(err)
			}
			run := domain.RunnableRun{
				Run:          domain.Run{ID: "run_limit", AutomationID: "stateful", RevisionID: "rev_limit"},
				ArtifactPath: directory,
				Manifest: domain.Manifest{
					Runtime:   domain.Runtime{Language: "shell", Command: []string{"./run.sh"}, Environment: map[string]string{"WERKT_STATE_MAX_BYTES": "1"}},
					Execution: domain.Execution{Timeout: "5s", Concurrency: "forbid", State: domain.StatePolicy{Enabled: true}},
				},
				State: stateOfSize(tc.input),
			}
			result, err := NewProcessRunnerWithStateLimit(tc.limit).Execute(context.Background(), run)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %s", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(result.State) != string(output) {
				t.Fatal("state did not round trip")
			}
		})
	}
}
