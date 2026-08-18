package domain

import (
	"encoding/json"
	"time"
)

const (
	RunQueued    = "queued"
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
)

type Manifest struct {
	APIVersion string    `yaml:"apiVersion" json:"apiVersion"`
	Kind       string    `yaml:"kind" json:"kind"`
	Metadata   Metadata  `yaml:"metadata" json:"metadata"`
	Triggers   []Trigger `yaml:"triggers" json:"triggers"`
	Runtime    Runtime   `yaml:"runtime" json:"runtime"`
	Execution  Execution `yaml:"execution" json:"execution"`
}

type Metadata struct {
	Name        string   `yaml:"name" json:"name"`
	Project     string   `yaml:"project" json:"project"`
	Folder      string   `yaml:"folder,omitempty" json:"folder,omitempty"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Labels      []string `yaml:"labels,omitempty" json:"labels,omitempty"`
}

type Trigger struct {
	ID      string         `yaml:"id" json:"id"`
	Type    string         `yaml:"type" json:"type"`
	Enabled *bool          `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Config  map[string]any `yaml:"config,omitempty" json:"config,omitempty"`
}

func (t Trigger) IsEnabled() bool {
	return t.Enabled == nil || *t.Enabled
}

type Runtime struct {
	Language    string            `yaml:"language" json:"language"`
	Image       string            `yaml:"image,omitempty" json:"image,omitempty"`
	BuildImage  string            `yaml:"buildImage,omitempty" json:"buildImage,omitempty"`
	Build       []string          `yaml:"build,omitempty" json:"build,omitempty"`
	Command     []string          `yaml:"command" json:"command"`
	Environment map[string]string `yaml:"environment,omitempty" json:"environment,omitempty"`
	Secrets     map[string]string `yaml:"secrets,omitempty" json:"secrets,omitempty"`
}

type Execution struct {
	Timeout     string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Retries     int    `yaml:"retries,omitempty" json:"retries,omitempty"`
	Concurrency string `yaml:"concurrency,omitempty" json:"concurrency,omitempty"`
}

func (e Execution) TimeoutDuration() time.Duration {
	if e.Timeout == "" {
		return 5 * time.Minute
	}
	duration, err := time.ParseDuration(e.Timeout)
	if err != nil {
		return 5 * time.Minute
	}
	return duration
}

type EventEnvelope struct {
	ID         string          `json:"id"`
	OccurredAt time.Time       `json:"occurredAt"`
	ReceivedAt time.Time       `json:"receivedAt"`
	Trigger    EventTrigger    `json:"trigger"`
	Data       json.RawMessage `json:"data"`
	Metadata   map[string]any  `json:"metadata,omitempty"`
}

type EventTrigger struct {
	Automation string `json:"automation"`
	ID         string `json:"id"`
	Type       string `json:"type"`
}

type TriggerDefinition struct {
	AutomationID string
	RevisionID   string
	TriggerID    string
	Type         string
	Config       json.RawMessage
	NextFireAt   *time.Time
}

type Run struct {
	ID           string          `json:"id"`
	AutomationID string          `json:"automationId"`
	RevisionID   string          `json:"revisionId"`
	EventID      string          `json:"eventId"`
	Status       string          `json:"status"`
	Attempt      int             `json:"attempt"`
	MaxAttempts  int             `json:"maxAttempts"`
	CreatedAt    time.Time       `json:"createdAt"`
	StartedAt    *time.Time      `json:"startedAt,omitempty"`
	FinishedAt   *time.Time      `json:"finishedAt,omitempty"`
	Logs         string          `json:"logs,omitempty"`
	Error        string          `json:"error,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"`
}

type RunnableRun struct {
	Run
	ArtifactPath string
	Manifest     Manifest
	Event        EventEnvelope
}
