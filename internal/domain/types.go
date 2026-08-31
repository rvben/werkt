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
	Tools       map[string]string `yaml:"tools,omitempty" json:"tools,omitempty"`
	Build       []string          `yaml:"build,omitempty" json:"build,omitempty"`
	Command     []string          `yaml:"command" json:"command"`
	Environment map[string]string `yaml:"environment,omitempty" json:"environment,omitempty"`
	Secrets     map[string]string `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	Egress      []EgressRule      `yaml:"egress,omitempty" json:"egress,omitempty"`
	// ResolvedTools is control-plane output. It is persisted with a revision but
	// cannot be supplied by automation YAML.
	ResolvedTools *ResolvedToolEnvironment `yaml:"-" json:"resolvedTools,omitempty"`
}

// ResolvedToolEnvironment is the immutable execution environment selected for
// runtime.tools. It binds the public request to the catalog, preparation code,
// installer and base-image inputs, plus the resulting Husker image bytes.
type ResolvedToolEnvironment struct {
	Version           int            `json:"version"`
	IdentityDigest    string         `json:"identityDigest"`
	Image             string         `json:"image"`
	ImageDigest       string         `json:"imageDigest"`
	CatalogRevision   string         `json:"catalogRevision"`
	PreparerRevision  string         `json:"preparerRevision"`
	Platform          string         `json:"platform"`
	BaseImage         string         `json:"baseImage"`
	BaseImageDigest   string         `json:"baseImageDigest"`
	Installer         ToolInstaller  `json:"installer"`
	Tools             []ResolvedTool `json:"tools"`
	Capabilities      []string       `json:"capabilities"`
	PreparationEgress []EgressRule   `json:"preparationEgress"`
}

type ToolInstaller struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

type ResolvedTool struct {
	Name         string                   `json:"name"`
	Version      string                   `json:"version"`
	Backend      string                   `json:"backend"`
	Executables  []ResolvedToolExecutable `json:"executables,omitempty"`
	Capabilities []string                 `json:"capabilities"`
	Artifact     *ToolArtifact            `json:"artifact,omitempty"`
}

type ResolvedToolExecutable struct {
	Name         string `json:"name"`
	RelativePath string `json:"relativePath"`
}

// ToolArtifact pins a catalog tool to the exact archive bytes mise must
// verify before the prepared image can be committed.
type ToolArtifact struct {
	URL             string `json:"url"`
	Digest          string `json:"digest"`
	SizeBytes       int64  `json:"sizeBytes"`
	Format          string `json:"format"`
	StripComponents int    `json:"stripComponents"`
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
	Timeout     string      `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Retries     int         `yaml:"retries,omitempty" json:"retries,omitempty"`
	Concurrency string      `yaml:"concurrency,omitempty" json:"concurrency,omitempty"`
	State       StatePolicy `yaml:"state,omitempty" json:"state,omitempty"`
}

// StatePolicy enables a bounded JSON object that is snapshotted before an
// attempt and committed atomically with a successful run. Stateful automations
// must forbid overlapping runs so external side effects and state transitions
// cannot race each other.
type StatePolicy struct {
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
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

// RunControl is a bounded, language-neutral request for Werkt to continue an
// automation after the current attempt and its state commit succeed.
type RunControl struct {
	Defer    *DeferredRunRequest `json:"defer,omitempty"`
	Approval *ApprovalRequest    `json:"approval,omitempty"`
}

type DeferredRunRequest struct {
	Key   string          `json:"key"`
	Until time.Time       `json:"until"`
	Data  json.RawMessage `json:"data,omitempty"`
}

type ApprovalRequest struct {
	Key         string           `json:"key"`
	Title       string           `json:"title"`
	Description string           `json:"description,omitempty"`
	ExpiresAt   time.Time        `json:"expiresAt"`
	Fields      []ApprovalField  `json:"fields"`
	Actions     []ApprovalAction `json:"actions"`
}

type ApprovalField struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Required    bool     `json:"required,omitempty"`
	Value       any      `json:"value,omitempty"`
	Description string   `json:"description,omitempty"`
	Options     []string `json:"options,omitempty"`
}

type ApprovalAction struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	Style          string `json:"style,omitempty"`
	RequiresFields bool   `json:"requiresFields,omitempty"`
}

type Approval struct {
	ID             string           `json:"id"`
	AutomationID   string           `json:"automationId"`
	RevisionID     string           `json:"revisionId"`
	RequestedByRun string           `json:"requestedByRunId"`
	Key            string           `json:"key"`
	Status         string           `json:"status"`
	Title          string           `json:"title"`
	Description    string           `json:"description,omitempty"`
	Fields         []ApprovalField  `json:"fields"`
	Actions        []ApprovalAction `json:"actions"`
	ExpiresAt      time.Time        `json:"expiresAt"`
	CreatedAt      time.Time        `json:"createdAt"`
	ResolvedAt     *time.Time       `json:"resolvedAt,omitempty"`
	ResolvedBy     string           `json:"resolvedBy,omitempty"`
	Response       json.RawMessage  `json:"response,omitempty"`
	ActionRunID    string           `json:"actionRunId,omitempty"`
}

// RunSummary is the bounded, log-free representation returned by run-list
// endpoints. Full logs, errors, event identity, and structured results remain
// available from the run detail endpoint.
type RunSummary struct {
	ID           string     `json:"id"`
	AutomationID string     `json:"automationId"`
	RevisionID   string     `json:"revisionId"`
	Status       string     `json:"status"`
	Attempt      int        `json:"attempt"`
	MaxAttempts  int        `json:"maxAttempts"`
	CreatedAt    time.Time  `json:"createdAt"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
}

type RunnableRun struct {
	Run
	ArtifactPath string
	Provenance   ArtifactProvenance
	Manifest     Manifest
	Event        EventEnvelope
	State        json.RawMessage
	StateVersion int64
}

// ArtifactProvenance binds one immutable artifact tree to the effective images
// and source content that produced a revision. The signature is verified from
// externally custodied key material before every execution attempt.
type ArtifactProvenance struct {
	Version         int                      `json:"version"`
	ArtifactDigest  string                   `json:"artifactDigest"`
	ContentHash     string                   `json:"contentHash"`
	AutomationID    string                   `json:"automationId"`
	RuntimeImage    string                   `json:"runtimeImage,omitempty"`
	BuildImage      string                   `json:"buildImage,omitempty"`
	ToolEnvironment *ResolvedToolEnvironment `json:"toolEnvironment,omitempty"`
	Algorithm       string                   `json:"algorithm"`
	SigningKeyID    string                   `json:"signingKeyId"`
	PublicKey       string                   `json:"publicKey"`
	Signature       string                   `json:"signature"`
}

// Deployment is the durable, agent-visible lifecycle of one uploaded package.
// SourcePath and the idempotency key stay internal to the control plane.
type Deployment struct {
	ID                string              `json:"id"`
	Status            string              `json:"status"`
	AutomationID      string              `json:"automationId,omitempty"`
	PackageDigest     string              `json:"packageDigest"`
	ContentHash       string              `json:"contentHash,omitempty"`
	RevisionID        string              `json:"revisionId,omitempty"`
	RetryOf           string              `json:"retryOf,omitempty"`
	Actor             string              `json:"actor"`
	Error             string              `json:"error,omitempty"`
	Provenance        *ArtifactProvenance `json:"provenance,omitempty"`
	Steps             []DeploymentStep    `json:"steps,omitempty"`
	CreatedAt         time.Time           `json:"createdAt"`
	UpdatedAt         time.Time           `json:"updatedAt"`
	StartedAt         *time.Time          `json:"startedAt,omitempty"`
	FinishedAt        *time.Time          `json:"finishedAt,omitempty"`
	CancelRequestedAt *time.Time          `json:"cancelRequestedAt,omitempty"`
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
