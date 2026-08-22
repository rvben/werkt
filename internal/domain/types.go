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

	DeploymentQueued     = "queued"
	DeploymentValidating = "validating"
	DeploymentBuilding   = "building"
	DeploymentChecking   = "checking"
	DeploymentActivating = "activating"
	DeploymentSucceeded  = "succeeded"
	DeploymentFailed     = "failed"
	DeploymentCancelled  = "cancelled"

	DeploymentStepRunning   = "running"
	DeploymentStepSucceeded = "succeeded"
	DeploymentStepFailed    = "failed"
)

type Manifest struct {
	APIVersion string           `yaml:"apiVersion" json:"apiVersion"`
	Kind       string           `yaml:"kind" json:"kind"`
	Metadata   Metadata         `yaml:"metadata" json:"metadata"`
	Triggers   []Trigger        `yaml:"triggers" json:"triggers"`
	Runtime    Runtime          `yaml:"runtime" json:"runtime"`
	Deployment DeploymentPolicy `yaml:"deployment,omitempty" json:"deployment,omitempty"`
	Execution  Execution        `yaml:"execution" json:"execution"`
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
	Egress      []EgressRule      `yaml:"egress,omitempty" json:"egress,omitempty"`
}

// EgressRule names one exact destination available to an automation at
// runtime. Hostnames are resolved and pinned by Husker before the VM boots.
type EgressRule struct {
	Host     string `yaml:"host" json:"host"`
	Port     uint16 `yaml:"port" json:"port"`
	Protocol string `yaml:"protocol,omitempty" json:"protocol,omitempty"`
}

func (r EgressRule) EffectiveProtocol() string {
	if r.Protocol == "" {
		return "tcp"
	}
	return r.Protocol
}

// DeploymentPolicy defines language-neutral commands that must pass before a
// revision can become active. Checks run in the build plane, never in the
// runtime attempt environment, and therefore never receive runtime secrets.
type DeploymentPolicy struct {
	Checks []DeploymentCheck `yaml:"checks,omitempty" json:"checks,omitempty"`
}

type DeploymentCheck struct {
	ID      string   `yaml:"id" json:"id"`
	Command []string `yaml:"command" json:"command"`
	Timeout string   `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

func (c DeploymentCheck) TimeoutDuration() time.Duration {
	if c.Timeout == "" {
		return 10 * time.Minute
	}
	duration, err := time.ParseDuration(c.Timeout)
	if err != nil {
		return 10 * time.Minute
	}
	return duration
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

// Deployment is the durable, agent-visible lifecycle of one uploaded package.
// SourcePath and the idempotency key stay internal to the control plane.
type Deployment struct {
	ID                string           `json:"id"`
	Status            string           `json:"status"`
	AutomationID      string           `json:"automationId,omitempty"`
	PackageDigest     string           `json:"packageDigest"`
	ContentHash       string           `json:"contentHash,omitempty"`
	RevisionID        string           `json:"revisionId,omitempty"`
	RetryOf           string           `json:"retryOf,omitempty"`
	Actor             string           `json:"actor"`
	Error             string           `json:"error,omitempty"`
	Steps             []DeploymentStep `json:"steps,omitempty"`
	CreatedAt         time.Time        `json:"createdAt"`
	UpdatedAt         time.Time        `json:"updatedAt"`
	StartedAt         *time.Time       `json:"startedAt,omitempty"`
	FinishedAt        *time.Time       `json:"finishedAt,omitempty"`
	CancelRequestedAt *time.Time       `json:"cancelRequestedAt,omitempty"`
}

type RunnableDeployment struct {
	Deployment
	SourcePath string
}

type DeploymentStep struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	Logs       string     `json:"logs,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

type DeploymentStepUpdate struct {
	ID     string
	Kind   string
	Status string
	Logs   string
	Error  string
}

type DeploymentStepReporter func(DeploymentStepUpdate) error
