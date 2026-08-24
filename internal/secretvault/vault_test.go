package secretvault

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/domain"
)

type memoryRepository struct {
	value database.EncryptedSecret
}

func (r *memoryRepository) ListSecrets(context.Context) ([]domain.SecretMetadata, error) {
	if r.value.Name == "" {
		return []domain.SecretMetadata{}, nil
	}
	return []domain.SecretMetadata{r.value.SecretMetadata}, nil
}

func (r *memoryRepository) GetEncryptedSecret(_ context.Context, name string) (database.EncryptedSecret, error) {
	if r.value.Name != name {
		return database.EncryptedSecret{}, database.ErrSecretNotFound
	}
	return r.value, nil
}

func (r *memoryRepository) PutEncryptedSecret(_ context.Context, name, description string, ciphertext []byte, keyID, _ string) (domain.SecretMetadata, bool, error) {
	created := r.value.Name == ""
	version := r.value.Version + 1
	if created {
		version = 1
	}
	now := time.Now().UTC()
	r.value = database.EncryptedSecret{SecretMetadata: domain.SecretMetadata{
		Name: name, Description: description, Version: version, CreatedAt: now, UpdatedAt: now,
	}, Ciphertext: append([]byte(nil), ciphertext...), KeyID: keyID}
	return r.value.SecretMetadata, created, nil
}

func (r *memoryRepository) DeleteSecret(context.Context, string, string) error { return nil }

func TestVaultEncryptsResolvesAndRotatesWithoutReturningValues(t *testing.T) {
	repository := &memoryRepository{}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	vault, err := New(repository, key)
	if err != nil {
		t.Fatal(err)
	}
	metadata, created, err := vault.Put(context.Background(), "ops/github/token", "first-secret-value", "GitHub token", "test")
	if err != nil || !created || metadata.Version != 1 {
		t.Fatalf("Put() metadata=%+v created=%v err=%v", metadata, created, err)
	}
	if string(repository.value.Ciphertext) == "first-secret-value" {
		t.Fatal("repository received plaintext")
	}
	values, err := vault.Resolve(context.Background(), []string{"ops/github/token", "ops/github/token"})
	if err != nil || values["ops/github/token"] != "first-secret-value" {
		t.Fatalf("Resolve() values=%v err=%v", values, err)
	}
	metadata, created, err = vault.Put(context.Background(), "ops/github/token", "second-secret-value", "GitHub token", "test")
	if err != nil || created || metadata.Version != 2 {
		t.Fatalf("rotate metadata=%+v created=%v err=%v", metadata, created, err)
	}
	values, err = vault.Resolve(context.Background(), []string{"ops/github/token"})
	if err != nil || values["ops/github/token"] != "second-secret-value" {
		t.Fatalf("Resolve() after rotate values=%v err=%v", values, err)
	}
	repository.value.KeyID = "legacy-key-fingerprint"
	values, err = vault.Resolve(context.Background(), []string{"ops/github/token"})
	if err != nil || values["ops/github/token"] != "second-secret-value" {
		t.Fatalf("Resolve() legacy key ID values=%v err=%v", values, err)
	}
}

func TestVaultRejectsWrongKeyAndInvalidInputs(t *testing.T) {
	repository := &memoryRepository{}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	vault, _ := New(repository, key)
	_, _, _ = vault.Put(context.Background(), "ops/token", "valid-secret-value", "", "test")
	otherKey := base64.StdEncoding.EncodeToString([]byte("abcdef0123456789abcdef0123456789"))
	other, _ := New(repository, otherKey)
	if _, err := other.Resolve(context.Background(), []string{"ops/token"}); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("Resolve() error=%v, want key mismatch", err)
	}
	if err := ValidateName("UPPER_CASE"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("ValidateName() error=%v", err)
	}
	if err := ValidateValue("short"); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("ValidateValue() error=%v", err)
	}
	if _, err := New(repository, "not-a-key"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("New() error=%v", err)
	}
}
