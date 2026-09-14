package service

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/domain"
	"github.com/rvben/werkt/internal/packageio"
	"github.com/rvben/werkt/internal/provenance"
)

var (
	ErrDeploymentDigestMismatch = errors.New("uploaded package does not match the expected SHA-256 digest")
	ErrInvalidDeploymentDigest  = errors.New("expected package digest must be 64 hexadecimal characters")
	ErrInvalidIdempotencyKey    = errors.New("Idempotency-Key is required and cannot exceed 255 printable characters")
)

type DeploymentIntake struct {
	store   *database.Store
	dataDir string
	limits  packageio.Limits
}

func NewDeploymentIntake(store *database.Store, dataDir string, limits packageio.Limits) *DeploymentIntake {
	return &DeploymentIntake{store: store, dataDir: dataDir, limits: limits}
}

func (i *DeploymentIntake) Accept(ctx context.Context, packageReader io.Reader, expectedDigest, idempotencyKey, actor string) (domain.Deployment, bool, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if !validIdempotencyKey(idempotencyKey) {
		return domain.Deployment{}, false, ErrInvalidIdempotencyKey
	}
	expectedDigest = strings.ToLower(strings.TrimSpace(expectedDigest))
	decoded, err := hex.DecodeString(expectedDigest)
	if err != nil || len(decoded) != 32 {
		return domain.Deployment{}, false, ErrInvalidDeploymentDigest
	}
	deploymentID, err := NewDeploymentID()
	if err != nil {
		return domain.Deployment{}, false, err
	}
	uploadsDirectory := filepath.Join(i.dataDir, "deployment-uploads")
	sourcesDirectory := filepath.Join(i.dataDir, "deployment-sources")
	if err := os.MkdirAll(uploadsDirectory, 0o750); err != nil {
		return domain.Deployment{}, false, fmt.Errorf("create deployment upload directory: %w", err)
	}
	if err := os.MkdirAll(sourcesDirectory, 0o750); err != nil {
		return domain.Deployment{}, false, fmt.Errorf("create deployment source directory: %w", err)
	}
	upload, err := os.CreateTemp(uploadsDirectory, ".incoming-*.tar.gz")
	if err != nil {
		return domain.Deployment{}, false, err
	}
	uploadPath := upload.Name()
	if err := upload.Close(); err != nil {
		_ = os.Remove(uploadPath)
		return domain.Deployment{}, false, err
	}
	if err := os.Remove(uploadPath); err != nil {
		return domain.Deployment{}, false, err
	}
	defer os.Remove(uploadPath) //nolint:errcheck

	packageDigest, err := packageio.Receive(packageReader, uploadPath, i.limits.CompressedBytes)
	if err != nil {
		return domain.Deployment{}, false, err
	}
	if packageDigest != expectedDigest {
		return domain.Deployment{}, false, ErrDeploymentDigestMismatch
	}
	temporarySource, err := os.MkdirTemp(sourcesDirectory, ".incoming-")
	if err != nil {
		return domain.Deployment{}, false, err
	}
	promoted := false
	defer func() {
		if !promoted {
			_ = os.RemoveAll(temporarySource)
		}
	}()
	if err := packageio.Extract(uploadPath, temporarySource, i.limits); err != nil {
		return domain.Deployment{}, false, err
	}
	// The deployment's identity is the package content, normalized by extraction,
	// rather than the compressed bytes it travelled in: the same package archived
	// twice can produce different streams, and an idempotency key promises that
	// the package has not changed, not that the encoder has not.
	contentDigest, err := provenance.DigestDirectory(temporarySource)
	if err != nil {
		return domain.Deployment{}, false, fmt.Errorf("digest package contents: %w", err)
	}
	finalSource := filepath.Join(sourcesDirectory, deploymentID)
	if err := os.Rename(temporarySource, finalSource); err != nil {
		return domain.Deployment{}, false, err
	}
	temporarySource = finalSource
	value, created, err := i.store.CreateDeployment(
		ctx, deploymentID, idempotencyKey, packageDigest, contentDigest, finalSource, actor,
	)
	if err != nil {
		return domain.Deployment{}, false, err
	}
	if !created {
		return value, false, nil
	}
	promoted = true
	return value, true, nil
}

func NewDeploymentID() (string, error) {
	var value [12]byte
	if _, err := cryptorand.Read(value[:]); err != nil {
		return "", err
	}
	return "dep_" + hex.EncodeToString(value[:]), nil
}

func ValidateIdempotencyKey(value string) error {
	if !validIdempotencyKey(strings.TrimSpace(value)) {
		return ErrInvalidIdempotencyKey
	}
	return nil
}

func validIdempotencyKey(value string) bool {
	if value == "" || len(value) > 255 {
		return false
	}
	return strings.IndexFunc(value, func(character rune) bool {
		return unicode.IsControl(character)
	}) == -1
}
