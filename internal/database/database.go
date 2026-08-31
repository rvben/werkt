package database

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/robfig/cron/v3"
	"github.com/rvben/werkt/internal/domain"
)

var (
	ErrRunLeaseLost                  = errors.New("run lease ownership was lost")
	ErrDeploymentLeaseLost           = errors.New("deployment lease ownership was lost")
	ErrDeploymentIdempotencyConflict = errors.New("deployment idempotency key was already used for another package")
	ErrDeploymentNotRetryable        = errors.New("deployment is not failed or cancelled")
	ErrDeploymentSourceUnavailable   = errors.New("deployment source is unavailable")
	ErrDeploymentNotCancellable      = errors.New("deployment is already terminal")
	ErrDeploymentCancelled           = errors.New("deployment cancellation was requested")
	ErrAutomationNotFound            = errors.New("automation not found")
	ErrAutomationRevisionChanged     = errors.New("automation active revision changed")
	ErrRevisionNotFound              = errors.New("revision not found for automation")
	ErrRevisionArtifactUnavailable   = errors.New("revision artifact is no longer retained")
	ErrRunNotFound                   = errors.New("run not found")
	ErrDeploymentNotFound            = errors.New("deployment not found")
	ErrTriggerNotFound               = errors.New("enabled trigger not found")
	ErrRetentionPlanNotFound         = errors.New("retention plan not found")
	ErrRetentionPlanExpired          = errors.New("retention plan expired")
	ErrRetentionPlanBusy             = errors.New("retention plan is already being applied")
	ErrSecretNotFound                = errors.New("secret not found")
	ErrSecretInUse                   = errors.New("secret is in use")
	ErrAutomationStateConflict       = errors.New("automation state changed after the run started")
	ErrArtifactProvenanceRequired    = errors.New("valid artifact provenance is required")
	ErrApprovalNotFound              = errors.New("approval not found")
	ErrApprovalResolved              = errors.New("approval is already resolved")
	ErrApprovalExpired               = errors.New("approval has expired")
	ErrApprovalInvalidResponse       = errors.New("approval response is invalid")
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct {
	pool *pgxpool.Pool
}

type AutomationSummary struct {
	ID               string      `json:"id"`
	Project          string      `json:"project"`
	Folder           string      `json:"folder,omitempty"`
	Description      string      `json:"description,omitempty"`
	Labels           []string    `json:"labels"`
	Enabled          bool        `json:"enabled"`
	ActiveRevisionID string      `json:"activeRevisionId"`
	UpdatedAt        time.Time   `json:"updatedAt"`
	LatestRun        *RunSummary `json:"latestRun,omitempty"`
}

type RunSummary struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

type AutomationFilter struct {
	Project string
	Folder  string
	Label   string
	Query   string
	Enabled *bool
}

type TriggerSummary struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	Enabled    bool            `json:"enabled"`
	Config     json.RawMessage `json:"config"`
	NextFireAt *time.Time      `json:"nextFireAt,omitempty"`
}

type RevisionSummary struct {
	ID          string                     `json:"id"`
	ContentHash string                     `json:"contentHash"`
	Active      bool                       `json:"active"`
	Provenance  *domain.ArtifactProvenance `json:"provenance,omitempty"`
	CreatedAt   time.Time                  `json:"createdAt"`
}

type RevisionArtifact struct {
	AutomationID string
	RevisionID   string
	ContentHash  string
	Path         string
	Manifest     domain.Manifest
	Provenance   domain.ArtifactProvenance
}

type AutomationDetail struct {
	AutomationSummary
	Manifest  domain.Manifest   `json:"manifest"`
	Triggers  []TriggerSummary  `json:"triggers"`
	Revisions []RevisionSummary `json:"revisions"`
}

type AuditEvent struct {
	ID           string          `json:"id"`
	Action       string          `json:"action"`
	AutomationID string          `json:"automationId"`
	Actor        string          `json:"actor"`
	Details      json.RawMessage `json:"details"`
	CreatedAt    time.Time       `json:"createdAt"`
}

type ListCursor struct {
	CreatedAt time.Time
	ID        string
}

type AuditFilter struct {
	AutomationID string
	Action       string
	Actor        string
	Since        *time.Time
	Until        *time.Time
}

type TriggerIngressPolicy struct {
	Config json.RawMessage
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		contents, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		var applied bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, entry.Name()).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(contents)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, entry.Name()); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Deploy(ctx context.Context, value domain.Manifest, contentHash, artifactPath string, provenance domain.ArtifactProvenance) (string, error) {
	return s.deploy(ctx, value, contentHash, artifactPath, provenance, "cli", "", "")
}

func (s *Store) DeployAs(ctx context.Context, value domain.Manifest, contentHash, artifactPath string, provenance domain.ArtifactProvenance, actor string) (string, error) {
	return s.deploy(ctx, value, contentHash, artifactPath, provenance, actor, "", "")
}

// ActivateDeployment publishes an immutable revision and completes its durable
// deployment job in one transaction. A worker that lost its lease cannot
// activate an artifact.
func (s *Store) ActivateDeployment(ctx context.Context, deploymentID, workerID string, value domain.Manifest, contentHash, artifactPath string, provenance domain.ArtifactProvenance, actor string) (string, error) {
	return s.deploy(ctx, value, contentHash, artifactPath, provenance, actor, deploymentID, workerID)
}

func (s *Store) deploy(ctx context.Context, value domain.Manifest, contentHash, artifactPath string, provenance domain.ArtifactProvenance, actor, deploymentID, workerID string) (string, error) {
	validVersion := provenance.Version == 1 && provenance.ToolEnvironment == nil ||
		provenance.Version == 2 && validResolvedToolEnvironment(provenance.ToolEnvironment)
	if !validSHA256Hex(contentHash) || !validVersion || provenance.Algorithm != "ed25519" ||
		!validSHA256Digest(provenance.ArtifactDigest) || !validSHA256Digest(provenance.SigningKeyID) ||
		provenance.PublicKey == "" || provenance.Signature == "" ||
		provenance.ContentHash != contentHash || provenance.AutomationID != value.Metadata.Name ||
		!reflect.DeepEqual(provenance.ToolEnvironment, value.Runtime.ResolvedTools) {
		return "", ErrArtifactProvenanceRequired
	}
	manifestJSON, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode manifest: %w", err)
	}
	labelsJSON, err := json.Marshal(value.Metadata.Labels)
	if err != nil {
		return "", fmt.Errorf("encode labels: %w", err)
	}
	provenanceJSON, err := json.Marshal(provenance)
	if err != nil {
		return "", fmt.Errorf("encode artifact provenance: %w", err)
	}
	revisionID := "rev_" + contentHash[:24]

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	_, err = tx.Exec(ctx, `
		INSERT INTO automations (id, project, folder, description, labels)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE SET
			project = EXCLUDED.project,
			folder = EXCLUDED.folder,
			description = EXCLUDED.description,
			labels = EXCLUDED.labels,
			updated_at = now()`,
		value.Metadata.Name, value.Metadata.Project, value.Metadata.Folder, value.Metadata.Description, labelsJSON)
	if err != nil {
		return "", fmt.Errorf("upsert automation: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO revisions (id, automation_id, content_hash, manifest, artifact_path, provenance)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (automation_id, content_hash) DO UPDATE SET
			artifact_path = EXCLUDED.artifact_path, provenance = EXCLUDED.provenance`,
		revisionID, value.Metadata.Name, contentHash, manifestJSON, artifactPath, provenanceJSON)
	if err != nil {
		return "", fmt.Errorf("insert revision: %w", err)
	}

	if err := replaceTriggers(ctx, tx, value.Metadata.Name, revisionID, value.Triggers); err != nil {
		return "", err
	}
	if err := replaceSecretReferences(ctx, tx, revisionID, value.SecretReferences()); err != nil {
		return "", err
	}

	if _, err := tx.Exec(ctx, `UPDATE automations SET active_revision_id = $2, updated_at = now() WHERE id = $1`, value.Metadata.Name, revisionID); err != nil {
		return "", fmt.Errorf("activate revision: %w", err)
	}
	if err := insertAuditEvent(ctx, tx, "automation.deployed", value.Metadata.Name, actor, map[string]any{
		"revisionId":   revisionID,
		"contentHash":  contentHash,
		"deploymentId": deploymentID,
		"provenance":   provenanceAuditDetails(provenance),
	}); err != nil {
		return "", fmt.Errorf("audit deployment: %w", err)
	}
	if deploymentID != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE deployment_steps
			SET status = $3, finished_at = now()
			WHERE deployment_id = $1 AND id = 'activate' AND status = $2`,
			deploymentID, domain.DeploymentStepRunning, domain.DeploymentStepSucceeded); err != nil {
			return "", fmt.Errorf("complete activation step: %w", err)
		}
		command, err := tx.Exec(ctx, `
			UPDATE deployments
			SET status = $3, revision_id = $4, provenance = $6, error = '', updated_at = now(), finished_at = now(),
				lease_owner = NULL, lease_expires_at = NULL
			WHERE id = $1 AND lease_owner = $2 AND status = $5 AND cancel_requested_at IS NULL`,
			deploymentID, workerID, domain.DeploymentSucceeded, revisionID, domain.DeploymentActivating, provenanceJSON)
		if err != nil {
			return "", fmt.Errorf("complete deployment: %w", err)
		}
		if command.RowsAffected() != 1 {
			return "", deploymentLeaseOrCancellation(ctx, tx, deploymentID)
		}
		if err := insertAuditEvent(ctx, tx, "deployment.succeeded", value.Metadata.Name, actor, map[string]any{
			"deploymentId": deploymentID,
			"revisionId":   revisionID,
			"contentHash":  contentHash,
			"provenance":   provenanceAuditDetails(provenance),
		}); err != nil {
			return "", fmt.Errorf("audit successful deployment: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return revisionID, nil
}

func validSHA256Digest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && validSHA256Hex(strings.TrimPrefix(value, "sha256:"))
}

func validSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func provenanceAuditDetails(value domain.ArtifactProvenance) map[string]any {
	if value.ArtifactDigest == "" {
		return nil
	}
	return map[string]any{
		"artifactDigest":  value.ArtifactDigest,
		"signingKeyId":    value.SigningKeyID,
		"runtimeImage":    value.RuntimeImage,
		"buildImage":      value.BuildImage,
		"toolEnvironment": value.ToolEnvironment,
	}
}

func validResolvedToolEnvironment(value *domain.ResolvedToolEnvironment) bool {
	return value != nil && value.Version == 1 && value.Image != "" && value.BaseImage != "" &&
		validSHA256Digest(value.IdentityDigest) && validSHA256Digest(value.ImageDigest) &&
		validSHA256Digest(value.BaseImageDigest) && validSHA256Digest(value.Installer.Digest) && len(value.Tools) > 0
}

func replaceTriggers(ctx context.Context, tx pgx.Tx, automationID, revisionID string, triggers []domain.Trigger) error {
	if _, err := tx.Exec(ctx, `DELETE FROM triggers WHERE automation_id = $1`, automationID); err != nil {
		return fmt.Errorf("replace triggers: %w", err)
	}
	for _, trigger := range triggers {
		configJSON := []byte("{}")
		var err error
		if trigger.Config != nil {
			configJSON, err = json.Marshal(trigger.Config)
			if err != nil {
				return fmt.Errorf("encode trigger %s: %w", trigger.ID, err)
			}
		}
		var nextFireAt *time.Time
		if trigger.Type == "schedule" && trigger.IsEnabled() {
			next, err := nextSchedule(trigger.Config, time.Now())
			if err != nil {
				return fmt.Errorf("calculate schedule %s: %w", trigger.ID, err)
			}
			nextFireAt = &next
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO triggers (automation_id, id, revision_id, type, config, enabled, next_fire_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			automationID, trigger.ID, revisionID, trigger.Type, configJSON, trigger.IsEnabled(), nextFireAt); err != nil {
			return fmt.Errorf("insert trigger %s: %w", trigger.ID, err)
		}
	}
	return nil
}

// RollbackAutomation atomically reactivates an existing immutable revision and
// reconstructs its trigger set. Repeating the same rollback is a safe no-op.
func (s *Store) RollbackAutomation(ctx context.Context, automationID, revisionID, actor string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var currentRevision string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(active_revision_id, '') FROM automations WHERE id = $1 FOR UPDATE`, automationID).Scan(&currentRevision); errors.Is(err, pgx.ErrNoRows) {
		return false, ErrAutomationNotFound
	} else if err != nil {
		return false, err
	}
	var manifestJSON []byte
	var artifactPath string
	if err := tx.QueryRow(ctx, `
		SELECT manifest, artifact_path FROM revisions
		WHERE id = $1 AND automation_id = $2 FOR UPDATE`, revisionID, automationID).
		Scan(&manifestJSON, &artifactPath); errors.Is(err, pgx.ErrNoRows) {
		return false, ErrRevisionNotFound
	} else if err != nil {
		return false, err
	}
	if artifactPath == "" {
		return false, ErrRevisionArtifactUnavailable
	}
	if currentRevision == revisionID {
		return false, tx.Commit(ctx)
	}
	var value domain.Manifest
	if err := json.Unmarshal(manifestJSON, &value); err != nil {
		return false, fmt.Errorf("decode revision manifest: %w", err)
	}
	if err := replaceTriggers(ctx, tx, automationID, revisionID, value.Triggers); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE automations SET active_revision_id = $2, updated_at = now() WHERE id = $1`, automationID, revisionID); err != nil {
		return false, err
	}
	if err := insertAuditEvent(ctx, tx, "automation.rolled_back", automationID, actor, map[string]any{
		"fromRevisionId": currentRevision,
		"toRevisionId":   revisionID,
	}); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (s *Store) IngestEvent(ctx context.Context, automationID, triggerID, expectedType, externalID string, occurredAt time.Time, data json.RawMessage, metadata map[string]any) (string, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	runID, created, err := enqueueEvent(ctx, tx, automationID, triggerID, expectedType, externalID, occurredAt, data, metadata)
	if err != nil {
		return "", false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, err
	}
	return runID, created, nil
}

func (s *Store) GetTriggerIngressPolicy(ctx context.Context, automationID, triggerID, expectedType string) (TriggerIngressPolicy, error) {
	var policy TriggerIngressPolicy
	err := s.pool.QueryRow(ctx, `
		SELECT t.config
		FROM triggers t
		JOIN automations a ON a.id = t.automation_id AND a.active_revision_id = t.revision_id
		WHERE t.automation_id = $1 AND t.id = $2 AND t.type = $3
			AND t.enabled = true AND a.enabled = true`, automationID, triggerID, expectedType).
		Scan(&policy.Config)
	if errors.Is(err, pgx.ErrNoRows) {
		return TriggerIngressPolicy{}, ErrTriggerNotFound
	}
	return policy, err
}

func enqueueEvent(ctx context.Context, tx pgx.Tx, automationID, triggerID, expectedType, externalID string, occurredAt time.Time, data json.RawMessage, metadata map[string]any) (string, bool, error) {
	var revisionID, triggerType string
	var manifestJSON, triggerConfigJSON []byte
	err := tx.QueryRow(ctx, `
		SELECT t.revision_id, t.type, t.config, r.manifest
		FROM triggers t
		JOIN automations a ON a.id = t.automation_id AND a.active_revision_id = t.revision_id
		JOIN revisions r ON r.id = t.revision_id
		WHERE t.automation_id = $1 AND t.id = $2 AND t.type = $3
			AND t.enabled = true AND a.enabled = true`, automationID, triggerID, expectedType).
		Scan(&revisionID, &triggerType, &triggerConfigJSON, &manifestJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, fmt.Errorf("enabled %s trigger %s/%s not found", expectedType, automationID, triggerID)
	}
	if err != nil {
		return "", false, err
	}
	if externalID == "" {
		externalID = newID("external")
	}
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	eventID := newID("evt")
	runID := newID("run")
	envelope := domain.EventEnvelope{
		ID:         eventID,
		OccurredAt: occurredAt.UTC(),
		ReceivedAt: time.Now().UTC(),
		Trigger: domain.EventTrigger{
			Automation: automationID,
			ID:         triggerID,
			Type:       triggerType,
		},
		Data:     data,
		Metadata: metadata,
	}
	envelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		return "", false, err
	}
	command, err := tx.Exec(ctx, `
		INSERT INTO events (id, trigger_key, external_id, envelope, occurred_at, received_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (trigger_key, external_id) DO NOTHING`,
		eventID, automationID+":"+triggerID, externalID, envelopeJSON, envelope.OccurredAt, envelope.ReceivedAt)
	if err != nil {
		return "", false, err
	}
	if command.RowsAffected() == 0 {
		var existingRunID string
		err := tx.QueryRow(ctx, `
			SELECT r.id FROM runs r
			JOIN events e ON e.id = r.event_id
			WHERE e.trigger_key = $1 AND e.external_id = $2`, automationID+":"+triggerID, externalID).Scan(&existingRunID)
		return existingRunID, false, err
	}

	var value domain.Manifest
	if err := json.Unmarshal(manifestJSON, &value); err != nil {
		return "", false, err
	}
	policy := value.Execution.Concurrency
	if policy == "" {
		policy = "allow"
	}
	availableAt := time.Now().UTC()
	if triggerType == "webhook" {
		var triggerConfig struct {
			DeliveryDelay string `json:"deliveryDelay"`
		}
		if err := json.Unmarshal(triggerConfigJSON, &triggerConfig); err != nil {
			return "", false, err
		}
		if triggerConfig.DeliveryDelay != "" {
			delay, err := time.ParseDuration(triggerConfig.DeliveryDelay)
			if err != nil || delay <= 0 || delay > 24*time.Hour {
				return "", false, errors.New("webhook trigger has invalid delivery delay")
			}
			availableAt = availableAt.Add(delay)
		}
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO runs (id, automation_id, revision_id, event_id, status, max_attempts, concurrency_policy, available_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		runID, automationID, revisionID, eventID, domain.RunQueued, value.Execution.Retries+1, policy, availableAt)
	if err != nil {
		return "", false, err
	}
	return runID, true, nil
}

func (s *Store) EnqueueDueSchedules(ctx context.Context, now time.Time, limit int) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	rows, err := tx.Query(ctx, `
		SELECT t.automation_id, t.id, t.config, t.next_fire_at
		FROM triggers t
		JOIN automations a ON a.id = t.automation_id
		WHERE t.type = 'schedule' AND t.enabled = true AND a.enabled = true
			AND t.next_fire_at <= $1
		ORDER BY t.next_fire_at
		FOR UPDATE SKIP LOCKED
		LIMIT $2`, now, limit)
	if err != nil {
		return 0, err
	}
	type due struct {
		automationID string
		triggerID    string
		config       map[string]any
		fireAt       time.Time
	}
	var dueTriggers []due
	for rows.Next() {
		var item due
		var configJSON []byte
		if err := rows.Scan(&item.automationID, &item.triggerID, &configJSON, &item.fireAt); err != nil {
			rows.Close()
			return 0, err
		}
		if err := json.Unmarshal(configJSON, &item.config); err != nil {
			rows.Close()
			return 0, err
		}
		dueTriggers = append(dueTriggers, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	created := 0
	for _, item := range dueTriggers {
		next, err := nextSchedule(item.config, item.fireAt)
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE triggers SET next_fire_at = $3, updated_at = now() WHERE automation_id = $1 AND id = $2`, item.automationID, item.triggerID, next); err != nil {
			return 0, err
		}
		data, _ := json.Marshal(map[string]any{"scheduledAt": item.fireAt.UTC()})
		externalID := item.fireAt.UTC().Format(time.RFC3339Nano)
		if _, wasCreated, err := enqueueEvent(ctx, tx, item.automationID, item.triggerID, "schedule", externalID, item.fireAt, data, map[string]any{"source": "scheduler"}); err != nil {
			return 0, err
		} else if wasCreated {
			created++
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return created, nil
}

func (s *Store) AcquireRun(ctx context.Context, workerID string, leaseDuration time.Duration) (*domain.RunnableRun, error) {
	var run domain.RunnableRun
	var manifestJSON, eventJSON, stateJSON, provenanceJSON []byte
	err := s.pool.QueryRow(ctx, `
		WITH candidate AS (
			SELECT r.id
			FROM runs r
			WHERE (
				(r.status = 'queued' AND r.available_at <= now())
				OR (r.status = 'running' AND r.lease_expires_at < now())
			)
			AND NOT (
				r.concurrency_policy = 'forbid'
				AND EXISTS (
					SELECT 1 FROM runs active
					WHERE active.automation_id = r.automation_id
					AND active.id <> r.id
					AND active.status = 'running'
					AND active.lease_expires_at >= now()
				)
			)
			ORDER BY r.available_at, r.created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		), claimed AS (
			UPDATE runs r
			SET status = 'running', attempt = attempt + 1, lease_owner = $1,
				lease_expires_at = now() + $2::interval,
				started_at = COALESCE(started_at, now()), error = ''
			FROM candidate
			WHERE r.id = candidate.id
			RETURNING r.*
		)
		SELECT c.id, c.automation_id, c.revision_id, c.event_id, c.status,
			c.attempt, c.max_attempts, c.created_at, c.started_at, c.finished_at,
			c.error, c.result, rev.artifact_path, rev.provenance, rev.manifest, e.envelope,
			COALESCE(state.value, '{}'::jsonb), COALESCE(state.version, 0)
		FROM claimed c
		JOIN revisions rev ON rev.id = c.revision_id
		JOIN events e ON e.id = c.event_id
		LEFT JOIN automation_state state ON state.automation_id = c.automation_id`, workerID, leaseDuration.String()).Scan(
		&run.ID, &run.AutomationID, &run.RevisionID, &run.EventID, &run.Status,
		&run.Attempt, &run.MaxAttempts, &run.CreatedAt, &run.StartedAt, &run.FinishedAt,
		&run.Error, &run.Result, &run.ArtifactPath, &provenanceJSON, &manifestJSON, &eventJSON,
		&stateJSON, &run.StateVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(manifestJSON, &run.Manifest); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(provenanceJSON, &run.Provenance); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(eventJSON, &run.Event); err != nil {
		return nil, err
	}
	run.State = stateJSON
	return &run, nil
}

// RenewRunLease extends a running claim only while it is still owned by the
// same worker. A false result means another worker may have reclaimed it and
// the caller must stop executing the automation.
func (s *Store) RenewRunLease(ctx context.Context, runID, workerID string, leaseDuration time.Duration) (bool, error) {
	command, err := s.pool.Exec(ctx, `
		UPDATE runs
		SET lease_expires_at = now() + $3::interval
		WHERE id = $1 AND status = 'running' AND lease_owner = $2`,
		runID, workerID, leaseDuration.String())
	if err != nil {
		return false, err
	}
	return command.RowsAffected() == 1, nil
}

func (s *Store) CompleteRun(ctx context.Context, run domain.RunnableRun, workerID, logs string, result, state json.RawMessage, control domain.RunControl) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	command, err := tx.Exec(ctx, `
		UPDATE runs SET status = 'succeeded', logs = $2, result = $3, error = '',
			finished_at = now(), lease_owner = NULL, lease_expires_at = NULL
		WHERE id = $1 AND status = 'running' AND lease_owner = $4`,
		run.ID, logs, nullableJSON(result), workerID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrRunLeaseLost
	}
	if run.Manifest.Execution.State.Enabled {
		if !validStateObject(state) {
			return errors.New("automation state is not a JSON object")
		}
		if run.StateVersion == 0 {
			command, err = tx.Exec(ctx, `
				INSERT INTO automation_state (automation_id, version, value, updated_by_run_id)
				VALUES ($1, 1, $2, $3)
				ON CONFLICT (automation_id) DO NOTHING`, run.AutomationID, state, run.ID)
		} else {
			command, err = tx.Exec(ctx, `
				UPDATE automation_state
				SET version = version + 1, value = $3, updated_by_run_id = $4, updated_at = now()
				WHERE automation_id = $1 AND version = $2`, run.AutomationID, run.StateVersion, state, run.ID)
		}
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return ErrAutomationStateConflict
		}
	}
	if control.Defer != nil {
		if err := enqueueDeferredRun(ctx, tx, run, *control.Defer); err != nil {
			return err
		}
	}
	if control.Approval != nil {
		approvalID := newID("approval")
		fields, err := json.Marshal(control.Approval.Fields)
		if err != nil {
			return err
		}
		actions, err := json.Marshal(control.Approval.Actions)
		if err != nil {
			return err
		}
		command, err := tx.Exec(ctx, `
			INSERT INTO approvals (id, automation_id, revision_id, requested_by_run_id,
				approval_key, title, description, fields, actions, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (automation_id, approval_key) DO NOTHING`,
			approvalID, run.AutomationID, run.RevisionID, run.ID,
			control.Approval.Key, strings.TrimSpace(control.Approval.Title),
			strings.TrimSpace(control.Approval.Description), fields, actions, control.Approval.ExpiresAt)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return errors.New("approval key was already used by this automation")
		}
		if err := insertAuditEvent(ctx, tx, "approval.requested", run.AutomationID, "run:"+run.ID, map[string]any{"approvalId": approvalID, "revisionId": run.RevisionID, "key": control.Approval.Key}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func enqueueDeferredRun(ctx context.Context, tx pgx.Tx, parent domain.RunnableRun, request domain.DeferredRunRequest) error {
	now := time.Now().UTC()
	eventID := newID("evt")
	runID := newID("run")
	externalID := parent.ID + ":" + request.Key
	envelope := domain.EventEnvelope{
		ID: eventID, OccurredAt: request.Until.UTC(), ReceivedAt: now,
		Trigger:  domain.EventTrigger{Automation: parent.AutomationID, ID: "deferred", Type: "deferred"},
		Data:     request.Data,
		Metadata: map[string]any{"source": "deferred", "parentRunId": parent.ID, "continuationKey": request.Key},
	}
	envelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO events (id, trigger_key, external_id, envelope, occurred_at, received_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, eventID, parent.AutomationID+":deferred", externalID, envelopeJSON, request.Until.UTC(), now); err != nil {
		return err
	}
	policy := parent.Manifest.Execution.Concurrency
	if policy == "" {
		policy = "allow"
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO runs (id, automation_id, revision_id, event_id, status, max_attempts, concurrency_policy, available_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, runID, parent.AutomationID,
		parent.RevisionID, eventID, domain.RunQueued, parent.Manifest.Execution.Retries+1, policy, request.Until.UTC())
	return err
}

func validStateObject(value json.RawMessage) bool {
	var object map[string]any
	return len(value) > 0 && json.Unmarshal(value, &object) == nil && object != nil
}

func (s *Store) FailRun(ctx context.Context, run domain.Run, workerID, logs string, runErr error) error {
	status := domain.RunFailed
	availableAt := time.Now()
	finishedAt := any(time.Now())
	if run.Attempt < run.MaxAttempts {
		status = domain.RunQueued
		availableAt = time.Now().Add(retryDelay(run.Attempt))
		finishedAt = nil
	}
	command, err := s.pool.Exec(ctx, `
		UPDATE runs SET status = $2, logs = $3, error = $4, available_at = $5,
			finished_at = $6, lease_owner = NULL, lease_expires_at = NULL
		WHERE id = $1 AND status = 'running' AND lease_owner = $7`,
		run.ID, status, logs, runErr.Error(), availableAt, finishedAt, workerID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrRunLeaseLost
	}
	return nil
}

func (s *Store) ListAutomations(ctx context.Context, filter AutomationFilter) ([]AutomationSummary, error) {
	var enabled any
	if filter.Enabled != nil {
		enabled = *filter.Enabled
	}
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.project, a.folder, a.description, a.labels, a.enabled,
			COALESCE(a.active_revision_id, ''), a.updated_at,
			COALESCE(latest_run.id, ''), COALESCE(latest_run.status, ''), latest_run.created_at
		FROM automations a
		LEFT JOIN LATERAL (
			SELECT id, status, created_at
			FROM runs
			WHERE automation_id = a.id
			ORDER BY created_at DESC, id DESC
			LIMIT 1
		) latest_run ON true
		WHERE ($1 = '' OR a.project = $1)
			AND ($2 = '' OR a.folder = $2)
			AND ($3 = '' OR a.labels ? $3)
			AND ($4 = '' OR a.id ILIKE '%' || $4 || '%' OR a.description ILIKE '%' || $4 || '%')
			AND ($5::boolean IS NULL OR a.enabled = $5)
		ORDER BY a.project, a.folder, a.id`,
		filter.Project, filter.Folder, filter.Label, filter.Query, enabled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]AutomationSummary, 0)
	for rows.Next() {
		var item AutomationSummary
		var labelsJSON []byte
		var latestRunID, latestRunStatus string
		var latestRunCreatedAt *time.Time
		if err := rows.Scan(&item.ID, &item.Project, &item.Folder, &item.Description,
			&labelsJSON, &item.Enabled, &item.ActiveRevisionID, &item.UpdatedAt,
			&latestRunID, &latestRunStatus, &latestRunCreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(labelsJSON, &item.Labels); err != nil {
			return nil, err
		}
		if latestRunCreatedAt != nil {
			item.LatestRun = &RunSummary{ID: latestRunID, Status: latestRunStatus, CreatedAt: *latestRunCreatedAt}
		}
		values = append(values, item)
	}
	return values, rows.Err()
}

func (s *Store) GetAutomation(ctx context.Context, automationID string) (AutomationDetail, error) {
	var value AutomationDetail
	var labelsJSON, manifestJSON []byte
	var latestRunID, latestRunStatus string
	var latestRunCreatedAt *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT a.id, a.project, a.folder, a.description, a.labels, a.enabled,
			a.active_revision_id, a.updated_at, r.manifest,
			COALESCE(latest_run.id, ''), COALESCE(latest_run.status, ''), latest_run.created_at
		FROM automations a
		JOIN revisions r ON r.id = a.active_revision_id
		LEFT JOIN LATERAL (
			SELECT id, status, created_at
			FROM runs
			WHERE automation_id = a.id
			ORDER BY created_at DESC, id DESC
			LIMIT 1
		) latest_run ON true
		WHERE a.id = $1`, automationID).Scan(
		&value.ID, &value.Project, &value.Folder, &value.Description, &labelsJSON,
		&value.Enabled, &value.ActiveRevisionID, &value.UpdatedAt, &manifestJSON,
		&latestRunID, &latestRunStatus, &latestRunCreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AutomationDetail{}, ErrAutomationNotFound
	}
	if err != nil {
		return AutomationDetail{}, err
	}
	if err := json.Unmarshal(labelsJSON, &value.Labels); err != nil {
		return AutomationDetail{}, err
	}
	if err := json.Unmarshal(manifestJSON, &value.Manifest); err != nil {
		return AutomationDetail{}, err
	}
	if latestRunCreatedAt != nil {
		value.LatestRun = &RunSummary{ID: latestRunID, Status: latestRunStatus, CreatedAt: *latestRunCreatedAt}
	}

	triggerRows, err := s.pool.Query(ctx, `
		SELECT id, type, enabled, config, next_fire_at
		FROM triggers WHERE automation_id = $1 ORDER BY id`, automationID)
	if err != nil {
		return AutomationDetail{}, err
	}
	value.Triggers = make([]TriggerSummary, 0)
	for triggerRows.Next() {
		var trigger TriggerSummary
		if err := triggerRows.Scan(&trigger.ID, &trigger.Type, &trigger.Enabled, &trigger.Config, &trigger.NextFireAt); err != nil {
			triggerRows.Close()
			return AutomationDetail{}, err
		}
		value.Triggers = append(value.Triggers, trigger)
	}
	if err := triggerRows.Err(); err != nil {
		triggerRows.Close()
		return AutomationDetail{}, err
	}
	triggerRows.Close()

	revisionRows, err := s.pool.Query(ctx, `
		SELECT id, content_hash, id = $2, provenance, created_at
		FROM revisions WHERE automation_id = $1 ORDER BY created_at DESC, id DESC`,
		automationID, value.ActiveRevisionID)
	if err != nil {
		return AutomationDetail{}, err
	}
	defer revisionRows.Close()
	value.Revisions = make([]RevisionSummary, 0)
	for revisionRows.Next() {
		var revision RevisionSummary
		var provenanceJSON []byte
		if err := revisionRows.Scan(&revision.ID, &revision.ContentHash, &revision.Active, &provenanceJSON, &revision.CreatedAt); err != nil {
			return AutomationDetail{}, err
		}
		var artifactProvenance domain.ArtifactProvenance
		if err := json.Unmarshal(provenanceJSON, &artifactProvenance); err != nil {
			return AutomationDetail{}, err
		}
		if artifactProvenance.ArtifactDigest != "" {
			revision.Provenance = &artifactProvenance
		}
		value.Revisions = append(value.Revisions, revision)
	}
	return value, revisionRows.Err()
}

func (s *Store) GetRevisionArtifact(ctx context.Context, automationID, revisionID string) (RevisionArtifact, error) {
	var value RevisionArtifact
	var manifestJSON, provenanceJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT automation_id, id, content_hash, artifact_path, manifest, provenance
		FROM revisions WHERE automation_id = $1 AND id = $2`, automationID, revisionID).
		Scan(&value.AutomationID, &value.RevisionID, &value.ContentHash, &value.Path, &manifestJSON, &provenanceJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return RevisionArtifact{}, ErrRevisionNotFound
	}
	if err != nil {
		return RevisionArtifact{}, err
	}
	if value.Path == "" {
		return RevisionArtifact{}, ErrRevisionArtifactUnavailable
	}
	if err := json.Unmarshal(manifestJSON, &value.Manifest); err != nil {
		return RevisionArtifact{}, err
	}
	if err := json.Unmarshal(provenanceJSON, &value.Provenance); err != nil {
		return RevisionArtifact{}, err
	}
	return value, nil
}

func (s *Store) ListRevisionArtifacts(ctx context.Context) ([]RevisionArtifact, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT automation_id, id, content_hash, artifact_path, manifest, provenance
		FROM revisions WHERE artifact_path <> '' ORDER BY automation_id, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []RevisionArtifact
	for rows.Next() {
		var value RevisionArtifact
		var manifestJSON, provenanceJSON []byte
		if err := rows.Scan(&value.AutomationID, &value.RevisionID, &value.ContentHash, &value.Path, &manifestJSON, &provenanceJSON); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(manifestJSON, &value.Manifest); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(provenanceJSON, &value.Provenance); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) AdoptRevisionProvenance(ctx context.Context, artifact RevisionArtifact, value domain.ArtifactProvenance) (bool, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	command, err := tx.Exec(ctx, `
		UPDATE revisions SET provenance = $3
		WHERE automation_id = $1 AND id = $2 AND provenance = '{}'::jsonb`,
		artifact.AutomationID, artifact.RevisionID, encoded)
	if err != nil {
		return false, err
	}
	if command.RowsAffected() == 0 {
		return false, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE deployments SET provenance = $2 WHERE revision_id = $1`, artifact.RevisionID, encoded); err != nil {
		return false, err
	}
	if err := insertAuditEvent(ctx, tx, "artifact.adopted", artifact.AutomationID, "system:upgrade", map[string]any{
		"revisionId": artifact.RevisionID, "provenance": provenanceAuditDetails(value),
	}); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// SetAutomationEnabled pauses or resumes automatic trigger ingestion. Manual
// runs remain available while paused. Resuming schedules starts from the next
// future occurrence instead of replaying every occurrence missed while paused.
func (s *Store) SetAutomationEnabled(ctx context.Context, automationID string, enabled bool, actor string) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var current bool
	err = tx.QueryRow(ctx, `SELECT enabled FROM automations WHERE id = $1 FOR UPDATE`, automationID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrAutomationNotFound
	}
	if err != nil {
		return false, err
	}
	if current == enabled {
		return false, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE automations SET enabled = $2, updated_at = now() WHERE id = $1`, automationID, enabled); err != nil {
		return false, err
	}
	if enabled {
		rows, err := tx.Query(ctx, `
			SELECT id, config FROM triggers
			WHERE automation_id = $1 AND type = 'schedule' AND enabled = true
			FOR UPDATE`, automationID)
		if err != nil {
			return false, err
		}
		type scheduleTrigger struct {
			id     string
			config map[string]any
		}
		var schedules []scheduleTrigger
		for rows.Next() {
			var item scheduleTrigger
			var configJSON []byte
			if err := rows.Scan(&item.id, &configJSON); err != nil {
				rows.Close()
				return false, err
			}
			if err := json.Unmarshal(configJSON, &item.config); err != nil {
				rows.Close()
				return false, err
			}
			schedules = append(schedules, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return false, err
		}
		rows.Close()
		now := time.Now()
		for _, schedule := range schedules {
			next, err := nextSchedule(schedule.config, now)
			if err != nil {
				return false, err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE triggers SET next_fire_at = $3, updated_at = now()
				WHERE automation_id = $1 AND id = $2`, automationID, schedule.id, next); err != nil {
				return false, err
			}
		}
	}
	action := "automation.paused"
	if enabled {
		action = "automation.resumed"
	}
	if err := insertAuditEvent(ctx, tx, action, automationID, actor, map[string]any{"enabled": enabled}); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) EnqueueManualRun(ctx context.Context, automationID, externalID, expectedRevisionID string, data json.RawMessage, actor string) (string, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if externalID != "" {
		var existingRunID string
		err := tx.QueryRow(ctx, `
			SELECT r.id FROM runs r JOIN events e ON e.id = r.event_id
			WHERE e.trigger_key = $1 AND e.external_id = $2`, automationID+":manual", externalID).Scan(&existingRunID)
		if err == nil {
			if err := tx.Commit(ctx); err != nil {
				return "", false, err
			}
			return existingRunID, false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", false, err
		}
	}
	var revisionID string
	err = tx.QueryRow(ctx, `
		SELECT active_revision_id
		FROM automations
		WHERE id = $1
		FOR UPDATE`, automationID).Scan(&revisionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrAutomationNotFound
	}
	if err != nil {
		return "", false, err
	}
	var manifestJSON []byte
	err = tx.QueryRow(ctx, `SELECT manifest FROM revisions WHERE id = $1`, revisionID).Scan(&manifestJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrRevisionNotFound
	}
	if err != nil {
		return "", false, err
	}
	if externalID != "" {
		var existingRunID string
		err := tx.QueryRow(ctx, `
			SELECT r.id FROM runs r JOIN events e ON e.id = r.event_id
			WHERE e.trigger_key = $1 AND e.external_id = $2`, automationID+":manual", externalID).Scan(&existingRunID)
		if err == nil {
			if err := tx.Commit(ctx); err != nil {
				return "", false, err
			}
			return existingRunID, false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", false, err
		}
	}
	if expectedRevisionID != "" && revisionID != expectedRevisionID {
		return "", false, ErrAutomationRevisionChanged
	}
	if externalID == "" {
		externalID = newID("external")
	}
	eventID := newID("evt")
	runID := newID("run")
	now := time.Now().UTC()
	envelope := domain.EventEnvelope{
		ID:         eventID,
		OccurredAt: now,
		ReceivedAt: now,
		Trigger: domain.EventTrigger{
			Automation: automationID,
			ID:         "manual",
			Type:       "manual",
		},
		Data:     data,
		Metadata: map[string]any{"source": "manual", "actor": actor},
	}
	envelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		return "", false, err
	}
	triggerKey := automationID + ":manual"
	command, err := tx.Exec(ctx, `
		INSERT INTO events (id, trigger_key, external_id, envelope, occurred_at, received_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (trigger_key, external_id) DO NOTHING`,
		eventID, triggerKey, externalID, envelopeJSON, now, now)
	if err != nil {
		return "", false, err
	}
	if command.RowsAffected() == 0 {
		var existingRunID string
		err := tx.QueryRow(ctx, `
			SELECT r.id FROM runs r JOIN events e ON e.id = r.event_id
			WHERE e.trigger_key = $1 AND e.external_id = $2`, triggerKey, externalID).Scan(&existingRunID)
		if err != nil {
			return "", false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return "", false, err
		}
		return existingRunID, false, nil
	}
	var value domain.Manifest
	if err := json.Unmarshal(manifestJSON, &value); err != nil {
		return "", false, err
	}
	policy := value.Execution.Concurrency
	if policy == "" {
		policy = "allow"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO runs (id, automation_id, revision_id, event_id, status, max_attempts, concurrency_policy)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		runID, automationID, revisionID, eventID, domain.RunQueued, value.Execution.Retries+1, policy); err != nil {
		return "", false, err
	}
	if err := insertAuditEvent(ctx, tx, "run.queued_manually", automationID, actor, map[string]any{
		"runId": runID, "externalId": externalID,
	}); err != nil {
		return "", false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, err
	}
	return runID, true, nil
}

func (s *Store) CreateDeployment(ctx context.Context, deploymentID, idempotencyKey, packageDigest, sourcePath, actor string) (domain.Deployment, bool, error) {
	command, err := s.pool.Exec(ctx, `
		INSERT INTO deployments (id, idempotency_key, package_digest, source_path, status, actor)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (idempotency_key) DO NOTHING`,
		deploymentID, idempotencyKey, packageDigest, sourcePath, domain.DeploymentQueued, actor)
	if err != nil {
		return domain.Deployment{}, false, err
	}
	value, err := s.getDeploymentByIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		return domain.Deployment{}, false, err
	}
	if value.PackageDigest != packageDigest {
		return domain.Deployment{}, false, ErrDeploymentIdempotencyConflict
	}
	return value, command.RowsAffected() == 1, nil
}

func (s *Store) ListDeploymentsFiltered(ctx context.Context, automationID, status string, limit int) ([]domain.Deployment, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, status, automation_id, package_digest, content_hash,
			COALESCE(revision_id, ''), COALESCE(retry_of, ''), actor, error, provenance,
			created_at, updated_at, started_at, finished_at, cancel_requested_at
		FROM deployments
		WHERE ($1 = '' OR automation_id = $1) AND ($2 = '' OR status = $2)
		ORDER BY created_at DESC, id DESC LIMIT $3`, automationID, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.Deployment, 0)
	for rows.Next() {
		var value domain.Deployment
		if err := scanDeployment(rows, &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) ListDeploymentsPage(ctx context.Context, automationID, status string, cursor ListCursor, limit int) ([]domain.Deployment, error) {
	if limit <= 0 || limit > 501 {
		limit = 101
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, status, automation_id, package_digest, content_hash,
			COALESCE(revision_id, ''), COALESCE(retry_of, ''), actor, error, provenance,
			created_at, updated_at, started_at, finished_at, cancel_requested_at
		FROM deployments
		WHERE ($1 = '' OR automation_id = $1) AND ($2 = '' OR status = $2)
			AND ($3 = '' OR (created_at, id) < ($4, $3))
		ORDER BY created_at DESC, id DESC LIMIT $5`, automationID, status, cursor.ID, cursor.CreatedAt, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.Deployment, 0)
	for rows.Next() {
		var value domain.Deployment
		if err := scanDeployment(rows, &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) GetDeployment(ctx context.Context, deploymentID string) (domain.Deployment, error) {
	var value domain.Deployment
	err := scanDeployment(s.pool.QueryRow(ctx, `
		SELECT id, status, automation_id, package_digest, content_hash,
			COALESCE(revision_id, ''), COALESCE(retry_of, ''), actor, error, provenance,
			created_at, updated_at, started_at, finished_at, cancel_requested_at
		FROM deployments WHERE id = $1`, deploymentID), &value)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Deployment{}, ErrDeploymentNotFound
	}
	if err != nil {
		return domain.Deployment{}, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, kind, status, logs, error, started_at, finished_at
		FROM deployment_steps WHERE deployment_id = $1 ORDER BY position`, deploymentID)
	if err != nil {
		return domain.Deployment{}, err
	}
	defer rows.Close()
	value.Steps = make([]domain.DeploymentStep, 0)
	for rows.Next() {
		var step domain.DeploymentStep
		if err := rows.Scan(&step.ID, &step.Kind, &step.Status, &step.Logs, &step.Error, &step.StartedAt, &step.FinishedAt); err != nil {
			return domain.Deployment{}, err
		}
		value.Steps = append(value.Steps, step)
	}
	return value, rows.Err()
}

func (s *Store) getDeploymentByIdempotencyKey(ctx context.Context, idempotencyKey string) (domain.Deployment, error) {
	var value domain.Deployment
	err := scanDeployment(s.pool.QueryRow(ctx, `
		SELECT id, status, automation_id, package_digest, content_hash,
			COALESCE(revision_id, ''), COALESCE(retry_of, ''), actor, error, provenance,
			created_at, updated_at, started_at, finished_at, cancel_requested_at
		FROM deployments WHERE idempotency_key = $1`, idempotencyKey), &value)
	return value, err
}

func (s *Store) AcquireDeployment(ctx context.Context, workerID string, leaseDuration time.Duration) (*domain.RunnableDeployment, error) {
	var value domain.RunnableDeployment
	var provenanceJSON []byte
	err := s.pool.QueryRow(ctx, `
		WITH candidate AS (
			SELECT id FROM deployments
			WHERE cancel_requested_at IS NULL AND (status = $3 OR (
				status IN ($4, $5, $6, $7) AND lease_expires_at < now()
			))
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		), claimed AS (
			UPDATE deployments d
			SET status = $4, started_at = COALESCE(started_at, now()), updated_at = now(),
				lease_owner = $1, lease_expires_at = now() + $2::interval, error = ''
			FROM candidate WHERE d.id = candidate.id
			RETURNING d.*
		)
		SELECT id, status, automation_id, package_digest, content_hash,
			COALESCE(revision_id, ''), COALESCE(retry_of, ''), actor, error, provenance,
			created_at, updated_at, started_at, finished_at, cancel_requested_at,
			source_path
		FROM claimed`, workerID, leaseDuration.String(), domain.DeploymentQueued,
		domain.DeploymentValidating, domain.DeploymentBuilding, domain.DeploymentChecking, domain.DeploymentActivating).Scan(
		&value.ID, &value.Status, &value.AutomationID, &value.PackageDigest, &value.ContentHash,
		&value.RevisionID, &value.RetryOf, &value.Actor, &value.Error, &provenanceJSON, &value.CreatedAt, &value.UpdatedAt,
		&value.StartedAt, &value.FinishedAt, &value.CancelRequestedAt, &value.SourcePath)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := setDeploymentProvenance(&value.Deployment, provenanceJSON); err != nil {
		return nil, err
	}
	return &value, nil
}

func (s *Store) RenewDeploymentLease(ctx context.Context, deploymentID, workerID string, leaseDuration time.Duration) (bool, error) {
	command, err := s.pool.Exec(ctx, `
		UPDATE deployments SET lease_expires_at = now() + $3::interval, updated_at = now()
		WHERE id = $1 AND lease_owner = $2 AND cancel_requested_at IS NULL AND status IN ($4, $5, $6, $7)`,
		deploymentID, workerID, leaseDuration.String(), domain.DeploymentValidating,
		domain.DeploymentBuilding, domain.DeploymentChecking, domain.DeploymentActivating)
	if err != nil {
		return false, err
	}
	return command.RowsAffected() == 1, nil
}

func (s *Store) SetDeploymentStage(ctx context.Context, deploymentID, workerID, status, automationID, contentHash string) error {
	if status != domain.DeploymentBuilding && status != domain.DeploymentChecking && status != domain.DeploymentActivating {
		return fmt.Errorf("unsupported deployment stage %q", status)
	}
	command, err := s.pool.Exec(ctx, `
		UPDATE deployments
		SET status = $3, automation_id = $4, content_hash = $5, updated_at = now()
		WHERE id = $1 AND lease_owner = $2 AND cancel_requested_at IS NULL AND status IN ($6, $7, $8)`,
		deploymentID, workerID, status, automationID, contentHash,
		domain.DeploymentValidating, domain.DeploymentBuilding, domain.DeploymentChecking)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrDeploymentLeaseLost
	}
	return nil
}

// RecordDeploymentStep persists an ordered promotion stage. Starting a step
// advances the deployment status in the same transaction; finishing it retains
// bounded diagnostics for agents and operators.
func (s *Store) RecordDeploymentStep(ctx context.Context, deploymentID, workerID string, update domain.DeploymentStepUpdate) error {
	logs := truncateDiagnostic(update.Logs, "logs")
	message := truncateDiagnostic(update.Error, "error")
	if update.Status == domain.DeploymentStepRunning {
		stage, err := deploymentStatusForStep(update.Kind)
		if err != nil {
			return err
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		command, err := tx.Exec(ctx, `
			UPDATE deployments SET status = $3, updated_at = now()
			WHERE id = $1 AND lease_owner = $2 AND cancel_requested_at IS NULL
				AND status IN ($4, $5, $6, $7)`,
			deploymentID, workerID, stage, domain.DeploymentValidating, domain.DeploymentBuilding,
			domain.DeploymentChecking, domain.DeploymentActivating)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return deploymentLeaseOrCancellation(ctx, tx, deploymentID)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO deployment_steps (deployment_id, position, id, kind, status, logs, error, started_at, finished_at)
			VALUES ($1, COALESCE((SELECT max(position) + 1 FROM deployment_steps WHERE deployment_id = $1), 0),
				$2, $3, $4, '', '', now(), NULL)
			ON CONFLICT (deployment_id, id) DO UPDATE SET
				kind = EXCLUDED.kind, status = EXCLUDED.status, logs = '', error = '', started_at = now(), finished_at = NULL`,
			deploymentID, update.ID, update.Kind, domain.DeploymentStepRunning)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if update.Status != domain.DeploymentStepSucceeded && update.Status != domain.DeploymentStepFailed {
		return fmt.Errorf("unsupported deployment step status %q", update.Status)
	}
	command, err := s.pool.Exec(ctx, `
		UPDATE deployment_steps SET status = $4, logs = $5, error = $6, finished_at = now()
		WHERE deployment_id = $1 AND id = $2 AND status = $3
			AND EXISTS (
				SELECT 1 FROM deployments
				WHERE id = $1 AND lease_owner = $7
					AND status IN ($8, $9, $10, $11)
			)`, deploymentID, update.ID, domain.DeploymentStepRunning, update.Status, logs, message,
		workerID, domain.DeploymentValidating, domain.DeploymentBuilding,
		domain.DeploymentChecking, domain.DeploymentActivating)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrDeploymentLeaseLost
	}
	return nil
}

func deploymentStatusForStep(kind string) (string, error) {
	switch kind {
	case "validate":
		return domain.DeploymentValidating, nil
	case "build":
		return domain.DeploymentBuilding, nil
	case "check":
		return domain.DeploymentChecking, nil
	case "activate":
		return domain.DeploymentActivating, nil
	default:
		return "", fmt.Errorf("unsupported deployment step kind %q", kind)
	}
}

func deploymentLeaseOrCancellation(ctx context.Context, tx pgx.Tx, deploymentID string) error {
	var requested bool
	err := tx.QueryRow(ctx, `SELECT cancel_requested_at IS NOT NULL FROM deployments WHERE id = $1`, deploymentID).Scan(&requested)
	if err == nil && requested {
		return ErrDeploymentCancelled
	}
	return ErrDeploymentLeaseLost
}

func truncateDiagnostic(value, label string) string {
	const limit = 1 << 20
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "\n[" + label + " truncated]"
}

func (s *Store) DeploymentCancellationRequested(ctx context.Context, deploymentID, workerID string) (bool, error) {
	var owner string
	var requested bool
	var status string
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(lease_owner, ''), cancel_requested_at IS NOT NULL, status
		FROM deployments WHERE id = $1`, deploymentID).Scan(&owner, &requested, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrDeploymentLeaseLost
	}
	if err != nil {
		return false, err
	}
	if status == domain.DeploymentSucceeded || status == domain.DeploymentFailed || status == domain.DeploymentCancelled {
		return false, nil
	}
	if owner != workerID {
		return false, ErrDeploymentLeaseLost
	}
	return requested, nil
}

func (s *Store) RequestDeploymentCancellation(ctx context.Context, deploymentID, actor string) (domain.Deployment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Deployment{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var automationID string
	err = tx.QueryRow(ctx, `
		UPDATE deployments
		SET cancel_requested_at = COALESCE(cancel_requested_at, now()),
			status = CASE WHEN status = $2 THEN $3 ELSE status END,
			finished_at = CASE WHEN status = $2 THEN now() ELSE finished_at END,
			updated_at = now()
		WHERE id = $1 AND status IN ($2, $4, $5, $6, $7)
		RETURNING automation_id`,
		deploymentID, domain.DeploymentQueued, domain.DeploymentCancelled,
		domain.DeploymentValidating, domain.DeploymentBuilding, domain.DeploymentChecking, domain.DeploymentActivating).Scan(&automationID)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := s.GetDeployment(ctx, deploymentID); errors.Is(err, ErrDeploymentNotFound) {
			return domain.Deployment{}, err
		}
		return domain.Deployment{}, ErrDeploymentNotCancellable
	}
	if err != nil {
		return domain.Deployment{}, err
	}
	if err := insertAuditEvent(ctx, tx, "deployment.cancel_requested", automationID, actor, map[string]any{"deploymentId": deploymentID}); err != nil {
		return domain.Deployment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Deployment{}, err
	}
	return s.GetDeployment(ctx, deploymentID)
}

func (s *Store) CompleteDeploymentCancellation(ctx context.Context, deploymentID, workerID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `
		UPDATE deployment_steps SET status = $3, error = 'deployment cancelled', finished_at = now()
		WHERE deployment_id = $1 AND status = $2`,
		deploymentID, domain.DeploymentStepRunning, domain.DeploymentStepFailed); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `
		UPDATE deployments
		SET status = $3, error = 'deployment cancelled', updated_at = now(), finished_at = now(),
			lease_owner = NULL, lease_expires_at = NULL
		WHERE id = $1 AND lease_owner = $2 AND cancel_requested_at IS NOT NULL
			AND status IN ($4, $5, $6, $7)`,
		deploymentID, workerID, domain.DeploymentCancelled, domain.DeploymentValidating,
		domain.DeploymentBuilding, domain.DeploymentChecking, domain.DeploymentActivating)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrDeploymentLeaseLost
	}
	return tx.Commit(ctx)
}

func (s *Store) RetryDeployment(ctx context.Context, originalID, deploymentID, idempotencyKey, actor string) (domain.Deployment, bool, error) {
	if existing, err := s.getDeploymentByIdempotencyKey(ctx, idempotencyKey); err == nil {
		if existing.RetryOf != originalID {
			return domain.Deployment{}, false, ErrDeploymentIdempotencyConflict
		}
		return existing, false, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Deployment{}, false, err
	}
	var originalStatus, sourcePath string
	if err := s.pool.QueryRow(ctx, `SELECT status, source_path FROM deployments WHERE id = $1`, originalID).Scan(&originalStatus, &sourcePath); errors.Is(err, pgx.ErrNoRows) {
		return domain.Deployment{}, false, ErrDeploymentNotFound
	} else if err != nil {
		return domain.Deployment{}, false, err
	}
	if originalStatus != domain.DeploymentFailed && originalStatus != domain.DeploymentCancelled {
		return domain.Deployment{}, false, ErrDeploymentNotRetryable
	}
	if info, err := os.Stat(sourcePath); err != nil || !info.IsDir() {
		return domain.Deployment{}, false, ErrDeploymentSourceUnavailable
	}
	command, err := s.pool.Exec(ctx, `
		INSERT INTO deployments (id, idempotency_key, package_digest, source_path, status, actor, retry_of)
		SELECT $2, $3, package_digest, source_path, $4, $5, id
		FROM deployments
		WHERE id = $1 AND status IN ($6, $7) AND source_path <> ''
		ON CONFLICT (idempotency_key) DO NOTHING`,
		originalID, deploymentID, idempotencyKey, domain.DeploymentQueued, actor,
		domain.DeploymentFailed, domain.DeploymentCancelled)
	if err != nil {
		return domain.Deployment{}, false, err
	}
	value, err := s.getDeploymentByIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		return domain.Deployment{}, false, err
	}
	if value.RetryOf != originalID {
		return domain.Deployment{}, false, ErrDeploymentIdempotencyConflict
	}
	return value, command.RowsAffected() == 1, nil
}

func (s *Store) FailDeployment(ctx context.Context, deploymentID, workerID string, deploymentErr error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	message := deploymentErr.Error()
	if len(message) > 1<<20 {
		message = message[:1<<20] + "\n[error truncated]"
	}
	var automationID, actor, packageDigest, contentHash string
	err = tx.QueryRow(ctx, `
		UPDATE deployments
		SET status = $3, error = $4, updated_at = now(), finished_at = now(),
			lease_owner = NULL, lease_expires_at = NULL
		WHERE id = $1 AND lease_owner = $2 AND cancel_requested_at IS NULL AND status IN ($5, $6, $7, $8)
		RETURNING automation_id, actor, package_digest, content_hash`,
		deploymentID, workerID, domain.DeploymentFailed, message,
		domain.DeploymentValidating, domain.DeploymentBuilding, domain.DeploymentChecking, domain.DeploymentActivating).
		Scan(&automationID, &actor, &packageDigest, &contentHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDeploymentLeaseLost
	}
	if err != nil {
		return err
	}
	if automationID != "" {
		if err := insertAuditEvent(ctx, tx, "deployment.failed", automationID, actor, map[string]any{
			"deploymentId":  deploymentID,
			"packageDigest": packageDigest,
			"contentHash":   contentHash,
			"error":         message,
		}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

type rowScanner interface {
	Scan(...any) error
}

func scanDeployment(row rowScanner, value *domain.Deployment) error {
	var provenanceJSON []byte
	if err := row.Scan(&value.ID, &value.Status, &value.AutomationID, &value.PackageDigest,
		&value.ContentHash, &value.RevisionID, &value.RetryOf, &value.Actor, &value.Error,
		&provenanceJSON, &value.CreatedAt, &value.UpdatedAt, &value.StartedAt, &value.FinishedAt,
		&value.CancelRequestedAt); err != nil {
		return err
	}
	return setDeploymentProvenance(value, provenanceJSON)
}

func setDeploymentProvenance(value *domain.Deployment, encoded []byte) error {
	var artifactProvenance domain.ArtifactProvenance
	if err := json.Unmarshal(encoded, &artifactProvenance); err != nil {
		return err
	}
	if artifactProvenance.ArtifactDigest != "" {
		value.Provenance = &artifactProvenance
	}
	return nil
}

func (s *Store) ListRuns(ctx context.Context, limit int) ([]domain.Run, error) {
	return s.ListRunsFiltered(ctx, "", "", limit)
}

func (s *Store) ListRunsFiltered(ctx context.Context, automationID, status string, limit int) ([]domain.Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, automation_id, revision_id, event_id, status, attempt, max_attempts,
			created_at, started_at, finished_at, logs, error, result
		FROM runs
		WHERE ($1 = '' OR automation_id = $1) AND ($2 = '' OR status = $2)
		ORDER BY created_at DESC LIMIT $3`, automationID, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.Run, 0)
	for rows.Next() {
		var item domain.Run
		if err := rows.Scan(&item.ID, &item.AutomationID, &item.RevisionID, &item.EventID,
			&item.Status, &item.Attempt, &item.MaxAttempts, &item.CreatedAt, &item.StartedAt,
			&item.FinishedAt, &item.Logs, &item.Error, &item.Result); err != nil {
			return nil, err
		}
		values = append(values, item)
	}
	return values, rows.Err()
}

func (s *Store) ListRunSummariesPage(ctx context.Context, automationID, status string, cursor ListCursor, limit int) ([]domain.RunSummary, error) {
	if limit <= 0 || limit > 501 {
		limit = 101
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, automation_id, revision_id, status, attempt, max_attempts,
			created_at, started_at, finished_at
		FROM runs
		WHERE ($1 = '' OR automation_id = $1) AND ($2 = '' OR status = $2)
			AND ($3 = '' OR (created_at, id) < ($4, $3))
		ORDER BY created_at DESC, id DESC LIMIT $5`, automationID, status, cursor.ID, cursor.CreatedAt, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.RunSummary, 0)
	for rows.Next() {
		var item domain.RunSummary
		if err := rows.Scan(&item.ID, &item.AutomationID, &item.RevisionID, &item.Status,
			&item.Attempt, &item.MaxAttempts, &item.CreatedAt, &item.StartedAt, &item.FinishedAt); err != nil {
			return nil, err
		}
		values = append(values, item)
	}
	return values, rows.Err()
}

func (s *Store) GetRun(ctx context.Context, runID string) (domain.Run, error) {
	var item domain.Run
	err := s.pool.QueryRow(ctx, `
		SELECT id, automation_id, revision_id, event_id, status, attempt, max_attempts,
			created_at, started_at, finished_at, logs, error, result
		FROM runs WHERE id = $1`, runID).Scan(
		&item.ID, &item.AutomationID, &item.RevisionID, &item.EventID, &item.Status,
		&item.Attempt, &item.MaxAttempts, &item.CreatedAt, &item.StartedAt,
		&item.FinishedAt, &item.Logs, &item.Error, &item.Result)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Run{}, ErrRunNotFound
	}
	return item, err
}

func (s *Store) ListAuditEvents(ctx context.Context, automationID string, limit int) ([]AuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, action, automation_id, actor, details, created_at
		FROM audit_events
		WHERE ($1 = '' OR automation_id = $1)
		ORDER BY created_at DESC, id DESC LIMIT $2`, automationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]AuditEvent, 0)
	for rows.Next() {
		var item AuditEvent
		if err := rows.Scan(&item.ID, &item.Action, &item.AutomationID, &item.Actor, &item.Details, &item.CreatedAt); err != nil {
			return nil, err
		}
		values = append(values, item)
	}
	return values, rows.Err()
}

func (s *Store) ListAuditEventsPage(ctx context.Context, filter AuditFilter, cursor ListCursor, limit int) ([]AuditEvent, error) {
	if limit <= 0 || limit > 501 {
		limit = 101
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, action, automation_id, actor, details, created_at
		FROM audit_events
		WHERE ($1 = '' OR automation_id = $1)
			AND ($2 = '' OR action = $2)
			AND ($3 = '' OR actor = $3)
			AND ($4::timestamptz IS NULL OR created_at >= $4)
			AND ($5::timestamptz IS NULL OR created_at <= $5)
			AND ($6 = '' OR (created_at, id) < ($7, $6))
		ORDER BY created_at DESC, id DESC LIMIT $8`, filter.AutomationID, filter.Action, filter.Actor,
		filter.Since, filter.Until, cursor.ID, cursor.CreatedAt, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]AuditEvent, 0)
	for rows.Next() {
		var item AuditEvent
		if err := rows.Scan(&item.ID, &item.Action, &item.AutomationID, &item.Actor, &item.Details, &item.CreatedAt); err != nil {
			return nil, err
		}
		values = append(values, item)
	}
	return values, rows.Err()
}

func (s *Store) ListNtfyTriggers(ctx context.Context) ([]domain.TriggerDefinition, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.automation_id, t.id, t.revision_id, t.type, t.config
		FROM triggers t
		JOIN automations a ON a.id = t.automation_id
		WHERE t.type = 'ntfy' AND t.enabled = true AND a.enabled = true
		ORDER BY t.automation_id, t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []domain.TriggerDefinition
	for rows.Next() {
		var item domain.TriggerDefinition
		if err := rows.Scan(&item.AutomationID, &item.TriggerID, &item.RevisionID, &item.Type, &item.Config); err != nil {
			return nil, err
		}
		values = append(values, item)
	}
	return values, rows.Err()
}

func nextSchedule(config map[string]any, after time.Time) (time.Time, error) {
	expression, _ := config["cron"].(string)
	timezone, _ := config["timezone"].(string)
	location := time.UTC
	if timezone != "" {
		loaded, err := time.LoadLocation(timezone)
		if err != nil {
			return time.Time{}, err
		}
		location = loaded
	}
	schedule, err := cron.ParseStandard(expression)
	if err != nil {
		return time.Time{}, err
	}
	return schedule.Next(after.In(location)), nil
}

func retryDelay(attempt int) time.Duration {
	delay := time.Second * time.Duration(1<<min(attempt-1, 8))
	return min(delay, 5*time.Minute)
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func insertAuditEvent(ctx context.Context, tx pgx.Tx, action, automationID, actor string, details map[string]any) error {
	if actor == "" {
		actor = "unknown"
	}
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO audit_events (id, action, automation_id, actor, details)
		VALUES ($1, $2, $3, $4, $5)`, newID("audit"), action, automationID, actor, detailsJSON)
	return err
}

func newID(prefix string) string {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(bytes)
}
