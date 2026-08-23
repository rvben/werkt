package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rvben/werkt/internal/domain"
	"gopkg.in/yaml.v3"
)

const Filename = "automation.yaml"

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var secretName = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._/-][a-z0-9]+)*$`)
var headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

const maxEgressRules = 32

func Load(directory string) (domain.Manifest, error) {
	file, err := os.Open(filepath.Join(directory, Filename))
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("open manifest: %w", err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var value domain.Manifest
	if err := decoder.Decode(&value); err != nil {
		return domain.Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := Validate(value); err != nil {
		return domain.Manifest{}, err
	}
	return value, nil
}

func Validate(value domain.Manifest) error {
	var problems []string
	if value.APIVersion != "werkt.dev/v1" {
		problems = append(problems, "apiVersion must be werkt.dev/v1")
	}
	if value.Kind != "Automation" {
		problems = append(problems, "kind must be Automation")
	}
	if !identifier.MatchString(value.Metadata.Name) {
		problems = append(problems, "metadata.name must start with a letter and contain only lowercase letters, digits, or hyphens")
	}
	if value.Metadata.Project == "" {
		problems = append(problems, "metadata.project is required")
	}
	if len(value.Triggers) == 0 {
		problems = append(problems, "at least one trigger is required")
	}

	seen := make(map[string]struct{}, len(value.Triggers))
	for index, trigger := range value.Triggers {
		path := fmt.Sprintf("triggers[%d]", index)
		if !identifier.MatchString(trigger.ID) {
			problems = append(problems, path+".id is invalid")
		}
		if _, exists := seen[trigger.ID]; exists {
			problems = append(problems, path+".id must be unique")
		}
		seen[trigger.ID] = struct{}{}
		switch trigger.Type {
		case "schedule":
			problems = append(problems, unexpectedTriggerConfig(path, trigger.Config, "cron", "timezone")...)
			expression, _ := trigger.Config["cron"].(string)
			if expression == "" {
				problems = append(problems, path+".config.cron is required")
			} else if _, err := cron.ParseStandard(expression); err != nil {
				problems = append(problems, path+".config.cron is invalid: "+err.Error())
			}
			if raw, exists := trigger.Config["timezone"]; exists {
				timezone, valid := raw.(string)
				if !valid {
					problems = append(problems, path+".config.timezone must be a string")
					break
				}
				if _, err := time.LoadLocation(timezone); err != nil {
					problems = append(problems, path+".config.timezone is invalid")
				}
			}
		case "webhook":
			provider, providerIsString := trigger.Config["provider"].(string)
			if _, exists := trigger.Config["provider"]; exists && !providerIsString {
				problems = append(problems, path+".config.provider must be a string")
			}
			if provider != "" && provider != "werkt" && provider != "github" {
				problems = append(problems, path+".config.provider must be werkt or github")
			}
			allowed := []string{"secret", "provider"}
			if provider == "" || provider == "werkt" {
				allowed = append(allowed, "signatureHeader")
			}
			problems = append(problems, unexpectedTriggerConfig(path, trigger.Config, allowed...)...)
			secret, _ := trigger.Config["secret"].(string)
			if !validSecretReference(secret) {
				problems = append(problems, path+".config.secret must name a Werkt secret")
			}
			if raw, exists := trigger.Config["signatureHeader"]; exists && provider != "github" {
				signatureHeader, valid := raw.(string)
				if !valid || signatureHeader == "" || !headerName.MatchString(signatureHeader) {
					problems = append(problems, path+".config.signatureHeader is invalid")
				}
			}
		case "email":
			problems = append(problems, unexpectedTriggerConfig(path, trigger.Config, "tokenSecret")...)
			tokenSecret, _ := trigger.Config["tokenSecret"].(string)
			if !validSecretReference(tokenSecret) {
				problems = append(problems, path+".config.tokenSecret must name a Werkt secret")
			}
		case "ntfy":
			problems = append(problems, unexpectedTriggerConfig(path, trigger.Config, "server", "topic", "tokenSecret")...)
			server, _ := trigger.Config["server"].(string)
			topic, _ := trigger.Config["topic"].(string)
			if server == "" {
				problems = append(problems, path+".config.server is required")
			}
			if topic == "" {
				problems = append(problems, path+".config.topic is required")
			}
			if tokenSecret, _ := trigger.Config["tokenSecret"].(string); tokenSecret != "" && !validSecretReference(tokenSecret) {
				problems = append(problems, path+".config.tokenSecret must name a Werkt secret")
			}
		default:
			problems = append(problems, path+".type must be schedule, webhook, email, or ntfy")
		}
	}

	if value.Runtime.Language == "" {
		problems = append(problems, "runtime.language is required")
	}
	if len(value.Runtime.Command) == 0 || strings.TrimSpace(value.Runtime.Command[0]) == "" {
		problems = append(problems, "runtime.command must contain an executable")
	}
	if len(value.Runtime.Build) > 0 && strings.TrimSpace(value.Runtime.Build[0]) == "" {
		problems = append(problems, "runtime.build must start with an executable")
	}
	if len(value.Runtime.Egress) > maxEgressRules {
		problems = append(problems, fmt.Sprintf("runtime.egress accepts at most %d rules", maxEgressRules))
	}
	for index, rule := range value.Runtime.Egress {
		path := fmt.Sprintf("runtime.egress[%d]", index)
		if problem := validateEgressHost(rule.Host); problem != "" {
			problems = append(problems, path+".host "+problem)
		}
		if rule.Port == 0 {
			problems = append(problems, path+".port must be between 1 and 65535")
		}
		if rule.Protocol != "" && rule.Protocol != "tcp" && rule.Protocol != "udp" {
			problems = append(problems, path+".protocol must be tcp or udp")
		}
	}
	checkIDs := make(map[string]struct{}, len(value.Deployment.Checks))
	for index, check := range value.Deployment.Checks {
		path := fmt.Sprintf("deployment.checks[%d]", index)
		if !identifier.MatchString(check.ID) {
			problems = append(problems, path+".id is invalid")
		}
		if _, exists := checkIDs[check.ID]; exists {
			problems = append(problems, path+".id must be unique")
		}
		checkIDs[check.ID] = struct{}{}
		if len(check.Command) == 0 || strings.TrimSpace(check.Command[0]) == "" {
			problems = append(problems, path+".command must contain an executable")
		}
		if check.Timeout != "" {
			if duration, err := time.ParseDuration(check.Timeout); err != nil || duration <= 0 {
				problems = append(problems, path+".timeout must be a positive duration")
			}
		}
	}
	for key := range value.Runtime.Environment {
		if !environmentName.MatchString(key) {
			problems = append(problems, "runtime.environment contains invalid variable name "+key)
		}
		if strings.HasPrefix(key, "WERKT_") {
			problems = append(problems, "runtime.environment cannot override reserved WERKT_ variables")
		}
	}
	for target, source := range value.Runtime.Secrets {
		if !environmentName.MatchString(target) {
			problems = append(problems, "runtime.secrets contains invalid target variable name "+target)
		}
		if strings.HasPrefix(target, "WERKT_") {
			problems = append(problems, "runtime.secrets cannot override reserved WERKT_ variables")
		}
		if !validSecretReference(source) {
			problems = append(problems, "runtime.secrets source for "+target+" must name a Werkt secret")
		}
		if _, exists := value.Runtime.Environment[target]; exists {
			problems = append(problems, "runtime.secrets target "+target+" duplicates runtime.environment")
		}
	}
	if value.Execution.Timeout != "" {
		if duration, err := time.ParseDuration(value.Execution.Timeout); err != nil || duration <= 0 {
			problems = append(problems, "execution.timeout must be a positive duration")
		}
	}
	if value.Execution.Retries < 0 {
		problems = append(problems, "execution.retries cannot be negative")
	}
	if value.Execution.Concurrency != "" && value.Execution.Concurrency != "allow" && value.Execution.Concurrency != "forbid" {
		problems = append(problems, "execution.concurrency must be allow or forbid")
	}
	if value.Execution.State.Enabled && value.Execution.Concurrency != "forbid" {
		problems = append(problems, "execution.state.enabled requires execution.concurrency: forbid")
	}

	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func validateEgressHost(value string) string {
	host := strings.TrimSuffix(value, ".")
	if host == "" {
		return "is required"
	}
	if value != strings.TrimSpace(value) || len(host) > 253 {
		return "must be a hostname or IPv4 address of at most 253 bytes"
	}
	if strings.Contains(host, "://") || strings.ContainsAny(host, "/*") {
		return "must be a hostname or IPv4 address without a scheme, path, or wildcard"
	}
	for _, character := range host {
		if character > 127 || character < 33 || character == 127 {
			return "must contain only printable ASCII hostname characters"
		}
	}
	if address := net.ParseIP(host); address != nil {
		ipv4 := address.To4()
		if ipv4 == nil {
			return "must be IPv4; IPv6 egress is not supported"
		}
		if ipv4.IsUnspecified() || ipv4.IsMulticast() || ipv4.Equal(net.IPv4bcast) {
			return "must be a unicast IPv4 address"
		}
		return ""
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "is not a valid DNS hostname"
		}
		for _, character := range label {
			if !((character >= 'a' && character <= 'z') ||
				(character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') || character == '-') {
				return "is not a valid DNS hostname"
			}
		}
	}
	return ""
}

func unexpectedTriggerConfig(path string, config map[string]any, allowed ...string) []string {
	allowedKeys := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedKeys[key] = struct{}{}
	}
	var problems []string
	for key := range config {
		if _, exists := allowedKeys[key]; !exists {
			problems = append(problems, path+".config."+key+" is not supported")
		}
	}
	sort.Strings(problems)
	return problems
}

func validSecretReference(value string) bool {
	return len(value) <= 128 && secretName.MatchString(value)
}

func CanonicalJSON(value domain.Manifest) ([]byte, error) {
	return json.Marshal(value)
}

func HashDirectory(directory string) (string, error) {
	var files []string
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		if entry.IsDir() && relative != "." && ignoredDirectory(entry.Name()) {
			return filepath.SkipDir
		}
		if !entry.IsDir() {
			files = append(files, relative)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk automation directory: %w", err)
	}
	sort.Strings(files)

	hash := sha256.New()
	for _, relative := range files {
		if _, err := io.WriteString(hash, relative+"\x00"); err != nil {
			return "", err
		}
		file, err := os.Open(filepath.Join(directory, relative))
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		if _, err := io.WriteString(hash, "\x00"); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func ignoredDirectory(name string) bool {
	switch name {
	case ".git", "target", "__pycache__", ".pytest_cache", ".venv", "node_modules":
		return true
	default:
		return false
	}
}
