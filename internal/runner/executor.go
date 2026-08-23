package runner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

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
	State  json.RawMessage
}

const MaxAutomationStateBytes = 64 * 1024

func validateAutomationState(value []byte) (json.RawMessage, error) {
	if len(value) > MaxAutomationStateBytes {
		return nil, fmt.Errorf("automation state exceeds %d bytes", MaxAutomationStateBytes)
	}
	var object map[string]any
	if err := json.Unmarshal(value, &object); err != nil || object == nil {
		return nil, errors.New("automation state must be a JSON object")
	}
	return json.RawMessage(value), nil
}

var ErrSecretResolverUnavailable = errors.New("secret resolver is not configured")

type SecretResolver interface {
	Resolve(context.Context, []string) (map[string]string, error)
}

type resolvedEnvironment struct {
	values  map[string]string
	secrets []string
}

func resolveRuntimeEnvironment(ctx context.Context, resolver SecretResolver, runtime domain.Runtime) (resolvedEnvironment, error) {
	values := make(map[string]string, len(runtime.Environment)+len(runtime.Secrets))
	for key, value := range runtime.Environment {
		values[key] = value
	}
	if len(runtime.Secrets) == 0 {
		return resolvedEnvironment{values: values}, nil
	}
	if resolver == nil {
		return resolvedEnvironment{}, ErrSecretResolverUnavailable
	}
	targets := make([]string, 0, len(runtime.Secrets))
	namesSet := make(map[string]struct{}, len(runtime.Secrets))
	for target := range runtime.Secrets {
		targets = append(targets, target)
		namesSet[runtime.Secrets[target]] = struct{}{}
	}
	sort.Strings(targets)
	names := make([]string, 0, len(namesSet))
	for name := range namesSet {
		names = append(names, name)
	}
	sort.Strings(names)
	resolved, err := resolver.Resolve(ctx, names)
	if err != nil {
		return resolvedEnvironment{}, fmt.Errorf("resolve runtime secrets: %w", err)
	}
	secretValues := make([]string, 0, len(names))
	for _, name := range names {
		value, ok := resolved[name]
		if !ok || value == "" {
			return resolvedEnvironment{}, fmt.Errorf("runtime secret %q was not resolved", name)
		}
		secretValues = append(secretValues, value)
	}
	for _, target := range targets {
		source := runtime.Secrets[target]
		value, exists := resolved[source]
		if !exists || value == "" {
			return resolvedEnvironment{}, fmt.Errorf("runtime secret %q references unavailable secret %q", target, source)
		}
		values[target] = value
	}
	return resolvedEnvironment{values: values, secrets: secretValues}, nil
}

type logRedactor struct {
	replacer *strings.Replacer
}

func newLogRedactor(values []string) logRedactor {
	patterns := make(map[string]struct{}, len(values)*5)
	for _, value := range values {
		if value == "" {
			continue
		}
		patterns[value] = struct{}{}
		patterns[url.QueryEscape(value)] = struct{}{}
		patterns[base64.StdEncoding.EncodeToString([]byte(value))] = struct{}{}
		patterns[base64.RawStdEncoding.EncodeToString([]byte(value))] = struct{}{}
		quoted := strconv.Quote(value)
		if len(quoted) >= 2 {
			patterns[quoted[1:len(quoted)-1]] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(patterns))
	for pattern := range patterns {
		if pattern != "" {
			ordered = append(ordered, pattern)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if len(ordered[i]) == len(ordered[j]) {
			return ordered[i] < ordered[j]
		}
		return len(ordered[i]) > len(ordered[j])
	})
	replacements := make([]string, 0, len(ordered)*2)
	for _, pattern := range ordered {
		replacements = append(replacements, pattern, "[REDACTED]")
	}
	if len(replacements) == 0 {
		return logRedactor{}
	}
	return logRedactor{replacer: strings.NewReplacer(replacements...)}
}

func (r logRedactor) Redact(value string) string {
	if r.replacer == nil {
		return value
	}
	return r.replacer.Replace(value)
}

func (r logRedactor) Error(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(r.Redact(err.Error()))
}
