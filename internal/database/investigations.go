package database

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rvben/werkt/internal/domain"
)

var ErrInvestigationNotFound = errors.New("investigation not found")
var ErrInvestigationConflict = errors.New("investigation already exists, is owned, or changed; refresh before continuing")

const investigationColumns = `spec, state, version, created_at, updated_at`

func scanInvestigation(row pgx.Row) (domain.Investigation, error) {
	var value domain.Investigation
	var spec, state []byte
	if err := row.Scan(&spec, &state, &value.Version, &value.CreatedAt, &value.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return value, ErrInvestigationNotFound
		}
		return value, err
	}
	if err := json.Unmarshal(spec, &value.InvestigationSpec); err != nil {
		return value, err
	}
	err := json.Unmarshal(state, &value.State)
	return value, err
}

func investigationConflict(err error) error {
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23505" {
		return ErrInvestigationConflict
	}
	return err
}

func (s *Store) GetInvestigation(ctx context.Context, id string) (domain.Investigation, error) {
	return scanInvestigation(s.pool.QueryRow(ctx, `SELECT `+investigationColumns+` FROM investigations WHERE request_id = $1`, id))
}

func (s *Store) ListInvestigations(ctx context.Context, repositoryID string, issue int64, cursor ListCursor, limit int) ([]domain.Investigation, error) {
	if err := domain.ValidateInvestigationSubject(repositoryID, issue); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 501 {
		limit = 101
	}
	rows, err := s.pool.Query(ctx, `SELECT `+investigationColumns+` FROM investigations
		WHERE repository_id = $1 AND issue_number = $2 AND ($3 = '' OR (created_at, request_id) < ($4, $3))
		ORDER BY created_at DESC, request_id DESC LIMIT $5`, repositoryID, issue, cursor.ID, cursor.CreatedAt, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Investigation, 0)
	for rows.Next() {
		value, err := scanInvestigation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, value)
	}
	return items, rows.Err()
}

// ReserveInvestigation commits intent before any external submission. A replay
// returns created=false even when the reservation has not yet been submitted.
func (s *Store) ReserveInvestigation(ctx context.Context, spec domain.InvestigationSpec, actor string) (domain.Investigation, bool, error) {
	if err := spec.Validate(); err != nil {
		return domain.Investigation{}, false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Investigation{}, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return domain.Investigation{}, false, err
	}
	// A targeted conflict handles simultaneous replays; the partial unique index
	// independently prevents different request IDs reserving the same issue.
	tag, err := tx.Exec(ctx, `INSERT INTO investigations (request_id, repository_id, issue_number, spec, state)
		VALUES ($1, $2, $3, $4, '{"status":"reserved"}') ON CONFLICT (request_id) DO NOTHING`, spec.RequestID, spec.RepositoryID, spec.IssueNumber, specJSON)
	if err != nil {
		return domain.Investigation{}, false, investigationConflict(err)
	}
	value, err := scanInvestigation(tx.QueryRow(ctx, `SELECT `+investigationColumns+` FROM investigations WHERE request_id = $1`, spec.RequestID))
	if err != nil {
		return value, false, err
	}
	if value.InvestigationSpec != spec {
		return value, false, ErrInvestigationConflict
	}
	created := tag.RowsAffected() == 1
	if created {
		if err := recordInvestigationVersion(ctx, tx, value, actor, "investigation.reserved"); err != nil {
			return value, false, err
		}
	}
	return value, created, tx.Commit(ctx)
}

func (s *Store) UpdateInvestigation(ctx context.Context, id string, expectedVersion int64, state domain.InvestigationState, actor string) (domain.Investigation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Investigation{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	old, err := scanInvestigation(tx.QueryRow(ctx, `SELECT `+investigationColumns+` FROM investigations WHERE request_id = $1 FOR UPDATE`, id))
	if err != nil {
		return old, err
	}
	if old.Version != expectedVersion {
		return old, ErrInvestigationConflict
	}
	if err := state.ValidateTransition(old.State); err != nil {
		return old, err
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return old, err
	}
	value, err := scanInvestigation(tx.QueryRow(ctx, `UPDATE investigations SET state = $2, version = version + 1, updated_at = now()
		WHERE request_id = $1 RETURNING `+investigationColumns, id, encoded))
	if err != nil {
		return old, investigationConflict(err)
	}
	if err := recordInvestigationVersion(ctx, tx, value, actor, "investigation.updated"); err != nil {
		return old, err
	}
	return value, tx.Commit(ctx)
}

func recordInvestigationVersion(ctx context.Context, tx pgx.Tx, value domain.Investigation, actor, action string) error {
	state, err := json.Marshal(value.State)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO investigation_versions (request_id, version, state, actor) VALUES ($1, $2, $3, $4)`, value.RequestID, value.Version, state, actor); err != nil {
		return err
	}
	return insertAuditEvent(ctx, tx, action, "", actor, map[string]any{"requestId": value.RequestID, "version": value.Version, "status": value.State.Status})
}
