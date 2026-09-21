package authoring

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rvben/werkt/internal/domain"
	"github.com/rvben/werkt/internal/manifest"
	"gopkg.in/yaml.v3"
)

type PackageOptions struct {
	OutputDirectory string
	Project         string
	Folder          string
	Model           string
	IntentDigest    string
}

type PackageResult struct {
	Automation   string   `json:"automation"`
	Output       string   `json:"output"`
	ContentHash  string   `json:"contentHash"`
	PlanFile     string   `json:"planFile"`
	Warnings     []string `json:"warnings"`
	NextCommands []string `json:"nextCommands"`
}

type planEnvelope struct {
	Version      int    `json:"version"`
	Generator    string `json:"generator"`
	Model        string `json:"model"`
	IntentDigest string `json:"intentSha256"`
	Draft        Draft  `json:"draft"`
}

func WritePackage(draft Draft, options PackageOptions) (PackageResult, error) {
	if err := draft.Validate(draft.Name); err != nil {
		return PackageResult{}, err
	}
	if strings.TrimSpace(options.Project) == "" {
		return PackageResult{}, errors.New("project is required")
	}
	if strings.TrimSpace(options.OutputDirectory) == "" {
		return PackageResult{}, errors.New("output directory is required")
	}
	output, err := filepath.Abs(options.OutputDirectory)
	if err != nil {
		return PackageResult{}, err
	}
	if _, err := os.Lstat(output); err == nil {
		return PackageResult{}, fmt.Errorf("output path already exists: %s", output)
	} else if !errors.Is(err, os.ErrNotExist) {
		return PackageResult{}, err
	}
	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return PackageResult{}, fmt.Errorf("create output parent: %w", err)
	}
	staging, err := os.MkdirTemp(parent, ".werkt-draft-")
	if err != nil {
		return PackageResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staging)
		}
	}()

	value := buildManifest(draft, options.Project, options.Folder)
	if err := manifest.Validate(value); err != nil {
		return PackageResult{}, fmt.Errorf("validate generated manifest: %w", err)
	}
	manifestBody, err := yaml.Marshal(value)
	if err != nil {
		return PackageResult{}, err
	}
	if err := writeNewFile(staging, manifest.Filename, manifestBody); err != nil {
		return PackageResult{}, err
	}
	for _, file := range draft.Files {
		body := []byte(file.Content)
		if !strings.HasSuffix(file.Content, "\n") {
			body = append(body, '\n')
		}
		if err := writeNewFile(staging, file.Path, body); err != nil {
			return PackageResult{}, err
		}
	}
	planBody, err := json.MarshalIndent(planEnvelope{Version: 1, Generator: "werkt draft (TypeSafe Jev)", Model: options.Model, IntentDigest: options.IntentDigest, Draft: draft}, "", "  ")
	if err != nil {
		return PackageResult{}, err
	}
	planBody = append(planBody, '\n')
	if err := writeNewFile(staging, "werkt.plan.json", planBody); err != nil {
		return PackageResult{}, err
	}
	if err := writeNewFile(staging, "README.md", []byte(packageReadme(draft))); err != nil {
		return PackageResult{}, err
	}
	if _, err := manifest.Load(staging); err != nil {
		return PackageResult{}, fmt.Errorf("reload generated package: %w", err)
	}
	contentHash, err := manifest.HashDirectory(staging)
	if err != nil {
		return PackageResult{}, err
	}
	if err := os.Rename(staging, output); err != nil {
		return PackageResult{}, fmt.Errorf("publish generated package: %w", err)
	}
	committed = true
	return PackageResult{
		Automation: draft.Name, Output: output, ContentHash: contentHash,
		PlanFile: filepath.Join(output, "werkt.plan.json"), Warnings: draft.Warnings,
		NextCommands: []string{
			fmt.Sprintf("werkt validate %s", shellQuote(output)),
			fmt.Sprintf("python3 -m unittest discover -s %s -p 'test_*.py'", shellQuote(output)),
			fmt.Sprintf("werkt deploy %s", shellQuote(output)),
		},
	}, nil
}

func buildManifest(draft Draft, project, folder string) domain.Manifest {
	config := make(map[string]any)
	switch draft.Trigger.Type {
	case "schedule":
		config["cron"], config["timezone"] = draft.Trigger.Cron, draft.Trigger.Timezone
	case "webhook":
		config["secret"] = draft.Trigger.Secret
		if draft.Trigger.Provider != "" {
			config["provider"] = draft.Trigger.Provider
		}
		if draft.Trigger.SignatureHeader != "" && (draft.Trigger.Provider == "" || draft.Trigger.Provider == "werkt") {
			config["signatureHeader"] = draft.Trigger.SignatureHeader
		}
	case "email":
		config["tokenSecret"] = draft.Trigger.TokenSecret
	case "ntfy":
		config["server"], config["topic"] = draft.Trigger.Server, draft.Trigger.Topic
		if draft.Trigger.TokenSecret != "" {
			config["tokenSecret"] = draft.Trigger.TokenSecret
		}
	}
	secrets := make(map[string]string, len(draft.Secrets))
	for _, binding := range draft.Secrets {
		secrets[binding.Environment] = binding.Secret
	}
	egress := make([]domain.EgressRule, 0, len(draft.Egress))
	for _, rule := range draft.Egress {
		egress = append(egress, domain.EgressRule{Host: rule.Host, Port: uint16(rule.Port)})
	}
	sort.Slice(egress, func(i, j int) bool {
		if egress[i].Host == egress[j].Host {
			return egress[i].Port < egress[j].Port
		}
		return egress[i].Host < egress[j].Host
	})
	return domain.Manifest{
		APIVersion: "werkt.dev/v1", Kind: "Automation",
		Metadata: domain.Metadata{Name: draft.Name, Project: project, Folder: folder, Description: draft.Description, Labels: []string{"generated", "review-required"}},
		Triggers: []domain.Trigger{{ID: draft.Trigger.ID, Type: draft.Trigger.Type, Config: config}},
		Runtime:  domain.Runtime{Language: "python", Tools: map[string]string{"python": "3.13.7"}, Command: []string{"python3", "main.py"}, Secrets: secrets, Egress: egress},
		Deployment: domain.DeploymentPolicy{Checks: []domain.DeploymentCheck{
			{ID: "syntax", Command: []string{"python3", "-m", "py_compile", "main.py"}, Timeout: "30s"},
			{ID: "tests", Command: []string{"python3", "-m", "unittest", "discover", "-s", ".", "-p", "test_*.py"}, Timeout: "2m"},
		}},
		Execution: domain.Execution{Timeout: "5m", Retries: 2, Concurrency: "forbid", State: domain.StatePolicy{Enabled: hasApprovalStep(draft.Steps)}},
	}
}

func hasApprovalStep(steps []Step) bool {
	for _, step := range steps {
		if step.Effect == "approval" {
			return true
		}
	}
	return false
}

func writeNewFile(root, relative string, body []byte) error {
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(path, body, 0o640); err != nil {
		return fmt.Errorf("write %s: %w", relative, err)
	}
	return nil
}

func packageReadme(draft Draft) string {
	var value strings.Builder
	fmt.Fprintf(&value, "# %s\n\n%s\n\n", draft.Name, draft.Description)
	fmt.Fprintf(&value, "This package was compiled from typed Jev decisions as a review-required draft (model `%s`, routing confidence %.2f). Inspect the source, plan, permissions, secret bindings, and external side effects before deployment.\n\n", draft.Decision.Model, draft.Decision.Confidence)
	value.WriteString("## Plan\n\n")
	for _, step := range draft.Steps {
		dependencies := "starts the workflow"
		if len(step.DependsOn) > 0 {
			dependencies = "after " + strings.Join(step.DependsOn, ", ")
		}
		fmt.Fprintf(&value, "- `%s` — %s (`%s.%s`, %s; %s)\n", step.ID, step.Title, step.Connector, step.Operation, step.Effect, dependencies)
	}
	if len(draft.Secrets) > 0 {
		value.WriteString("\n## Required secrets\n\n")
		for _, secret := range draft.Secrets {
			fmt.Fprintf(&value, "- `%s` → `%s`: %s\n", secret.Environment, secret.Secret, secret.Description)
		}
	}
	if len(draft.Warnings) > 0 {
		value.WriteString("\n## Review warnings\n\n")
		for _, warning := range draft.Warnings {
			fmt.Fprintf(&value, "- %s\n", warning)
		}
	}
	value.WriteString("\n## Verify\n\n```sh\nwerkt validate .\npython3 -m unittest discover -s . -p 'test_*.py'\n```\n")
	return value.String()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
