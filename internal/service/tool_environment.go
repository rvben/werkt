package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/rvben/werkt/internal/domain"
)

func resolvedEnvironmentRevisionHash(sourceHash string, environment *domain.ResolvedToolEnvironment) string {
	encoded, err := json.Marshal(struct {
		Source      string                          `json:"source"`
		Environment *domain.ResolvedToolEnvironment `json:"environment"`
	}{Source: sourceHash, Environment: environment})
	if err != nil {
		panic("resolved tool environment contains only JSON-compatible fields: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
