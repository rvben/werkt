package database

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rvben/werkt/internal/domain"
)

func testArtifactProvenance(value domain.Manifest, contentHash string) domain.ArtifactProvenance {
	return domain.ArtifactProvenance{
		Version: 1, ArtifactDigest: "sha256:" + strings.Repeat("f", 64), ContentHash: contentHash,
		AutomationID: value.Metadata.Name, Algorithm: "ed25519", SigningKeyID: "sha256:" + strings.Repeat("1", 64),
		PublicKey: "public", Signature: "signature",
	}
}

func TestDeployRejectsIncompleteOrMalformedProvenanceBeforeDatabaseAccess(t *testing.T) {
	value := domain.Manifest{Metadata: domain.Metadata{Name: "provenance-test"}}
	contentHash := strings.Repeat("a", 64)
	valid := testArtifactProvenance(value, contentHash)

	for name, mutate := range map[string]func(*domain.ArtifactProvenance){
		"short content hash": func(provenance *domain.ArtifactProvenance) { provenance.ContentHash = "short" },
		"wrong automation":   func(provenance *domain.ArtifactProvenance) { provenance.AutomationID = "other" },
		"missing public key": func(provenance *domain.ArtifactProvenance) { provenance.PublicKey = "" },
		"wrong algorithm":    func(provenance *domain.ArtifactProvenance) { provenance.Algorithm = "rsa" },
		"malformed digest":   func(provenance *domain.ArtifactProvenance) { provenance.ArtifactDigest = "sha256:not-a-digest" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			candidateHash := contentHash
			if name == "short content hash" {
				candidateHash = "short"
			}
			store := &Store{}
			if _, err := store.Deploy(context.Background(), value, candidateHash, t.TempDir(), candidate); !errors.Is(err, ErrArtifactProvenanceRequired) {
				t.Fatalf("Deploy error=%v", err)
			}
		})
	}
}
