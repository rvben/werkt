package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/domain"
	"github.com/rvben/werkt/internal/manifest"
)

type Deployer struct {
	store   *database.Store
	dataDir string
	builder Builder
}

// Builder turns a copied source tree into the artifact that a revision runs.
// Implementations may build locally or cross the isolation boundary.
type Builder interface {
	Build(context.Context, string, domain.Manifest, domain.DeploymentStepReporter) error
}

type Deployment struct {
	AutomationID string `json:"automationId"`
	RevisionID   string `json:"revisionId"`
	ContentHash  string `json:"contentHash"`
	ArtifactPath string `json:"artifactPath"`
}

type PreparedDeployment struct {
	SourceDirectory string
	Manifest        domain.Manifest
	ContentHash     string
}

type BuiltDeployment struct {
	PreparedDeployment
	ArtifactPath string
}

func NewDeployer(store *database.Store, dataDir string, builder Builder) *Deployer {
	return &Deployer{store: store, dataDir: dataDir, builder: builder}
}

func (d *Deployer) Deploy(ctx context.Context, sourceDirectory string) (Deployment, error) {
	prepared, err := d.Prepare(sourceDirectory)
	if err != nil {
		return Deployment{}, err
	}
	built, err := d.BuildArtifact(ctx, prepared, nil)
	if err != nil {
		return Deployment{}, err
	}
	return d.Activate(ctx, built, "cli")
}

func (d *Deployer) Prepare(sourceDirectory string) (PreparedDeployment, error) {
	absoluteSource, err := filepath.Abs(sourceDirectory)
	if err != nil {
		return PreparedDeployment{}, err
	}
	value, err := manifest.Load(absoluteSource)
	if err != nil {
		return PreparedDeployment{}, err
	}
	contentHash, err := manifest.HashDirectory(absoluteSource)
	if err != nil {
		return PreparedDeployment{}, err
	}
	return PreparedDeployment{SourceDirectory: absoluteSource, Manifest: value, ContentHash: contentHash}, nil
}

func (d *Deployer) BuildArtifact(ctx context.Context, prepared PreparedDeployment, reporter domain.DeploymentStepReporter) (BuiltDeployment, error) {
	artifactsDir := filepath.Join(d.dataDir, "artifacts")
	if err := os.MkdirAll(artifactsDir, 0o750); err != nil {
		return BuiltDeployment{}, fmt.Errorf("create artifacts directory: %w", err)
	}
	artifactPath, err := filepath.Abs(filepath.Join(artifactsDir, prepared.ContentHash))
	if err != nil {
		return BuiltDeployment{}, err
	}
	artifactInfo, statErr := os.Stat(artifactPath)
	requiresPromotion := len(prepared.Manifest.Runtime.Build) > 0 || len(prepared.Manifest.Deployment.Checks) > 0
	if os.IsNotExist(statErr) {
		temporary, err := os.MkdirTemp(artifactsDir, ".deploy-")
		if err != nil {
			return BuiltDeployment{}, err
		}
		deployed := false
		defer func() {
			if !deployed {
				_ = os.RemoveAll(temporary)
			}
		}()
		if err := copyDirectory(prepared.SourceDirectory, temporary); err != nil {
			return BuiltDeployment{}, err
		}
		if requiresPromotion {
			if d.builder == nil {
				return BuiltDeployment{}, errors.New("deployment builder is not configured")
			}
			if err := d.builder.Build(ctx, temporary, prepared.Manifest, reporter); err != nil {
				return BuiltDeployment{}, err
			}
		}
		if err := os.Rename(temporary, artifactPath); err != nil {
			publishedInfo, publishedErr := os.Stat(artifactPath)
			if publishedErr != nil || !publishedInfo.IsDir() {
				return BuiltDeployment{}, fmt.Errorf("publish artifact: %w", err)
			}
			// Another deployment of the same source won the publication race.
			if removeErr := os.RemoveAll(temporary); removeErr != nil {
				return BuiltDeployment{}, fmt.Errorf("remove redundant artifact: %w", removeErr)
			}
		}
		deployed = true
	} else if statErr != nil {
		return BuiltDeployment{}, statErr
	} else if !artifactInfo.IsDir() {
		return BuiltDeployment{}, fmt.Errorf("artifact path is not a directory: %s", artifactPath)
	} else if requiresPromotion {
		if d.builder == nil {
			return BuiltDeployment{}, errors.New("deployment builder is not configured")
		}
		temporary, err := os.MkdirTemp(artifactsDir, ".verify-")
		if err != nil {
			return BuiltDeployment{}, err
		}
		defer os.RemoveAll(temporary) //nolint:errcheck
		if err := copyDirectory(prepared.SourceDirectory, temporary); err != nil {
			return BuiltDeployment{}, err
		}
		if err := d.builder.Build(ctx, temporary, prepared.Manifest, reporter); err != nil {
			return BuiltDeployment{}, err
		}
	}
	return BuiltDeployment{PreparedDeployment: prepared, ArtifactPath: artifactPath}, nil
}

func (d *Deployer) Activate(ctx context.Context, built BuiltDeployment, actor string) (Deployment, error) {
	revisionID, err := d.store.DeployAs(ctx, built.Manifest, built.ContentHash, built.ArtifactPath, actor)
	if err != nil {
		return Deployment{}, err
	}
	return Deployment{
		AutomationID: built.Manifest.Metadata.Name,
		RevisionID:   revisionID,
		ContentHash:  built.ContentHash,
		ArtifactPath: built.ArtifactPath,
	}, nil
}

func (d *Deployer) ActivateDeployment(ctx context.Context, built BuiltDeployment, deploymentID, workerID, actor string) (Deployment, error) {
	revisionID, err := d.store.ActivateDeployment(
		ctx, deploymentID, workerID, built.Manifest, built.ContentHash, built.ArtifactPath, actor,
	)
	if err != nil {
		return Deployment{}, err
	}
	return Deployment{
		AutomationID: built.Manifest.Metadata.Name,
		RevisionID:   revisionID,
		ContentHash:  built.ContentHash,
		ArtifactPath: built.ArtifactPath,
	}, nil
}

func copyDirectory(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.IsDir() && ignoredArtifactDirectory(entry.Name()) {
			return filepath.SkipDir
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("automation packages cannot contain symbolic links: %s", relative)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported package entry: %s", relative)
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func ignoredArtifactDirectory(name string) bool {
	switch name {
	case ".git", "target", "__pycache__", ".pytest_cache", ".venv", "node_modules":
		return true
	default:
		return false
	}
}
