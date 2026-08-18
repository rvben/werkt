package database

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/robfig/cron/v3"
	"github.com/rvben/werkt/internal/domain"
)

var (
	ErrRunLeaseLost       = errors.New("run lease ownership was lost")
	ErrAutomationNotFound = errors.New("automation not found")
	ErrRunNotFound        = errors.New("run not found")
	ErrTriggerNotFound    = errors.New("enabled trigger not found")
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct {
	pool *pgxpool.Pool
}

type AutomationSummary struct {
	ID               string    `json:"id"`
	Project          string    `json:"project"`
	Folder           string    `json:"folder,omitempty"`
	Description      string    `json:"description,omitempty"`
	Labels           []string  `json:"labels"`
	Enabled          bool      `json:"enabled"`
	ActiveRevisionID string    `json:"activeRevisionId"`
	UpdatedAt        time.Time `json:"updatedAt"`
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
	ID          string    `json:"id"`
	ContentHash string    `json:"contentHash"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"createdAt"`
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

func (s *Store) Deploy(ctx context.Context, value domain.Manifest, contentHash, artifactPath string) (string, error) {
	manifestJSON, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode manifest: %w", err)
	}
	labelsJSON, err := json.Marshal(value.Metadata.Labels)
	if err != nil {
		return "", fmt.Errorf("encode labels: %w", err)
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
		INSERT INTO revisions (id, automation_id, content_hash, manifest, artifact_path)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (automation_id, content_hash) DO UPDATE SET artifact_path = EXCLUDED.artifact_path`,
		revisionID, value.Metadata.Name, contentHash, manifestJSON, artifactPath)
	if err != nil {
		return "", fmt.Errorf("insert revision: %w", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM triggers WHERE automation_id = $1`, value.Metadata.Name); err != nil {
		return "", fmt.Errorf("replace triggers: %w", err)
	}
	for _, trigger := range value.Triggers {
		configJSON := []byte("{}")
		if trigger.Config != nil {
			configJSON, err = json.Marshal(trigger.Config)
			if err != nil {
				return "", fmt.Errorf("encode trigger %s: %w", trigger.ID, err)
			}
		}
		var nextFireAt *time.Time
		if trigger.Type == "schedule" && trigger.IsEnabled() {
			next, err := nextSchedule(trigger.Config, time.Now())
			if err != nil {
				return "", fmt.Errorf("calculate schedule %s: %w", trigger.ID, err)
			}
			nextFireAt = &next
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO triggers (automation_id, id, revision_id, type, config, enabled, next_fire_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			value.Metadata.Name, trigger.ID, revisionID, trigger.Type, configJSON, trigger.IsEnabled(), nextFireAt)
		if err != nil {
			return "", fmt.Errorf("insert trigger %s: %w", trigger.ID, err)
		}
	}

	if _, err := tx.Exec(ctx, `UPDATE automations SET active_revision_id = $2, updated_at = now() WHERE id = $1`, value.Metadata.Name, revisionID); err != nil {
		return "", fmt.Errorf("activate revision: %w", err)
	}
	if err := insertAuditEvent(ctx, tx, "automation.deployed", value.Metadata.Name, "cli", map[string]any{
		"revisionId":  revisionID,
		"contentHash": contentHash,
	}); err != nil {
		return "", fmt.Errorf("audit deployment: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return revisionID, nil
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
	var manifestJSON []byte
	err := tx.QueryRow(ctx, `
		SELECT t.revision_id, t.type, r.manifest
		FROM triggers t
		JOIN automations a ON a.id = t.automation_id AND a.active_revision_id = t.revision_id
		JOIN revisions r ON r.id = t.revision_id
		WHERE t.automation_id = $1 AND t.id = $2 AND t.type = $3
			AND t.enabled = true AND a.enabled = true`, automationID, triggerID, expectedType).
		Scan(&revisionID, &triggerType, &manifestJSON)
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
	_, err = tx.Exec(ctx, `
		INSERT INTO runs (id, automation_id, revision_id, event_id, status, max_attempts, concurrency_policy)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		runID, automationID, revisionID, eventID, domain.RunQueued, value.Execution.Retries+1, policy)
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
	var manifestJSON, eventJSON []byte
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
			c.error, c.result, rev.artifact_path, rev.manifest, e.envelope
		FROM claimed c
		JOIN revisions rev ON rev.id = c.revision_id
		JOIN events e ON e.id = c.event_id`, workerID, leaseDuration.String()).Scan(
		&run.ID, &run.AutomationID, &run.RevisionID, &run.EventID, &run.Status,
		&run.Attempt, &run.MaxAttempts, &run.CreatedAt, &run.StartedAt, &run.FinishedAt,
		&run.Error, &run.Result, &run.ArtifactPath, &manifestJSON, &eventJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(manifestJSON, &run.Manifest); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(eventJSON, &run.Event); err != nil {
		return nil, err
	}
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

func (s *Store) CompleteRun(ctx context.Context, runID, workerID, logs string, result json.RawMessage) error {
	command, err := s.pool.Exec(ctx, `
		UPDATE runs SET status = 'succeeded', logs = $2, result = $3, error = '',
			finished_at = now(), lease_owner = NULL, lease_expires_at = NULL
		WHERE id = $1 AND status = 'running' AND lease_owner = $4`,
		runID, logs, nullableJSON(result), workerID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrRunLeaseLost
	}
	return nil
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
		SELECT id, project, folder, description, labels, enabled,
			COALESCE(active_revision_id, ''), updated_at
		FROM automations
		WHERE ($1 = '' OR project = $1)
			AND ($2 = '' OR folder = $2)
			AND ($3 = '' OR labels ? $3)
			AND ($4 = '' OR id ILIKE '%' || $4 || '%' OR description ILIKE '%' || $4 || '%')
			AND ($5::boolean IS NULL OR enabled = $5)
		ORDER BY project, folder, id`,
		filter.Project, filter.Folder, filter.Label, filter.Query, enabled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]AutomationSummary, 0)
	for rows.Next() {
		var item AutomationSummary
		var labelsJSON []byte
		if err := rows.Scan(&item.ID, &item.Project, &item.Folder, &item.Description,
			&labelsJSON, &item.Enabled, &item.ActiveRevisionID, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(labelsJSON, &item.Labels); err != nil {
			return nil, err
		}
		values = append(values, item)
	}
	return values, rows.Err()
}

func (s *Store) GetAutomation(ctx context.Context, automationID string) (AutomationDetail, error) {
	var value AutomationDetail
	var labelsJSON, manifestJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT a.id, a.project, a.folder, a.description, a.labels, a.enabled,
			a.active_revision_id, a.updated_at, r.manifest
		FROM automations a
		JOIN revisions r ON r.id = a.active_revision_id
		WHERE a.id = $1`, automationID).Scan(
		&value.ID, &value.Project, &value.Folder, &value.Description, &labelsJSON,
		&value.Enabled, &value.ActiveRevisionID, &value.UpdatedAt, &manifestJSON)
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
		SELECT id, content_hash, id = $2, created_at
		FROM revisions WHERE automation_id = $1 ORDER BY created_at DESC, id DESC`,
		automationID, value.ActiveRevisionID)
	if err != nil {
		return AutomationDetail{}, err
	}
	defer revisionRows.Close()
	value.Revisions = make([]RevisionSummary, 0)
	for revisionRows.Next() {
		var revision RevisionSummary
		if err := revisionRows.Scan(&revision.ID, &revision.ContentHash, &revision.Active, &revision.CreatedAt); err != nil {
			return AutomationDetail{}, err
		}
		value.Revisions = append(value.Revisions, revision)
	}
	return value, revisionRows.Err()
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

func (s *Store) EnqueueManualRun(ctx context.Context, automationID, externalID string, data json.RawMessage, actor string) (string, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var revisionID string
	var manifestJSON []byte
	err = tx.QueryRow(ctx, `
		SELECT a.active_revision_id, r.manifest
		FROM automations a
		JOIN revisions r ON r.id = a.active_revision_id
		WHERE a.id = $1`, automationID).Scan(&revisionID, &manifestJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrAutomationNotFound
	}
	if err != nil {
		return "", false, err
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
