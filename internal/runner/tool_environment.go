package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rvben/werkt/internal/domain"
)

const (
	toolEnvironmentVersion = 1
	toolCatalogRevision    = "2026-09-01.2"
	toolPreparerRevision   = "werkt-mise-v1"
)

var exactResolvedToolVersion = regexp.MustCompile(`^[0-9][0-9A-Za-z]*(?:[._-][0-9A-Za-z]+)*(?:\+[0-9A-Za-z.-]+)?$`)

type toolEnvironmentConfig struct {
	BaseImage      string
	BaseDigest     string
	Platform       string
	MisePath       string
	MiseVersion    string
	MiseDigest     string
	PrepareTimeout time.Duration
}

type toolCatalogEntry struct {
	Backend      string
	Capabilities []string
	SmokeCommand []string
	Egress       []domain.EgressRule
	Artifacts    map[string]map[string]domain.ToolArtifact
}

var toolCatalog = map[string]toolCatalogEntry{
	"ffmpeg": {
		Backend:      "aqua:Tyrrrz/FFmpegBin",
		Capabilities: []string{"audio-encode:libmp3lame", "ffmpeg", "ffprobe"},
		SmokeCommand: []string{"ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "anullsrc=r=16000:cl=mono", "-t", "0.01", "-c:a", "libmp3lame", "-f", "null", "-"},
		Egress:       verifiedGitHubReleaseEgress(),
		Artifacts: map[string]map[string]domain.ToolArtifact{
			"8.1.2": {
				"linux-amd64": {
					URL:    "https://github.com/Tyrrrz/FFmpegBin/releases/download/8.1.2/ffmpeg-linux-x64.zip",
					Digest: "sha256:66d14e6fdd3ca71e26e674afced0b3a44e63d4d892fcbec1cbce3b2a6d5032b8",
				},
				"linux-arm64": {
					URL:    "https://github.com/Tyrrrz/FFmpegBin/releases/download/8.1.2/ffmpeg-linux-arm64.zip",
					Digest: "sha256:acceaf328440388b321ef79b07663496a9b2607a412c2ce1704d32a8f83defce",
				},
			},
		},
	},
	"python": {
		Backend:      "python",
		Capabilities: []string{"pip", "python", "python3"},
		SmokeCommand: []string{"python3", "--version"},
		Egress:       verifiedGitHubReleaseEgress(),
	},
}

func verifiedGitHubReleaseEgress() []domain.EgressRule {
	return []domain.EgressRule{
		{Host: "api.github.com", Port: 443},
		{Host: "github.com", Port: 443},
		{Host: "mise-versions.jdx.dev", Port: 443},
		{Host: "objects.githubusercontent.com", Port: 443},
		{Host: "release-assets.githubusercontent.com", Port: 443},
		{Host: "tuf-repo-cdn.sigstore.dev", Port: 443},
	}
}

type toolEnvironmentIdentity struct {
	Version           int                   `json:"version"`
	CatalogRevision   string                `json:"catalogRevision"`
	PreparerRevision  string                `json:"preparerRevision"`
	Platform          string                `json:"platform"`
	BaseImage         string                `json:"baseImage"`
	BaseImageDigest   string                `json:"baseImageDigest"`
	Installer         domain.ToolInstaller  `json:"installer"`
	Tools             []domain.ResolvedTool `json:"tools"`
	Capabilities      []string              `json:"capabilities"`
	PreparationEgress []domain.EgressRule   `json:"preparationEgress"`
}

func resolveToolEnvironment(requested map[string]string, config toolEnvironmentConfig) (domain.ResolvedToolEnvironment, error) {
	if len(requested) == 0 {
		return domain.ResolvedToolEnvironment{}, errors.New("runtime.tools must contain at least one tool")
	}
	if strings.TrimSpace(config.BaseImage) == "" {
		return domain.ResolvedToolEnvironment{}, errors.New("runtime.tools requires WERKT_HUSKER_TOOL_BASE_IMAGE")
	}
	if !validSHA256Digest(config.BaseDigest) {
		return domain.ResolvedToolEnvironment{}, errors.New("runtime.tools requires WERKT_HUSKER_TOOL_BASE_DIGEST as sha256:<64 lowercase hex>")
	}
	if config.Platform != "linux-amd64" && config.Platform != "linux-arm64" {
		return domain.ResolvedToolEnvironment{}, errors.New("runtime.tools requires WERKT_HUSKER_TOOL_PLATFORM to be linux-amd64 or linux-arm64")
	}
	if strings.TrimSpace(config.MisePath) == "" || !filepath.IsAbs(config.MisePath) {
		return domain.ResolvedToolEnvironment{}, errors.New("runtime.tools requires an absolute WERKT_HUSKER_MISE_PATH")
	}
	if !exactResolvedToolVersion.MatchString(config.MiseVersion) {
		return domain.ResolvedToolEnvironment{}, errors.New("runtime.tools requires an exact WERKT_HUSKER_MISE_VERSION")
	}
	if !validSHA256Digest(config.MiseDigest) {
		return domain.ResolvedToolEnvironment{}, errors.New("runtime.tools requires WERKT_HUSKER_MISE_DIGEST as sha256:<64 lowercase hex>")
	}

	names := make([]string, 0, len(requested))
	for name := range requested {
		names = append(names, name)
	}
	sort.Strings(names)
	tools := make([]domain.ResolvedTool, 0, len(names))
	capabilitySet := make(map[string]struct{})
	egressSet := make(map[string]domain.EgressRule)
	for _, name := range names {
		entry, ok := toolCatalog[name]
		if !ok {
			return domain.ResolvedToolEnvironment{}, fmt.Errorf("runtime.tools.%s is not in catalog %s", name, toolCatalogRevision)
		}
		version := requested[name]
		if !exactResolvedToolVersion.MatchString(version) || strings.EqualFold(version, "latest") || strings.EqualFold(version, "system") {
			return domain.ResolvedToolEnvironment{}, fmt.Errorf("runtime.tools.%s must be an exact version", name)
		}
		capabilities := append([]string(nil), entry.Capabilities...)
		sort.Strings(capabilities)
		resolved := domain.ResolvedTool{Name: name, Version: version, Backend: entry.Backend, Capabilities: capabilities}
		if entry.Artifacts != nil {
			platforms, ok := entry.Artifacts[version]
			if !ok {
				return domain.ResolvedToolEnvironment{}, fmt.Errorf("runtime.tools.%s version %s is not in catalog %s", name, version, toolCatalogRevision)
			}
			artifact, ok := platforms[config.Platform]
			if !ok || !validSHA256Digest(artifact.Digest) || !strings.HasPrefix(artifact.URL, "https://") {
				return domain.ResolvedToolEnvironment{}, fmt.Errorf("runtime.tools.%s version %s has no verified artifact for %s", name, version, config.Platform)
			}
			resolved.Artifact = &artifact
		}
		tools = append(tools, resolved)
		for _, capability := range capabilities {
			capabilitySet[capability] = struct{}{}
		}
		for _, rule := range entry.Egress {
			key := fmt.Sprintf("%s:%d/%s", rule.Host, rule.Port, rule.EffectiveProtocol())
			egressSet[key] = rule
		}
	}
	capabilities := sortedKeys(capabilitySet)
	egressKeys := make([]string, 0, len(egressSet))
	for key := range egressSet {
		egressKeys = append(egressKeys, key)
	}
	sort.Strings(egressKeys)
	egress := make([]domain.EgressRule, 0, len(egressKeys))
	for _, key := range egressKeys {
		egress = append(egress, egressSet[key])
	}
	identity := toolEnvironmentIdentity{
		Version: toolEnvironmentVersion, CatalogRevision: toolCatalogRevision,
		PreparerRevision: toolPreparerRevision, Platform: config.Platform,
		BaseImage: config.BaseImage, BaseImageDigest: config.BaseDigest,
		Installer: domain.ToolInstaller{Name: "mise", Version: config.MiseVersion, Digest: config.MiseDigest},
		Tools:     tools, Capabilities: capabilities, PreparationEgress: egress,
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return domain.ResolvedToolEnvironment{}, fmt.Errorf("encode tool environment identity: %w", err)
	}
	digest := sha256.Sum256(encoded)
	identityDigest := "sha256:" + hex.EncodeToString(digest[:])
	return domain.ResolvedToolEnvironment{
		Version: identity.Version, IdentityDigest: identityDigest,
		Image:           "werkt-tools-" + hex.EncodeToString(digest[:24]),
		CatalogRevision: identity.CatalogRevision, PreparerRevision: identity.PreparerRevision,
		Platform: identity.Platform, BaseImage: identity.BaseImage, BaseImageDigest: identity.BaseImageDigest,
		Installer: identity.Installer, Tools: identity.Tools, Capabilities: identity.Capabilities,
		PreparationEgress: identity.PreparationEgress,
	}, nil
}

func (r *HuskerRunner) verifyMiseBinary() ([]byte, error) {
	info, err := os.Stat(r.toolConfig.MisePath)
	if err != nil {
		return nil, fmt.Errorf("inspect mise binary: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("WERKT_HUSKER_MISE_PATH must identify a regular file")
	}
	file, err := os.Open(r.toolConfig.MisePath)
	if err != nil {
		return nil, fmt.Errorf("open mise binary: %w", err)
	}
	hash := sha256.New()
	contents, readErr := io.ReadAll(io.TeeReader(file, hash))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("read mise binary: %w", err)
	}
	actual := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actual != r.toolConfig.MiseDigest {
		return nil, fmt.Errorf("mise binary digest mismatch: expected %s, got %s", r.toolConfig.MiseDigest, actual)
	}
	return contents, nil
}

func validSHA256Digest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func sortedKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
