package database

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

const notificationLeaseDuration = time.Minute

type NotificationEvent struct {
	ID           string
	Type         string
	AutomationID string
	SubjectID    string
	Payload      json.RawMessage
	CreatedAt    time.Time
}

type NotificationTarget struct {
	DestinationID string
	Provider      string
	Config        json.RawMessage
}

type NotificationDelivery struct {
	ID             string
	EventID        string
	EventType      string
	AutomationID   string
	SubjectID      string
	Payload        json.RawMessage
	EventCreatedAt time.Time
	DestinationID  string
	Provider       string
	Config         json.RawMessage
	Attempt        int
	MaxAttempts    int
}

func enqueueNotificationEvent(ctx context.Context, tx pgx.Tx, eventType, automationID, subjectID string, payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO notification_events (id, event_type, automation_id, subject_id, payload)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (event_type, subject_id) DO NOTHING`,
		newID("notification"), eventType, automationID, subjectID, encoded)
	return err
}

// AcquireNotificationEvent locks one unexpanded event. Expansion snapshots the
// currently configured routes into durable deliveries before the event is
// acknowledged, so restarts and later route changes cannot lose or redirect it.
func (s *Store) AcquireNotificationEvent(ctx context.Context) (*NotificationEvent, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var value NotificationEvent
	err = tx.QueryRow(ctx, `
		SELECT id, event_type, automation_id, subject_id, payload, created_at
		FROM notification_events
		WHERE status = 'pending'
		ORDER BY created_at, id
		FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(
		&value.ID, &value.Type, &value.AutomationID, &value.SubjectID, &value.Payload, &value.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	return &value, tx.Commit(ctx)
}

func (s *Store) ExpandNotificationEvent(ctx context.Context, eventID string, targets []NotificationTarget) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM notification_events WHERE id = $1 FOR UPDATE`, eventID).Scan(&status); err != nil {
		return err
	}
	if status != "pending" {
		return tx.Commit(ctx)
	}
	for _, target := range targets {
		if _, err := tx.Exec(ctx, `
			INSERT INTO notification_deliveries (id, event_id, destination_id, provider, provider_config)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (event_id, destination_id) DO NOTHING`,
			newID("delivery"), eventID, target.DestinationID, target.Provider, target.Config); err != nil {
			return err
		}
	}
	nextStatus := "expanded"
	if len(targets) == 0 {
		nextStatus = "skipped"
	}
	if _, err := tx.Exec(ctx, `UPDATE notification_events SET status = $2, expanded_at = now() WHERE id = $1`, eventID, nextStatus); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) AcquireNotificationDelivery(ctx context.Context, workerID string) (*NotificationDelivery, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var value NotificationDelivery
	err = tx.QueryRow(ctx, `
		SELECT d.id, d.event_id, e.event_type, e.automation_id, e.subject_id, e.payload, e.created_at,
			d.destination_id, d.provider, d.provider_config, d.attempt + 1, d.max_attempts
		FROM notification_deliveries d
		JOIN notification_events e ON e.id = d.event_id
		WHERE (d.status = 'queued' OR (d.status = 'running' AND d.lease_expires_at <= now()))
			AND d.available_at <= now()
		ORDER BY d.available_at, d.created_at, d.id
		FOR UPDATE OF d SKIP LOCKED LIMIT 1`).Scan(
		&value.ID, &value.EventID, &value.EventType, &value.AutomationID, &value.SubjectID,
		&value.Payload, &value.EventCreatedAt, &value.DestinationID, &value.Provider, &value.Config,
		&value.Attempt, &value.MaxAttempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, tx.Commit(ctx)
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE notification_deliveries
		SET status = 'running', attempt = attempt + 1, lease_owner = $2,
			lease_expires_at = now() + $3::interval
		WHERE id = $1`, value.ID, workerID, notificationLeaseDuration.String()); err != nil {
		return nil, err
	}
	return &value, tx.Commit(ctx)
}

func (s *Store) CompleteNotificationDelivery(ctx context.Context, delivery NotificationDelivery, workerID string, responseCode int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	command, err := tx.Exec(ctx, `
		UPDATE notification_deliveries
		SET status = 'succeeded', response_code = $3, delivered_at = now(),
			lease_owner = NULL, lease_expires_at = NULL, last_error = ''
		WHERE id = $1 AND status = 'running' AND lease_owner = $2`,
		delivery.ID, workerID, responseCode)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrNotificationLeaseLost
	}
	if err := insertAuditEvent(ctx, tx, "notification.delivered", delivery.AutomationID, "system:notifications", map[string]any{
		"deliveryId": delivery.ID, "destinationId": delivery.DestinationID,
		"eventType": delivery.EventType, "provider": delivery.Provider,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) FailNotificationDelivery(ctx context.Context, delivery NotificationDelivery, workerID, message string, retryable bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	status := "failed"
	availableAt := time.Now().UTC()
	if retryable && delivery.Attempt < delivery.MaxAttempts {
		status = "queued"
		availableAt = availableAt.Add(notificationRetryDelay(delivery.Attempt))
	}
	command, err := tx.Exec(ctx, `
		UPDATE notification_deliveries
		SET status = $3, available_at = $4, last_error = $5,
			lease_owner = NULL, lease_expires_at = NULL
		WHERE id = $1 AND status = 'running' AND lease_owner = $2`,
		delivery.ID, workerID, status, availableAt, message)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return ErrNotificationLeaseLost
	}
	if status == "failed" {
		if err := insertAuditEvent(ctx, tx, "notification.failed", delivery.AutomationID, "system:notifications", map[string]any{
			"deliveryId": delivery.ID, "destinationId": delivery.DestinationID,
			"eventType": delivery.EventType, "provider": delivery.Provider,
			"attempts": delivery.Attempt, "error": message,
		}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func notificationRetryDelay(attempt int) time.Duration {
	delay := time.Duration(1<<min(attempt-1, 6)) * 10 * time.Second
	return min(delay, 10*time.Minute)
}

// EnqueueExpiringApprovalNotifications atomically claims pending approvals
// whose warning window has opened and creates one deduplicated outbox event.
func (s *Store) EnqueueExpiringApprovalNotifications(ctx context.Context, horizon time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	rows, err := tx.Query(ctx, `
		SELECT id, automation_id, title, description, expires_at
		FROM approvals
		WHERE status = 'pending' AND expires_at > now() AND expires_at <= $1
			AND expiring_notified_at IS NULL
		ORDER BY expires_at, id
		FOR UPDATE SKIP LOCKED LIMIT $2`, horizon, limit)
	if err != nil {
		return 0, err
	}
	type approval struct {
		id, automationID, title, description string
		expiresAt                            time.Time
	}
	values := make([]approval, 0, limit)
	for rows.Next() {
		var value approval
		if err := rows.Scan(&value.id, &value.automationID, &value.title, &value.description, &value.expiresAt); err != nil {
			rows.Close()
			return 0, err
		}
		values = append(values, value)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, value := range values {
		if err := enqueueNotificationEvent(ctx, tx, "approval.expiring", value.automationID, value.id, map[string]any{
			"approvalId": value.id, "title": value.title, "description": value.description,
			"expiresAt": value.expiresAt,
		}); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE approvals SET expiring_notified_at = now() WHERE id = $1`, value.id); err != nil {
			return 0, err
		}
	}
	return len(values), tx.Commit(ctx)
}
