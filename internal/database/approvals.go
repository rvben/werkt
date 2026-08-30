package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rvben/werkt/internal/domain"
)

func (s *Store) ListApprovalsPage(ctx context.Context, automationID, status string, cursor ListCursor, limit int) ([]domain.Approval, error) {
	if limit <= 0 || limit > 501 {
		limit = 101
	}
	if _, err := s.pool.Exec(ctx, `UPDATE approvals SET status = 'expired' WHERE status = 'pending' AND expires_at <= now()`); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, automation_id, revision_id, requested_by_run_id, approval_key,
			status, title, description, fields, actions, expires_at, created_at,
			resolved_at, resolved_by, response, COALESCE(action_run_id, '')
		FROM approvals
		WHERE ($1 = '' OR automation_id = $1) AND ($2 = '' OR status = $2)
			AND ($3 = '' OR (created_at, id) < ($4, $3))
		ORDER BY created_at DESC, id DESC LIMIT $5`, automationID, status, cursor.ID, cursor.CreatedAt, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.Approval, 0)
	for rows.Next() {
		var value domain.Approval
		if err := scanApproval(rows, &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) GetApproval(ctx context.Context, approvalID string) (domain.Approval, error) {
	_, _ = s.pool.Exec(ctx, `UPDATE approvals SET status = 'expired' WHERE id = $1 AND status = 'pending' AND expires_at <= now()`, approvalID)
	var value domain.Approval
	err := scanApproval(s.pool.QueryRow(ctx, `
		SELECT id, automation_id, revision_id, requested_by_run_id, approval_key,
			status, title, description, fields, actions, expires_at, created_at,
			resolved_at, resolved_by, response, COALESCE(action_run_id, '')
		FROM approvals WHERE id = $1`, approvalID), &value)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Approval{}, ErrApprovalNotFound
	}
	return value, err
}

func scanApproval(row pgx.Row, value *domain.Approval) error {
	var fields, actions []byte
	if err := row.Scan(&value.ID, &value.AutomationID, &value.RevisionID,
		&value.RequestedByRun, &value.Key, &value.Status, &value.Title,
		&value.Description, &fields, &actions, &value.ExpiresAt, &value.CreatedAt,
		&value.ResolvedAt, &value.ResolvedBy, &value.Response, &value.ActionRunID); err != nil {
		return err
	}
	if err := json.Unmarshal(fields, &value.Fields); err != nil {
		return err
	}
	return json.Unmarshal(actions, &value.Actions)
}

func (s *Store) ResolveApproval(ctx context.Context, approvalID, idempotencyKey, expectedRevision, action string, fields map[string]any, actor string) (domain.Approval, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Approval{}, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var approval domain.Approval
	err = scanApproval(tx.QueryRow(ctx, `
		SELECT id, automation_id, revision_id, requested_by_run_id, approval_key,
			status, title, description, fields, actions, expires_at, created_at,
			resolved_at, resolved_by, response, COALESCE(action_run_id, '')
		FROM approvals WHERE id = $1 FOR UPDATE`, approvalID), &approval)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Approval{}, false, ErrApprovalNotFound
	}
	if err != nil {
		return domain.Approval{}, false, err
	}
	triggerKey := approval.AutomationID + ":approval:" + approval.ID
	if approval.Status != "pending" {
		responseFields, validationErr := validateApprovalResponse(approval, action, fields)
		var recorded struct {
			Action string         `json:"action"`
			Fields map[string]any `json:"fields"`
		}
		if validationErr != nil || json.Unmarshal(approval.Response, &recorded) != nil || recorded.Action != action || !reflect.DeepEqual(recorded.Fields, responseFields) {
			return domain.Approval{}, false, ErrApprovalResolved
		}
		var existingRunID string
		replayErr := tx.QueryRow(ctx, `SELECT r.id FROM runs r JOIN events e ON e.id = r.event_id WHERE e.trigger_key = $1 AND e.external_id = $2`, triggerKey, idempotencyKey).Scan(&existingRunID)
		if replayErr == nil && existingRunID == approval.ActionRunID {
			return approval, false, tx.Commit(ctx)
		}
		return domain.Approval{}, false, ErrApprovalResolved
	}
	if !approval.ExpiresAt.After(time.Now()) {
		_, _ = tx.Exec(ctx, `UPDATE approvals SET status = 'expired' WHERE id = $1`, approvalID)
		if err := tx.Commit(ctx); err != nil {
			return domain.Approval{}, false, err
		}
		return domain.Approval{}, false, ErrApprovalExpired
	}
	if expectedRevision == "" || expectedRevision != approval.RevisionID {
		return domain.Approval{}, false, ErrAutomationRevisionChanged
	}
	var activeRevision, manifestJSON string
	err = tx.QueryRow(ctx, `SELECT a.active_revision_id, r.manifest::text FROM automations a JOIN revisions r ON r.id = a.active_revision_id WHERE a.id = $1 FOR UPDATE OF a`, approval.AutomationID).Scan(&activeRevision, &manifestJSON)
	if err != nil {
		return domain.Approval{}, false, err
	}
	if activeRevision != approval.RevisionID {
		return domain.Approval{}, false, ErrAutomationRevisionChanged
	}
	responseFields, err := validateApprovalResponse(approval, action, fields)
	if err != nil {
		return domain.Approval{}, false, err
	}
	response := map[string]any{"approvalId": approval.ID, "key": approval.Key, "action": action, "fields": responseFields}
	data, err := json.Marshal(map[string]any{"approval": response})
	if err != nil {
		return domain.Approval{}, false, err
	}
	now := time.Now().UTC()
	eventID, runID := newID("evt"), newID("run")
	envelope := domain.EventEnvelope{
		ID: eventID, OccurredAt: now, ReceivedAt: now,
		Trigger: domain.EventTrigger{Automation: approval.AutomationID, ID: "approval", Type: "approval"},
		Data:    data, Metadata: map[string]any{"source": "approval", "actor": actor, "approvalId": approval.ID},
	}
	envelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		return domain.Approval{}, false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO events (id, trigger_key, external_id, envelope, occurred_at, received_at) VALUES ($1,$2,$3,$4,$5,$5)`, eventID, triggerKey, idempotencyKey, envelopeJSON, now); err != nil {
		return domain.Approval{}, false, err
	}
	var manifest domain.Manifest
	if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
		return domain.Approval{}, false, err
	}
	policy := manifest.Execution.Concurrency
	if policy == "" {
		policy = "allow"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO runs (id,automation_id,revision_id,event_id,status,max_attempts,concurrency_policy) VALUES ($1,$2,$3,$4,$5,$6,$7)`, runID, approval.AutomationID, approval.RevisionID, eventID, domain.RunQueued, manifest.Execution.Retries+1, policy); err != nil {
		return domain.Approval{}, false, err
	}
	responseJSON, _ := json.Marshal(response)
	status := map[string]string{"approve": "approved", "reject": "rejected"}[action]
	if _, err := tx.Exec(ctx, `UPDATE approvals SET status=$2,resolved_at=$3,resolved_by=$4,response=$5,action_run_id=$6 WHERE id=$1`, approval.ID, status, now, actor, responseJSON, runID); err != nil {
		return domain.Approval{}, false, err
	}
	if err := insertAuditEvent(ctx, tx, "approval."+status, approval.AutomationID, actor, map[string]any{"approvalId": approval.ID, "runId": runID, "action": action}); err != nil {
		return domain.Approval{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Approval{}, false, err
	}
	resolved, err := s.GetApproval(ctx, approval.ID)
	return resolved, true, err
}

func validateApprovalResponse(approval domain.Approval, action string, supplied map[string]any) (map[string]any, error) {
	var selected *domain.ApprovalAction
	for index := range approval.Actions {
		if approval.Actions[index].ID == action {
			selected = &approval.Actions[index]
			break
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("%w: action is not allowed", ErrApprovalInvalidResponse)
	}
	declarations := make(map[string]domain.ApprovalField, len(approval.Fields))
	for _, field := range approval.Fields {
		declarations[field.ID] = field
	}
	for key := range supplied {
		if _, exists := declarations[key]; !exists {
			return nil, fmt.Errorf("%w: field %q is not declared", ErrApprovalInvalidResponse, key)
		}
	}
	result := make(map[string]any, len(declarations))
	for id, declaration := range declarations {
		value, exists := supplied[id]
		if !exists {
			value = declaration.Value
		}
		if selected.RequiresFields && declaration.Required && (value == nil || strings.TrimSpace(fmt.Sprint(value)) == "") {
			return nil, fmt.Errorf("%w: field %q is required", ErrApprovalInvalidResponse, id)
		}
		if value == nil {
			continue
		}
		switch declaration.Type {
		case "text", "textarea":
			text, ok := value.(string)
			if !ok || len([]rune(text)) > 4000 {
				return nil, fmt.Errorf("%w: field %q must be text of at most 4000 characters", ErrApprovalInvalidResponse, id)
			}
			result[id] = strings.TrimSpace(text)
		case "number":
			if _, ok := value.(float64); !ok {
				return nil, fmt.Errorf("%w: field %q must be a number", ErrApprovalInvalidResponse, id)
			}
			result[id] = value
		case "boolean":
			if _, ok := value.(bool); !ok {
				return nil, fmt.Errorf("%w: field %q must be a boolean", ErrApprovalInvalidResponse, id)
			}
			result[id] = value
		case "select":
			text, ok := value.(string)
			if !ok || !containsString(declaration.Options, text) {
				return nil, fmt.Errorf("%w: field %q must use a declared option", ErrApprovalInvalidResponse, id)
			}
			result[id] = text
		}
	}
	return result, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
