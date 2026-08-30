package runner

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rvben/werkt/internal/domain"
	pythonsdk "github.com/rvben/werkt/sdk/python"
)

// Executor is the isolation boundary between Werkt's orchestration plane and
// the environment that runs automation code.
type Executor interface {
	Execute(context.Context, domain.RunnableRun) (Result, error)
}

type ArtifactVerifier interface {
	Verify(string, domain.ArtifactProvenance) error
}

type verifyingExecutor struct {
	next     Executor
	verifier ArtifactVerifier
}

func NewVerifyingExecutor(next Executor, verifier ArtifactVerifier) Executor {
	return &verifyingExecutor{next: next, verifier: verifier}
}

func (e *verifyingExecutor) Execute(ctx context.Context, run domain.RunnableRun) (Result, error) {
	if e.next == nil || e.verifier == nil {
		return Result{}, errors.New("artifact verification is not configured")
	}
	if err := e.verifier.Verify(run.ArtifactPath, run.Provenance); err != nil {
		return Result{}, fmt.Errorf("verify automation artifact: %w", err)
	}
	return e.next.Execute(ctx, run)
}

type Result struct {
	Output  json.RawMessage
	Logs    string
	State   json.RawMessage
	Control domain.RunControl
}

const MaxAutomationStateBytes = 64 * 1024
const MaxRunControlBytes = 64 * 1024
const maxContinuationDelay = 30 * 24 * time.Hour

var controlIdentifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)

func validateRunControl(value []byte, now time.Time) (domain.RunControl, error) {
	if len(value) > MaxRunControlBytes {
		return domain.RunControl{}, fmt.Errorf("run control exceeds %d bytes", MaxRunControlBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	var control domain.RunControl
	if err := decoder.Decode(&control); err != nil {
		return domain.RunControl{}, fmt.Errorf("run control must be a valid JSON object: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return domain.RunControl{}, errors.New("run control must contain one JSON object")
	}
	if control.Defer != nil {
		if !controlIdentifier.MatchString(control.Defer.Key) {
			return domain.RunControl{}, errors.New("defer.key is invalid")
		}
		if control.Defer.Until.Before(now) || control.Defer.Until.After(now.Add(maxContinuationDelay)) {
			return domain.RunControl{}, errors.New("defer.until must be in the next 30 days")
		}
		if len(control.Defer.Data) == 0 {
			control.Defer.Data = json.RawMessage(`{}`)
		} else if !json.Valid(control.Defer.Data) {
			return domain.RunControl{}, errors.New("defer.data must be valid JSON")
		}
	}
	if control.Approval != nil {
		if err := validateApprovalRequest(*control.Approval, now); err != nil {
			return domain.RunControl{}, err
		}
	}
	return control, nil
}

func validateApprovalRequest(value domain.ApprovalRequest, now time.Time) error {
	if !controlIdentifier.MatchString(value.Key) {
		return errors.New("approval.key is invalid")
	}
	if title := strings.TrimSpace(value.Title); title == "" || len([]rune(title)) > 120 {
		return errors.New("approval.title must contain at most 120 characters")
	}
	if len([]rune(value.Description)) > 1000 {
		return errors.New("approval.description must contain at most 1000 characters")
	}
	if value.ExpiresAt.Before(now) || value.ExpiresAt.After(now.Add(maxContinuationDelay)) {
		return errors.New("approval.expiresAt must be in the next 30 days")
	}
	if len(value.Fields) > 16 || len(value.Actions) == 0 || len(value.Actions) > 4 {
		return errors.New("approval accepts at most 16 fields and requires one to four actions")
	}
	fields := make(map[string]struct{}, len(value.Fields))
	for _, field := range value.Fields {
		if !controlIdentifier.MatchString(field.ID) {
			return errors.New("approval field id is invalid")
		}
		if _, exists := fields[field.ID]; exists {
			return errors.New("approval field ids must be unique")
		}
		fields[field.ID] = struct{}{}
		if strings.TrimSpace(field.Label) == "" || len([]rune(field.Label)) > 80 || len([]rune(field.Description)) > 300 {
			return errors.New("approval field label or description is invalid")
		}
		switch field.Type {
		case "text", "textarea", "number", "boolean":
			if len(field.Options) > 0 {
				return errors.New("approval field options require type select")
			}
		case "select":
			if len(field.Options) == 0 || len(field.Options) > 32 {
				return errors.New("approval select fields require one to 32 options")
			}
			seenOptions := make(map[string]struct{}, len(field.Options))
			for _, option := range field.Options {
				if strings.TrimSpace(option) == "" || len([]rune(option)) > 100 {
					return errors.New("approval select options must contain at most 100 characters")
				}
				if _, exists := seenOptions[option]; exists {
					return errors.New("approval select options must be unique")
				}
				seenOptions[option] = struct{}{}
			}
		default:
			return errors.New("approval field type must be text, textarea, number, boolean, or select")
		}
		if field.Value != nil {
			switch field.Type {
			case "text", "textarea":
				text, ok := field.Value.(string)
				if !ok || len([]rune(text)) > 4000 {
					return errors.New("approval text defaults must contain at most 4000 characters")
				}
			case "number":
				if _, ok := field.Value.(float64); !ok {
					return errors.New("approval number defaults must be numbers")
				}
			case "boolean":
				if _, ok := field.Value.(bool); !ok {
					return errors.New("approval boolean defaults must be booleans")
				}
			case "select":
				selected, ok := field.Value.(string)
				if !ok || !contains(field.Options, selected) {
					return errors.New("approval select defaults must use a declared option")
				}
			}
		}
	}
	actions := make(map[string]struct{}, len(value.Actions))
	for _, action := range value.Actions {
		if !controlIdentifier.MatchString(action.ID) || strings.TrimSpace(action.Label) == "" || len([]rune(action.Label)) > 80 {
			return errors.New("approval action id or label is invalid")
		}
		if _, exists := actions[action.ID]; exists {
			return errors.New("approval action ids must be unique")
		}
		actions[action.ID] = struct{}{}
		if action.ID != "approve" && action.ID != "reject" {
			return errors.New("approval action id must be approve or reject")
		}
		if action.Style != "" && action.Style != "primary" && action.Style != "neutral" && action.Style != "danger" {
			return errors.New("approval action style must be primary, neutral, or danger")
		}
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

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
	values := promotionEnvironment(runtime)
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

func promotionEnvironment(runtime domain.Runtime) map[string]string {
	values := make(map[string]string, len(runtime.Environment)+1)
	for key, value := range runtime.Environment {
		values[key] = value
	}
	if runtime.Language == "python" {
		values["PYTHONPATH"] = pythonsdk.ArtifactDirectory
	}
	return values
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
