package secretvault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/domain"
)

const (
	MinimumValueBytes       = 8
	MaximumValueBytes       = 64 << 10
	MaximumDescriptionBytes = 500
)

var (
	ErrUnavailable        = errors.New("secret vault is not configured")
	ErrInvalidKey         = errors.New("secret key must be base64-encoded 32 bytes")
	ErrInvalidName        = errors.New("secret name must be a lowercase hierarchical name of at most 128 characters")
	ErrInvalidValue       = errors.New("secret value must be valid UTF-8 without NUL bytes and contain 8 to 65536 bytes")
	ErrInvalidDescription = errors.New("secret description must be valid UTF-8 and at most 500 bytes")
	ErrKeyMismatch        = errors.New("secret was encrypted with a different master key")
	secretName            = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._/-][a-z0-9]+)*$`)
)

type Repository interface {
	ListSecrets(context.Context) ([]domain.SecretMetadata, error)
	GetEncryptedSecret(context.Context, string) (database.EncryptedSecret, error)
	PutEncryptedSecret(context.Context, string, string, []byte, string, string) (domain.SecretMetadata, bool, error)
	DeleteSecret(context.Context, string, string) error
}

type Vault struct {
	repository Repository
	aead       cipher.AEAD
	keyID      string
}

func New(repository Repository, encodedKey string) (*Vault, error) {
	key, err := decodeKey(encodedKey)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	fingerprint := hmac.New(sha256.New, key)
	_, _ = fingerprint.Write([]byte("werkt-secret-vault-key-id-v1"))
	return &Vault{repository: repository, aead: aead, keyID: hex.EncodeToString(fingerprint.Sum(nil)[:8])}, nil
}

func decodeKey(value string) ([]byte, error) {
	var key []byte
	var err error
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		key, err = encoding.DecodeString(strings.TrimSpace(value))
		if err == nil {
			break
		}
	}
	if err != nil || len(key) != 32 {
		return nil, ErrInvalidKey
	}
	return key, nil
}

func ValidateName(name string) error {
	if len(name) == 0 || len(name) > 128 || !secretName.MatchString(name) {
		return ErrInvalidName
	}
	return nil
}

func ValidateValue(value string) error {
	if len(value) < MinimumValueBytes || len(value) > MaximumValueBytes || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return ErrInvalidValue
	}
	return nil
}

func (v *Vault) List(ctx context.Context) ([]domain.SecretMetadata, error) {
	return v.repository.ListSecrets(ctx)
}

func (v *Vault) Get(ctx context.Context, name string) (domain.SecretMetadata, error) {
	if err := ValidateName(name); err != nil {
		return domain.SecretMetadata{}, err
	}
	value, err := v.repository.GetEncryptedSecret(ctx, name)
	return value.SecretMetadata, err
}

func (v *Vault) Put(ctx context.Context, name, value, description, actor string) (domain.SecretMetadata, bool, error) {
	if err := ValidateName(name); err != nil {
		return domain.SecretMetadata{}, false, err
	}
	if err := ValidateValue(value); err != nil {
		return domain.SecretMetadata{}, false, err
	}
	if len(description) > MaximumDescriptionBytes || !utf8.ValidString(description) {
		return domain.SecretMetadata{}, false, ErrInvalidDescription
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return domain.SecretMetadata{}, false, fmt.Errorf("generate secret nonce: %w", err)
	}
	sealed := v.aead.Seal(nil, nonce, []byte(value), additionalData(name))
	ciphertext := append(nonce, sealed...)
	return v.repository.PutEncryptedSecret(ctx, name, description, ciphertext, v.keyID, actor)
}

func (v *Vault) Delete(ctx context.Context, name, actor string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	return v.repository.DeleteSecret(ctx, name, actor)
}

// Resolve decrypts only the explicitly named values required by one boundary.
// The result is keyed by secret name and must never be logged or persisted.
func (v *Vault) Resolve(ctx context.Context, names []string) (map[string]string, error) {
	unique := make(map[string]struct{}, len(names))
	for _, name := range names {
		if err := ValidateName(name); err != nil {
			return nil, err
		}
		unique[name] = struct{}{}
	}
	ordered := make([]string, 0, len(unique))
	for name := range unique {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	resolved := make(map[string]string, len(ordered))
	for _, name := range ordered {
		value, err := v.repository.GetEncryptedSecret(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("resolve secret %q: %w", name, err)
		}
		if len(value.Ciphertext) < v.aead.NonceSize() {
			return nil, fmt.Errorf("resolve secret %q: invalid ciphertext", name)
		}
		nonce := value.Ciphertext[:v.aead.NonceSize()]
		plaintext, err := v.aead.Open(nil, nonce, value.Ciphertext[v.aead.NonceSize():], additionalData(name))
		if err != nil {
			return nil, fmt.Errorf("resolve secret %q: %w", name, ErrKeyMismatch)
		}
		resolved[name] = string(plaintext)
		for index := range plaintext {
			plaintext[index] = 0
		}
	}
	return resolved, nil
}

func additionalData(name string) []byte {
	return []byte("werkt-secret:v1:" + name)
}

type Disabled struct{}

func (Disabled) Resolve(context.Context, []string) (map[string]string, error) {
	return nil, ErrUnavailable
}
