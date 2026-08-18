package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/rvben/werkt/internal/domain"
)

// Executor is the isolation boundary between Werkt's orchestration plane and
// the environment that runs automation code.
type Executor interface {
	Execute(context.Context, domain.RunnableRun) (Result, error)
}

type Result struct {
	Output json.RawMessage
	Logs   string
}

func resolveRuntimeEnvironment(runtime domain.Runtime) (map[string]string, error) {
	values := make(map[string]string, len(runtime.Environment)+len(runtime.Secrets))
	for key, value := range runtime.Environment {
		values[key] = value
	}
	targets := make([]string, 0, len(runtime.Secrets))
	for target := range runtime.Secrets {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	for _, target := range targets {
		source := runtime.Secrets[target]
		value, exists := os.LookupEnv(source)
		if !exists || value == "" {
			return nil, fmt.Errorf("runtime secret %q references unset or empty environment variable %q", target, source)
		}
		values[target] = value
	}
	return values, nil
}
