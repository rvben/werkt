package authoring

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	MaxIntentBytes = 16 << 10
	MaxFiles       = 16
	MaxFileBytes   = 128 << 10
	MaxTotalBytes  = 512 << 10
)

var (
	identifier      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	secretName      = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._/-][a-z0-9]+)*$`)
)

// Draft is an untrusted model proposal. Validate must succeed before any of
// its files or configuration may cross into a generated automation package.
type Draft struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Trigger     Trigger         `json:"trigger"`
	Steps       []Step          `json:"steps"`
	Secrets     []SecretBinding `json:"secrets"`
	Egress      []EgressRule    `json:"egress"`
	Files       []File          `json:"files"`
	Warnings    []string        `json:"warnings"`
	Decision    DecisionSummary `json:"decision"`
}

type DecisionSummary struct {
	Provider            string              `json:"provider"`
	Model               string              `json:"model"`
	Confidence          float64             `json:"confidence"`
	Trigger             string              `json:"trigger"`
	TriggerConfidence   float64             `json:"triggerConfidence"`
	RiskScore           float64             `json:"riskScore"`
	ApprovalProbability float64             `json:"approvalProbability"`
	Operations          []OperationDecision `json:"operations"`
	InputTokens         int                 `json:"inputTokens"`
	OutputTokens        int                 `json:"outputTokens"`
}

type OperationDecision struct {
	ID                   string  `json:"id"`
	Selected             bool    `json:"selected"`
	RelevanceProbability float64 `json:"relevanceProbability"`
	PrimaryProbability   float64 `json:"primaryProbability"`
}

type Trigger struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	Cron            string `json:"cron"`
	Timezone        string `json:"timezone"`
	Secret          string `json:"secret"`
	Provider        string `json:"provider"`
	SignatureHeader string `json:"signature_header"`
	Server          string `json:"server"`
	Topic           string `json:"topic"`
	TokenSecret     string `json:"token_secret"`
}

type Step struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Connector string   `json:"connector"`
	Operation string   `json:"operation"`
	Effect    string   `json:"effect"`
	DependsOn []string `json:"depends_on"`
	Inputs    []string `json:"inputs"`
	Outputs   []string `json:"outputs"`
}

type SecretBinding struct {
	Environment string `json:"environment"`
	Secret      string `json:"secret"`
	Description string `json:"description"`
}

type EgressRule struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (d Draft) Validate(expectedName string) error {
	var problems []string
	if !identifier.MatchString(d.Name) {
		problems = append(problems, "name must start with a letter and contain only lowercase letters, digits, or hyphens")
	}
	if expectedName != "" && d.Name != expectedName {
		problems = append(problems, fmt.Sprintf("name is %q, expected %q", d.Name, expectedName))
	}
	if strings.TrimSpace(d.Description) == "" || len(d.Description) > 500 {
		problems = append(problems, "description must contain 1-500 bytes")
	}
	problems = append(problems, d.Trigger.problems()...)
	problems = append(problems, validateSteps(d.Steps)...)
	problems = append(problems, validateSecrets(d.Secrets)...)
	problems = append(problems, validateEgress(d.Egress)...)
	problems = append(problems, validateFiles(d.Files)...)
	if d.Decision.Provider != "typesafe" || strings.TrimSpace(d.Decision.Model) == "" || !unit(d.Decision.Confidence) || !unit(d.Decision.TriggerConfidence) || !unit(d.Decision.ApprovalProbability) || d.Decision.RiskScore < 0 || d.Decision.RiskScore > 3 {
		problems = append(problems, "decision metadata is invalid")
	}
	for index, operation := range d.Decision.Operations {
		if _, ok := operationByID(operation.ID); !ok || !unit(operation.RelevanceProbability) || !unit(operation.PrimaryProbability) {
			problems = append(problems, fmt.Sprintf("decision.operations[%d] is invalid", index))
		}
	}
	for index, warning := range d.Warnings {
		if strings.TrimSpace(warning) == "" || len(warning) > 500 {
			problems = append(problems, fmt.Sprintf("warnings[%d] must contain 1-500 bytes", index))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func (t Trigger) problems() []string {
	var problems []string
	if !identifier.MatchString(t.ID) {
		problems = append(problems, "trigger.id is invalid")
	}
	switch t.Type {
	case "schedule":
		if strings.TrimSpace(t.Cron) == "" {
			problems = append(problems, "schedule trigger requires cron")
		}
		if strings.TrimSpace(t.Timezone) == "" {
			problems = append(problems, "schedule trigger requires timezone")
		} else if _, err := time.LoadLocation(t.Timezone); err != nil {
			problems = append(problems, "schedule trigger requires a valid IANA timezone")
		}
	case "webhook":
		if !secretName.MatchString(t.Secret) {
			problems = append(problems, "webhook trigger requires a valid secret reference")
		}
		if t.Provider != "" && t.Provider != "werkt" && t.Provider != "github" && t.Provider != "zoom" {
			problems = append(problems, "webhook provider must be werkt, github, or zoom")
		}
	case "email":
		if !secretName.MatchString(t.TokenSecret) {
			problems = append(problems, "email trigger requires a valid token_secret reference")
		}
	case "ntfy":
		if strings.TrimSpace(t.Server) == "" || strings.TrimSpace(t.Topic) == "" {
			problems = append(problems, "ntfy trigger requires server and topic")
		}
		if t.TokenSecret != "" && !secretName.MatchString(t.TokenSecret) {
			problems = append(problems, "ntfy token_secret is invalid")
		}
	default:
		problems = append(problems, "trigger.type must be schedule, webhook, email, or ntfy")
	}
	return problems
}

func validateSteps(steps []Step) []string {
	if len(steps) == 0 || len(steps) > 32 {
		return []string{"steps must contain 1-32 entries"}
	}
	var problems []string
	seen := make(map[string]struct{}, len(steps))
	positions := make(map[string]int, len(steps))
	dependencies := make(map[string][]string, len(steps))
	for index, step := range steps {
		path := fmt.Sprintf("steps[%d]", index)
		if !identifier.MatchString(step.ID) {
			problems = append(problems, path+".id is invalid")
		}
		if _, exists := seen[step.ID]; exists {
			problems = append(problems, path+".id is duplicated")
		}
		seen[step.ID] = struct{}{}
		positions[step.ID] = index
		dependencies[step.ID] = step.DependsOn
		if strings.TrimSpace(step.Title) == "" || strings.TrimSpace(step.Connector) == "" || strings.TrimSpace(step.Operation) == "" {
			problems = append(problems, path+" requires title, connector, and operation")
		}
		if step.Effect != "read" && step.Effect != "write" && step.Effect != "compute" && step.Effect != "approval" {
			problems = append(problems, path+".effect must be read, write, compute, or approval")
		}
	}
	for id, required := range dependencies {
		for _, dependency := range required {
			if _, exists := seen[dependency]; !exists {
				problems = append(problems, fmt.Sprintf("step %s depends on unknown step %s", id, dependency))
			} else if positions[dependency] >= positions[id] {
				problems = append(problems, fmt.Sprintf("step %s must appear after dependency %s", id, dependency))
			}
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return true
		}
		if visited[id] {
			return false
		}
		visiting[id] = true
		for _, dependency := range dependencies[id] {
			if visit(dependency) {
				return true
			}
		}
		visiting[id] = false
		visited[id] = true
		return false
	}
	for id := range dependencies {
		if visit(id) {
			problems = append(problems, "steps contain a dependency cycle")
			break
		}
	}
	return problems
}

func validateSecrets(values []SecretBinding) []string {
	var problems []string
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		path := fmt.Sprintf("secrets[%d]", index)
		if !environmentName.MatchString(value.Environment) || strings.HasPrefix(value.Environment, "WERKT_") {
			problems = append(problems, path+".environment is invalid or reserved")
		}
		if !secretName.MatchString(value.Secret) || len(value.Secret) > 128 {
			problems = append(problems, path+".secret is invalid")
		}
		if _, exists := seen[value.Environment]; exists {
			problems = append(problems, path+".environment is duplicated")
		}
		seen[value.Environment] = struct{}{}
	}
	return problems
}

func validateEgress(values []EgressRule) []string {
	var problems []string
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		path := fmt.Sprintf("egress[%d]", index)
		host := strings.TrimSuffix(value.Host, ".")
		if host == "" || len(host) > 253 || strings.ContainsAny(host, "/:@ ") || (net.ParseIP(host) == nil && !validHostname(host)) {
			problems = append(problems, path+".host must be an exact hostname or IP address")
		}
		if value.Port < 1 || value.Port > 65535 {
			problems = append(problems, path+".port must be between 1 and 65535")
		}
		key := strings.ToLower(host) + fmt.Sprintf(":%d", value.Port)
		if _, exists := seen[key]; exists {
			problems = append(problems, path+" duplicates an egress destination")
		}
		seen[key] = struct{}{}
	}
	return problems
}

func validHostname(value string) bool {
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func validateFiles(files []File) []string {
	if len(files) == 0 || len(files) > MaxFiles {
		return []string{fmt.Sprintf("files must contain 1-%d entries", MaxFiles)}
	}
	var problems []string
	seen := make(map[string]struct{}, len(files))
	hasMain, hasTest, total := false, false, 0
	for index, file := range files {
		path := fmt.Sprintf("files[%d]", index)
		clean := filepath.ToSlash(filepath.Clean(file.Path))
		if clean != file.Path || clean == "." || strings.HasPrefix(clean, "../") || filepath.IsAbs(file.Path) {
			problems = append(problems, path+".path is unsafe or not normalized")
		}
		if !strings.HasSuffix(file.Path, ".py") || strings.Contains(file.Path, "/") || strings.HasPrefix(file.Path, ".") {
			problems = append(problems, path+".path must be a root-level Python file")
		}
		if _, exists := seen[strings.ToLower(clean)]; exists {
			problems = append(problems, path+".path is duplicated")
		}
		seen[strings.ToLower(clean)] = struct{}{}
		size := len(file.Content)
		total += size
		if size == 0 || size > MaxFileBytes {
			problems = append(problems, fmt.Sprintf("%s.content must contain 1-%d bytes", path, MaxFileBytes))
		}
		hasMain = hasMain || file.Path == "main.py"
		hasTest = hasTest || (strings.HasPrefix(file.Path, "test_") && strings.HasSuffix(file.Path, ".py"))
	}
	if !hasMain {
		problems = append(problems, "files must include main.py")
	}
	if !hasTest {
		problems = append(problems, "files must include at least one test_*.py file")
	}
	if total > MaxTotalBytes {
		problems = append(problems, fmt.Sprintf("files exceed the %d-byte total limit", MaxTotalBytes))
	}
	return problems
}

func IntentDigest(intent string) string {
	digest := sha256.Sum256([]byte(intent))
	return hex.EncodeToString(digest[:])
}
