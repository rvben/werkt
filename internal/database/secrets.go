package database

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/rvben/werkt/internal/domain"
)

type EncryptedSecret struct {
	domain.SecretMetadata
	Ciphertext []byte
	KeyID      string
}

func (s *Store) ValidateSecretReferences(ctx context.Context, references []domain.SecretReference) error {
	if len(references) == 0 {
		return nil
	}
	namesSet := make(map[string]struct{}, len(references))
	for _, reference := range references {
		namesSet[reference.Name] = struct{}{}
	}
	names := make([]string, 0, len(namesSet))
	for name := range namesSet {
		names = append(names, name)
	}
	sort.Strings(names)
	rows, err := s.pool.Query(ctx, `SELECT name FROM secrets WHERE name = ANY($1)`, names)
	if err != nil {
		return err
	}
	found := make(map[string]struct{}, len(names))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		found[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	missing := make([]string, 0)
	for _, name := range names {
		if _, ok := found[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrSecretNotFound, strings.Join(missing, ", "))
	}
	return nil
}

func (s *Store) ListSecrets(ctx context.Context) ([]domain.SecretMetadata, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT name, description, version, created_at, updated_at
		FROM secrets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]domain.SecretMetadata, 0)
	for rows.Next() {
		var value domain.SecretMetadata
		if err := rows.Scan(&value.Name, &value.Description, &value.Version, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) GetEncryptedSecret(ctx context.Context, name string) (EncryptedSecret, error) {
	var value EncryptedSecret
	err := s.pool.QueryRow(ctx, `
		SELECT name, description, version, created_at, updated_at, ciphertext, key_id
		FROM secrets WHERE name = $1`, name).Scan(
		&value.Name, &value.Description, &value.Version, &value.CreatedAt, &value.UpdatedAt,
		&value.Ciphertext, &value.KeyID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return EncryptedSecret{}, ErrSecretNotFound
	}
	return value, err
}

func (s *Store) PutEncryptedSecret(ctx context.Context, name, description string, ciphertext []byte, keyID, actor string) (domain.SecretMetadata, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.SecretMetadata{}, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var value domain.SecretMetadata
	var created bool
	err = tx.QueryRow(ctx, `
		INSERT INTO secrets (name, description, ciphertext, key_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (name) DO UPDATE SET
			description = EXCLUDED.description,
			ciphertext = EXCLUDED.ciphertext,
			key_id = EXCLUDED.key_id,
			version = secrets.version + 1,
			updated_at = now()
		RETURNING name, description, version, created_at, updated_at, (xmax = 0)`,
		name, description, ciphertext, keyID).Scan(
		&value.Name, &value.Description, &value.Version, &value.CreatedAt, &value.UpdatedAt, &created,
	)
	if err != nil {
		return domain.SecretMetadata{}, false, err
	}
	action := "secret.created"
	if !created {
		action = "secret.rotated"
	}
	if err := insertAuditEvent(ctx, tx, action, "", actor, map[string]any{
		"name": name, "version": value.Version,
	}); err != nil {
		return domain.SecretMetadata{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.SecretMetadata{}, false, err
	}
	return value, created, nil
}

func (s *Store) DeleteSecret(ctx context.Context, name, actor string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var version int64
	if err := tx.QueryRow(ctx, `SELECT version FROM secrets WHERE name = $1 FOR UPDATE`, name).Scan(&version); errors.Is(err, pgx.ErrNoRows) {
		return ErrSecretNotFound
	} else if err != nil {
		return err
	}
	var uses int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM revision_secret_references WHERE secret_name = $1`, name).Scan(&uses); err != nil {
		return err
	}
	if uses > 0 {
		return fmt.Errorf("%w: %s is referenced by %d retained revision bindings", ErrSecretInUse, name, uses)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM secrets WHERE name = $1`, name); err != nil {
		return err
	}
	if err := insertAuditEvent(ctx, tx, "secret.deleted", "", actor, map[string]any{
		"name": name, "version": version,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func replaceSecretReferences(ctx context.Context, tx pgx.Tx, revisionID string, references []domain.SecretReference) error {
	if _, err := tx.Exec(ctx, `DELETE FROM revision_secret_references WHERE revision_id = $1`, revisionID); err != nil {
		return fmt.Errorf("replace secret references: %w", err)
	}
	if len(references) == 0 {
		return nil
	}
	namesSet := make(map[string]struct{}, len(references))
	for _, reference := range references {
		namesSet[reference.Name] = struct{}{}
	}
	names := make([]string, 0, len(namesSet))
	for name := range namesSet {
		names = append(names, name)
	}
	sort.Strings(names)
	rows, err := tx.Query(ctx, `SELECT name FROM secrets WHERE name = ANY($1) FOR KEY SHARE`, names)
	if err != nil {
		return err
	}
	found := make(map[string]struct{}, len(names))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		found[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	missing := make([]string, 0)
	for _, name := range names {
		if _, ok := found[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrSecretNotFound, strings.Join(missing, ", "))
	}
	for _, reference := range references {
		if _, err := tx.Exec(ctx, `
			INSERT INTO revision_secret_references (revision_id, secret_name, purpose)
			VALUES ($1, $2, $3)`, revisionID, reference.Name, reference.Purpose); err != nil {
			return fmt.Errorf("record secret reference: %w", err)
		}
	}
	return nil
}
