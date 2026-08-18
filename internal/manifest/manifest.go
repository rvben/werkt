package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
			expression, _ := trigger.Config["cron"].(string)
			if expression == "" {
				problems = append(problems, path+".config.cron is required")
			} else if _, err := cron.ParseStandard(expression); err != nil {
				problems = append(problems, path+".config.cron is invalid: "+err.Error())
			}
			if timezone, _ := trigger.Config["timezone"].(string); timezone != "" {
				if _, err := time.LoadLocation(timezone); err != nil {
					problems = append(problems, path+".config.timezone is invalid")
				}
			}
		case "webhook", "email":
		case "ntfy":
			server, _ := trigger.Config["server"].(string)
			topic, _ := trigger.Config["topic"].(string)
			if server == "" {
				problems = append(problems, path+".config.server is required")
			}
			if topic == "" {
				problems = append(problems, path+".config.topic is required")
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
	for key := range value.Runtime.Environment {
		if strings.HasPrefix(key, "WERKT_") {
			problems = append(problems, "runtime.environment cannot override reserved WERKT_ variables")
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

	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
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
