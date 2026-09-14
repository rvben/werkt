package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rvben/werkt/internal/domain"
)

type DeploymentSourceRecord struct {
	ID           string
	Status       string
	AutomationID string
	Path         string
	CreatedAt    time.Time
}

type RevisionArtifactRecord struct {
	ID                     string
	AutomationID           string
	Path                   string
	CreatedAt              time.Time
	Active                 bool
	ReferencedByRun        bool
	ReferencedByDeployment bool
}

func (s *Store) ListDeploymentSourceRecords(ctx context.Context) ([]DeploymentSourceRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, status, automation_id, source_path, created_at
		FROM deployments WHERE source_path <> '' ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []DeploymentSourceRecord
	for rows.Next() {
		var value DeploymentSourceRecord
		if err := rows.Scan(&value.ID, &value.Status, &value.AutomationID, &value.Path, &value.CreatedAt); err != nil {
			return nil, err
		}
		if value.Path, err = s.resolveStorageLocation(value.Path); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) ListRevisionArtifactRecords(ctx context.Context) ([]RevisionArtifactRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.automation_id, r.artifact_path, r.created_at,
			a.active_revision_id = r.id,
			EXISTS (SELECT 1 FROM runs run WHERE run.revision_id = r.id AND run.status IN ($1, $2)),
			EXISTS (
				SELECT 1 FROM deployments deployment
				WHERE deployment.automation_id = r.automation_id
					AND deployment.content_hash = r.content_hash
					AND deployment.status IN ($3, $4, $5, $6)
			)
		FROM revisions r
		JOIN automations a ON a.id = r.automation_id
		WHERE r.artifact_path <> ''
		ORDER BY r.created_at DESC, r.id DESC`, domain.RunQueued, domain.RunRunning,
		domain.DeploymentValidating, domain.DeploymentBuilding, domain.DeploymentChecking, domain.DeploymentActivating)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []RevisionArtifactRecord
	for rows.Next() {
		var value RevisionArtifactRecord
		if err := rows.Scan(&value.ID, &value.AutomationID, &value.Path, &value.CreatedAt, &value.Active,
			&value.ReferencedByRun, &value.ReferencedByDeployment); err != nil {
			return nil, err
		}
		if value.Path, err = s.resolveStorageLocation(value.Path); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) CreateRetentionPlan(ctx context.Context, value domain.RetentionPlan) error {
	policyJSON, err := json.Marshal(value.Policy)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `
		INSERT INTO retention_plans (id, status, policy, actor, expires_at)
		VALUES ($1, $2, $3, $4, $5)`, value.ID, domain.RetentionPlanPlanned, policyJSON, value.Actor, value.ExpiresAt); err != nil {
		return err
	}
	for _, item := range value.Items {
		resourceIDs, err := json.Marshal(item.ResourceIDs)
		if err != nil {
			return err
		}
		automationIDs, err := json.Marshal(item.AutomationIDs)
		if err != nil {
			return err
		}
		recordedStoragePath, err := s.relativeStorageLocation(item.StoragePath)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO retention_items (
				plan_id, position, kind, storage_key, storage_path, resource_ids,
				automation_ids, reason, estimated_bytes, status
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			value.ID, item.Position, item.Kind, item.StorageKey, recordedStoragePath,
			resourceIDs, automationIDs, item.Reason, item.EstimatedBytes, domain.RetentionItemPlanned); err != nil {
			return err
		}
	}
	if err := insertAuditEvent(ctx, tx, "retention.planned", "", value.Actor, map[string]any{
		"planId": value.ID, "items": len(value.Items), "estimatedBytes": value.Summary.EstimatedBytes,
		"policy": value.Policy,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) GetRetentionPlan(ctx context.Context, planID string) (domain.RetentionPlan, error) {
	var value domain.RetentionPlan
	var policyJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT id, status, policy, actor, created_at, expires_at, applied_at
		FROM retention_plans WHERE id = $1`, planID).Scan(
		&value.ID, &value.Status, &policyJSON, &value.Actor, &value.CreatedAt, &value.ExpiresAt, &value.AppliedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RetentionPlan{}, ErrRetentionPlanNotFound
	}
	if err != nil {
		return domain.RetentionPlan{}, err
	}
	if err := json.Unmarshal(policyJSON, &value.Policy); err != nil {
		return domain.RetentionPlan{}, fmt.Errorf("decode retention policy: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT position, kind, storage_key, storage_path, resource_ids, automation_ids,
			reason, estimated_bytes, status, error, created_at, deleted_at
		FROM retention_items WHERE plan_id = $1 ORDER BY position`, planID)
	if err != nil {
		return domain.RetentionPlan{}, err
	}
	defer rows.Close()
	value.Items = make([]domain.RetentionItem, 0)
	for rows.Next() {
		var item domain.RetentionItem
		var resourceIDs, automationIDs []byte
		if err := rows.Scan(&item.Position, &item.Kind, &item.StorageKey, &item.StoragePath,
			&resourceIDs, &automationIDs, &item.Reason, &item.EstimatedBytes, &item.Status,
			&item.Error, &item.CreatedAt, &item.DeletedAt); err != nil {
			return domain.RetentionPlan{}, err
		}
		if item.StoragePath, err = s.resolveStorageLocation(item.StoragePath); err != nil {
			return domain.RetentionPlan{}, err
		}
		if err := json.Unmarshal(resourceIDs, &item.ResourceIDs); err != nil {
			return domain.RetentionPlan{}, err
		}
		if err := json.Unmarshal(automationIDs, &item.AutomationIDs); err != nil {
			return domain.RetentionPlan{}, err
		}
		value.Items = append(value.Items, item)
		value.Summary.Items++
		value.Summary.EstimatedBytes += item.EstimatedBytes
		switch item.Status {
		case domain.RetentionItemDeleted:
			value.Summary.Deleted++
		case domain.RetentionItemSkipped:
			value.Summary.Skipped++
		case domain.RetentionItemFailed:
			value.Summary.Failed++
		}
	}
	return value, rows.Err()
}

func (s *Store) ClaimRetentionPlan(ctx context.Context, planID string, lease time.Duration) (bool, error) {
	command, err := s.pool.Exec(ctx, `
		UPDATE retention_plans
		SET status = $2, updated_at = now(), lease_expires_at = now() + $3::interval
		WHERE id = $1 AND expires_at > now() AND (
			status = $4 OR (status = $2 AND lease_expires_at < now())
		)`, planID, domain.RetentionPlanApplying, lease.String(), domain.RetentionPlanPlanned)
	if err != nil {
		return false, err
	}
	if command.RowsAffected() == 1 {
		return true, nil
	}
	var status string
	var expiresAt time.Time
	err = s.pool.QueryRow(ctx, `SELECT status, expires_at FROM retention_plans WHERE id = $1`, planID).Scan(&status, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrRetentionPlanNotFound
	}
	if err != nil {
		return false, err
	}
	if status == domain.RetentionPlanApplied {
		return false, nil
	}
	if !expiresAt.After(time.Now()) {
		return false, ErrRetentionPlanExpired
	}
	return false, ErrRetentionPlanBusy
}

func (s *Store) RecordRetentionItem(ctx context.Context, planID string, position int, status, message string) error {
	if status != domain.RetentionItemDeleted && status != domain.RetentionItemSkipped && status != domain.RetentionItemFailed {
		return fmt.Errorf("invalid retention item status %q", status)
	}
	if len(message) > 4096 {
		message = message[:4096]
	}
	command, err := s.pool.Exec(ctx, `
		UPDATE retention_items SET status = $3, error = $4,
			deleted_at = CASE WHEN $3 = $5 THEN now() ELSE NULL END
		WHERE plan_id = $1 AND position = $2 AND status = $6`,
		planID, position, status, message, domain.RetentionItemDeleted, domain.RetentionItemPlanned)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("retention item %d is no longer planned", position)
	}
	return nil
}

func (s *Store) CompleteRetentionPlan(ctx context.Context, planID, actor string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var items, deleted, skipped, failed int
	var estimatedBytes int64
	if err := tx.QueryRow(ctx, `
		SELECT count(*), COALESCE(sum(estimated_bytes), 0),
			count(*) FILTER (WHERE status = $2),
			count(*) FILTER (WHERE status = $3),
			count(*) FILTER (WHERE status = $4)
		FROM retention_items WHERE plan_id = $1`, planID, domain.RetentionItemDeleted,
		domain.RetentionItemSkipped, domain.RetentionItemFailed).
		Scan(&items, &estimatedBytes, &deleted, &skipped, &failed); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `
		UPDATE retention_plans SET status = $2, updated_at = now(), applied_at = now(), lease_expires_at = NULL
		WHERE id = $1 AND status = $3`, planID, domain.RetentionPlanApplied, domain.RetentionPlanApplying)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrRetentionPlanBusy
	}
	if err := insertAuditEvent(ctx, tx, "retention.applied", "", actor, map[string]any{
		"planId": planID, "items": items, "estimatedBytes": estimatedBytes,
		"deleted": deleted, "skipped": skipped, "failed": failed,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DetachRetentionItem clears all database references only when the storage is
// still unused. SQL rechecks the invariant at the mutation point, closing the
// race between plan creation and execution.
func (s *Store) DetachRetentionItem(ctx context.Context, kind, path string) (bool, error) {
	// Rows hold locations relative to the data directory, so the caller's
	// absolute path has to be put back into that form before it can match.
	path, err := s.relativeStorageLocation(path)
	if err != nil {
		return false, err
	}
	switch kind {
	case domain.RetentionKindSource:
		command, err := s.pool.Exec(ctx, `
			UPDATE deployments SET source_path = ''
			WHERE source_path = $1 AND NOT EXISTS (
				SELECT 1 FROM deployments protected
				WHERE protected.source_path = $1
					AND protected.status NOT IN ($2, $3, $4)
			)`, path, domain.DeploymentSucceeded, domain.DeploymentFailed, domain.DeploymentCancelled)
		if err != nil {
			return false, err
		}
		return command.RowsAffected() > 0, nil
	case domain.RetentionKindArtifact:
		var detached int
		err := s.pool.QueryRow(ctx, `
			WITH detached AS (
				UPDATE revisions target SET artifact_path = ''
				WHERE target.artifact_path = $1
					AND NOT EXISTS (
						SELECT 1 FROM revisions protected
						JOIN automations a ON a.id = protected.automation_id
						WHERE protected.artifact_path = $1 AND a.active_revision_id = protected.id
					)
					AND NOT EXISTS (
						SELECT 1 FROM runs run
						JOIN revisions protected ON protected.id = run.revision_id
						WHERE protected.artifact_path = $1 AND run.status IN ($2, $3)
					)
					AND NOT EXISTS (
						SELECT 1 FROM deployments deployment
						JOIN revisions protected ON protected.automation_id = deployment.automation_id
							AND protected.content_hash = deployment.content_hash
						WHERE protected.artifact_path = $1
							AND deployment.status IN ($4, $5, $6, $7)
					)
				RETURNING id
			), released AS (
				DELETE FROM revision_secret_references
				WHERE revision_id IN (SELECT id FROM detached)
			)
			SELECT count(*) FROM detached`, path, domain.RunQueued, domain.RunRunning, domain.DeploymentValidating,
			domain.DeploymentBuilding, domain.DeploymentChecking, domain.DeploymentActivating).Scan(&detached)
		if err != nil {
			return false, err
		}
		return detached > 0, nil
	default:
		return false, fmt.Errorf("unsupported retention kind %q", kind)
	}
}
