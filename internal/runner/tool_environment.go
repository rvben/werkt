package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/rvben/werkt/internal/domain"
)

const (
	toolEnvironmentVersion = 2
	toolCatalogRevision    = "2026-09-01.5"
	toolPreparerRevision   = "werkt-mise-v6"
)

var exactResolvedToolVersion = regexp.MustCompile(`^[0-9][0-9A-Za-z]*(?:[._-][0-9A-Za-z]+)*(?:\+[0-9A-Za-z.-]+)?$`)
var catalogExecutableName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

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
	Executables  map[string]string
	Capabilities []string
	SmokeCommand []string
	Egress       []domain.EgressRule
	Artifacts    map[string]map[string]domain.ToolArtifact
}

var toolCatalog = map[string]toolCatalogEntry{
	"ffmpeg": {
		Backend:      "http:ffmpeg",
		Executables:  map[string]string{"ffmpeg": "bin/ffmpeg", "ffprobe": "bin/ffprobe"},
		Capabilities: []string{"audio-encode:libmp3lame", "ffmpeg", "ffprobe"},
		SmokeCommand: []string{"ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "anullsrc=r=16000:cl=mono", "-t", "0.01", "-c:a", "libmp3lame", "-f", "null", "-"},
		Egress: []domain.EgressRule{
			{Host: "github.com", Port: 443},
			{Host: "release-assets.githubusercontent.com", Port: 443},
		},
		Artifacts: map[string]map[string]domain.ToolArtifact{
			"8.1.2-50-g1a748fe2cd": {
				"linux-amd64": {
					URL:             "https://github.com/BtbN/FFmpeg-Builds/releases/download/autobuild-2026-08-31-13-27/ffmpeg-n8.1.2-50-g1a748fe2cd-linux64-gpl-8.1.tar.xz",
					Digest:          "sha256:c733b4b2951e5957e15505f788b2c65a7a41b6da4b289e295852cc38079b4d2b",
					SizeBytes:       125758156,
					Format:          "tar.xz",
					StripComponents: 1,
				},
				"linux-arm64": {
					URL:             "https://github.com/BtbN/FFmpeg-Builds/releases/download/autobuild-2026-08-31-13-27/ffmpeg-n8.1.2-50-g1a748fe2cd-linuxarm64-gpl-8.1.tar.xz",
					Digest:          "sha256:ae5da4f51b9052390f414005f8ab26c1eed1268f327cce7cb79aa076b29bd66e",
					SizeBytes:       107695184,
					Format:          "tar.xz",
					StripComponents: 1,
				},
			},
		},
	},
	"python": {
		Backend:      "python",
		Executables:  map[string]string{"pip": "bin/pip", "python": "bin/python", "python3": "bin/python3"},
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
	executableSet := make(map[string]string)
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
		executableNames := make([]string, 0, len(entry.Executables))
		for executable := range entry.Executables {
			executableNames = append(executableNames, executable)
		}
		sort.Strings(executableNames)
		if len(executableNames) == 0 || len(entry.SmokeCommand) == 0 {
			return domain.ResolvedToolEnvironment{}, fmt.Errorf("runtime.tools.%s has an incomplete catalog entry", name)
		}
		executables := make([]domain.ResolvedToolExecutable, 0, len(executableNames))
		for _, executable := range executableNames {
			if !catalogExecutableName.MatchString(executable) {
				return domain.ResolvedToolEnvironment{}, fmt.Errorf("runtime.tools.%s has unsafe catalog executable %q", name, executable)
			}
			relativePath := entry.Executables[executable]
			if relativePath == "" || path.IsAbs(relativePath) || path.Clean(relativePath) != relativePath || relativePath == "." || strings.HasPrefix(relativePath, "../") {
				return domain.ResolvedToolEnvironment{}, fmt.Errorf("runtime.tools.%s executable %s has unsafe catalog path %q", name, executable, relativePath)
			}
			if owner, exists := executableSet[executable]; exists {
				return domain.ResolvedToolEnvironment{}, fmt.Errorf("runtime.tools.%s executable %s conflicts with runtime.tools.%s", name, executable, owner)
			}
			executableSet[executable] = name
			executables = append(executables, domain.ResolvedToolExecutable{Name: executable, RelativePath: relativePath})
		}
		if executableSet[entry.SmokeCommand[0]] != name {
			return domain.ResolvedToolEnvironment{}, fmt.Errorf("runtime.tools.%s smoke command is not a cataloged executable", name)
		}
		resolved := domain.ResolvedTool{Name: name, Version: version, Backend: entry.Backend, Executables: executables, Capabilities: capabilities}
		if entry.Artifacts != nil {
			platforms, ok := entry.Artifacts[version]
			if !ok {
				return domain.ResolvedToolEnvironment{}, fmt.Errorf("runtime.tools.%s version %s is not in catalog %s", name, version, toolCatalogRevision)
			}
			artifact, ok := platforms[config.Platform]
			if !ok || !validSHA256Digest(artifact.Digest) || !strings.HasPrefix(artifact.URL, "https://") ||
				artifact.SizeBytes <= 0 || artifact.Format == "" || artifact.StripComponents < 0 {
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
