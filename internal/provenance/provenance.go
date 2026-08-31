package provenance

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rvben/werkt/internal/domain"
)

const (
	AlgorithmEd25519       = "ed25519"
	MetadataDirectory      = ".werkt"
	metadataFilename       = "provenance.json"
	statementVersionLegacy = 1
	statementVersionTools  = 2
)

var (
	ErrInvalidKey             = errors.New("invalid provenance custody key")
	ErrArtifactDigestMismatch = errors.New("artifact digest does not match its attestation")
	ErrInvalidAttestation     = errors.New("artifact attestation is invalid")
	ErrUnpinnedImage          = errors.New("husker images must use immutable OCI digest references")
)

type Attestor struct {
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	keyID      string
}

type statement struct {
	Version         int                             `json:"version"`
	ArtifactDigest  string                          `json:"artifactDigest"`
	ContentHash     string                          `json:"contentHash"`
	AutomationID    string                          `json:"automationId"`
	RuntimeImage    string                          `json:"runtimeImage,omitempty"`
	BuildImage      string                          `json:"buildImage,omitempty"`
	ToolEnvironment *domain.ResolvedToolEnvironment `json:"toolEnvironment,omitempty"`
}

func NewAttestor(encodedMasterKey string) (*Attestor, error) {
	masterKey, err := base64.StdEncoding.DecodeString(encodedMasterKey)
	if err != nil || len(masterKey) != 32 {
		return nil, fmt.Errorf("%w: WERKT_SECRET_KEY must be base64 for exactly 32 bytes", ErrInvalidKey)
	}
	deriver := hmac.New(sha256.New, masterKey)
	_, _ = deriver.Write([]byte("werkt.dev/artifact-attestation/ed25519/v1"))
	seed := deriver.Sum(nil)
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	fingerprint := sha256.Sum256(publicKey)
	return &Attestor{
		privateKey: privateKey,
		publicKey:  publicKey,
		keyID:      "sha256:" + hex.EncodeToString(fingerprint[:]),
	}, nil
}

func (a *Attestor) Attest(directory string, manifest domain.Manifest, contentHash string) (domain.ArtifactProvenance, error) {
	digest, err := DigestDirectory(directory)
	if err != nil {
		return domain.ArtifactProvenance{}, err
	}
	version := statementVersionLegacy
	if manifest.Runtime.ResolvedTools != nil {
		version = statementVersionTools
	}
	value := domain.ArtifactProvenance{
		Version:         version,
		ArtifactDigest:  digest,
		ContentHash:     contentHash,
		AutomationID:    manifest.Metadata.Name,
		RuntimeImage:    manifest.Runtime.Image,
		BuildImage:      effectiveBuildImage(manifest),
		ToolEnvironment: manifest.Runtime.ResolvedTools,
		Algorithm:       AlgorithmEd25519,
		SigningKeyID:    a.keyID,
		PublicKey:       base64.StdEncoding.EncodeToString(a.publicKey),
	}
	payload, err := statementJSON(value)
	if err != nil {
		return domain.ArtifactProvenance{}, err
	}
	value.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(a.privateKey, payload))
	return value, nil
}

func (a *Attestor) Verify(directory string, value domain.ArtifactProvenance) error {
	if (value.Version != statementVersionLegacy && value.Version != statementVersionTools) ||
		value.Algorithm != AlgorithmEd25519 || value.SigningKeyID != a.keyID ||
		value.PublicKey != base64.StdEncoding.EncodeToString(a.publicKey) {
		return ErrInvalidAttestation
	}
	if (value.Version == statementVersionLegacy && value.ToolEnvironment != nil) ||
		(value.Version == statementVersionTools && !validToolEnvironment(value.ToolEnvironment)) {
		return ErrInvalidAttestation
	}
	signature, err := base64.StdEncoding.DecodeString(value.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return ErrInvalidAttestation
	}
	payload, err := statementJSON(value)
	if err != nil || !ed25519.Verify(a.publicKey, payload, signature) {
		return ErrInvalidAttestation
	}
	digest, err := DigestDirectory(directory)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(digest), []byte(value.ArtifactDigest)) {
		return fmt.Errorf("%w: expected %s, got %s", ErrArtifactDigestMismatch, value.ArtifactDigest, digest)
	}
	return nil
}

func statementJSON(value domain.ArtifactProvenance) ([]byte, error) {
	return json.Marshal(statement{
		Version: value.Version, ArtifactDigest: value.ArtifactDigest, ContentHash: value.ContentHash,
		AutomationID: value.AutomationID, RuntimeImage: value.RuntimeImage, BuildImage: value.BuildImage,
		ToolEnvironment: value.ToolEnvironment,
	})
}

func validToolEnvironment(value *domain.ResolvedToolEnvironment) bool {
	return value != nil && value.Version == 1 && value.Image != "" && value.BaseImage != "" &&
		isSHA256Digest(value.IdentityDigest) && isSHA256Digest(value.ImageDigest) &&
		isSHA256Digest(value.BaseImageDigest) && isSHA256Digest(value.Installer.Digest) && len(value.Tools) > 0
}

func isSHA256Digest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func effectiveBuildImage(value domain.Manifest) string {
	if len(value.Runtime.Build) == 0 && len(value.Deployment.Checks) == 0 {
		return ""
	}
	if value.Runtime.BuildImage != "" {
		return value.Runtime.BuildImage
	}
	return value.Runtime.Image
}

func DigestDirectory(directory string) (string, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	var files []string
	err = filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(absolute, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		first, _, _ := strings.Cut(filepath.ToSlash(relative), "/")
		if first == MetadataDirectory {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return fmt.Errorf("reserved provenance metadata path %q is not a directory", MetadataDirectory)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact contains symbolic link: %s", relative)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("artifact contains unsupported entry: %s", relative)
		}
		files = append(files, relative)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk artifact: %w", err)
	}
	sort.Strings(files)
	hash := sha256.New()
	for _, relative := range files {
		path := filepath.Join(absolute, relative)
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if _, err := fmt.Fprintf(hash, "file\x00%s\x00%04o\x00%d\x00", filepath.ToSlash(relative), info.Mode().Perm(), info.Size()); err != nil {
			return "", err
		}
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func Write(directory string, value domain.ArtifactProvenance) error {
	metadataPath := filepath.Join(directory, MetadataDirectory)
	if err := os.Mkdir(metadataPath, 0o700); err != nil {
		return fmt.Errorf("create provenance metadata: %w", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(metadataPath, metadataFilename), append(encoded, '\n'), 0o600); err != nil {
		return fmt.Errorf("write provenance metadata: %w", err)
	}
	return nil
}

func Load(directory string) (domain.ArtifactProvenance, error) {
	encoded, err := os.ReadFile(filepath.Join(directory, MetadataDirectory, metadataFilename))
	if err != nil {
		return domain.ArtifactProvenance{}, fmt.Errorf("read provenance metadata: %w", err)
	}
	var value domain.ArtifactProvenance
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return domain.ArtifactProvenance{}, fmt.Errorf("decode provenance metadata: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("unexpected trailing JSON value")
		}
		return domain.ArtifactProvenance{}, fmt.Errorf("decode provenance metadata: %w", err)
	}
	return value, nil
}

func PinHuskerImages(value domain.Manifest) (domain.Manifest, error) {
	if !isDigestReference(value.Runtime.Image) {
		return domain.Manifest{}, fmt.Errorf("%w: runtime.image must use name@sha256:<64 lowercase hex>", ErrUnpinnedImage)
	}
	if value.Runtime.BuildImage != "" && !isDigestReference(value.Runtime.BuildImage) {
		return domain.Manifest{}, fmt.Errorf("%w: runtime.buildImage must use name@sha256:<64 lowercase hex>", ErrUnpinnedImage)
	}
	return value, nil
}

func isDigestReference(value string) bool {
	if value != strings.TrimSpace(value) || strings.ContainsAny(value, "\t\r\n ") {
		return false
	}
	name, digest, ok := strings.Cut(value, "@")
	if !ok || name == "" || strings.Contains(name, "@") || !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	return err == nil && strings.ToLower(digest) == digest
}
