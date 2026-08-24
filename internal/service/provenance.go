package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/rvben/werkt/internal/database"
	"github.com/rvben/werkt/internal/provenance"
)

type ArtifactCustodian struct {
	store    *database.Store
	attestor *provenance.Attestor
}

// AdoptLegacy establishes the first signed baseline for artifacts created
// before provenance existed. It never replaces metadata: an unexpected or
// invalid reserved metadata path fails the upgrade instead of being blessed.
func (c *ArtifactCustodian) AdoptLegacy(ctx context.Context) (int, error) {
	artifacts, err := c.store.ListRevisionArtifacts(ctx)
	if err != nil {
		return 0, err
	}
	adopted := 0
	for _, artifact := range artifacts {
		if artifact.Provenance.ArtifactDigest != "" {
			continue
		}
		value, err := provenance.Load(artifact.Path)
		if errors.Is(err, fs.ErrNotExist) {
			value, err = c.attestor.Attest(artifact.Path, artifact.Manifest, artifact.ContentHash)
			if err == nil {
				err = provenance.Write(artifact.Path, value)
			}
		}
		if err != nil {
			return adopted, fmt.Errorf("adopt legacy revision %s: %w", artifact.RevisionID, err)
		}
		if err := c.attestor.Verify(artifact.Path, value); err != nil {
			return adopted, fmt.Errorf("adopt legacy revision %s: %w", artifact.RevisionID, err)
		}
		if value.ContentHash != artifact.ContentHash || value.AutomationID != artifact.AutomationID ||
			value.RuntimeImage != artifact.Manifest.Runtime.Image || value.BuildImage != effectiveBuildImage(artifact.Manifest) {
			return adopted, fmt.Errorf("adopt legacy revision %s: attestation does not match revision identity", artifact.RevisionID)
		}
		changed, err := c.store.AdoptRevisionProvenance(ctx, artifact, value)
		if err != nil {
			return adopted, fmt.Errorf("adopt legacy revision %s: %w", artifact.RevisionID, err)
		}
		if changed {
			adopted++
		}
	}
	return adopted, nil
}

func NewArtifactCustodian(store *database.Store, attestor *provenance.Attestor) *ArtifactCustodian {
	return &ArtifactCustodian{store: store, attestor: attestor}
}

func (c *ArtifactCustodian) VerifyRevision(ctx context.Context, automationID, revisionID string) error {
	artifact, err := c.store.GetRevisionArtifact(ctx, automationID, revisionID)
	if err != nil {
		return err
	}
	if err := c.attestor.Verify(artifact.Path, artifact.Provenance); err != nil {
		return fmt.Errorf("revision %s: %w", revisionID, err)
	}
	return nil
}

func (c *ArtifactCustodian) VerifyAll(ctx context.Context) (int, error) {
	artifacts, err := c.store.ListRevisionArtifacts(ctx)
	if err != nil {
		return 0, err
	}
	for _, artifact := range artifacts {
		if err := c.attestor.Verify(artifact.Path, artifact.Provenance); err != nil {
			return 0, fmt.Errorf("revision %s: %w", artifact.RevisionID, err)
		}
	}
	return len(artifacts), nil
}
