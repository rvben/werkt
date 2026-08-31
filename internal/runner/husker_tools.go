package runner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/rvben/werkt/internal/domain"
)

const (
	guestMiseUploadPath = "/tmp/werkt-mise"
	guestMisePath       = "/opt/werkt/bin/mise"
	guestMiseDataDir    = "/opt/werkt/mise"
	guestRuntimeBinDir  = "/opt/werkt/runtime/bin"
)

// PrepareManifest resolves and materializes runtime.tools before a revision is
// attested. Legacy runtime.image manifests retain their digest-pinning path.
func (r *HuskerRunner) PrepareManifest(ctx context.Context, value domain.Manifest) (domain.Manifest, error) {
	prepared, err := r.PinImages(value)
	if err != nil {
		return domain.Manifest{}, err
	}
	if prepared.Runtime.ResolvedTools == nil {
		return prepared, nil
	}
	if err := r.ensureToolImage(ctx, prepared.Runtime.ResolvedTools); err != nil {
		return domain.Manifest{}, err
	}
	return prepared, nil
}

func (r *HuskerRunner) ensureToolImage(parent context.Context, environment *domain.ResolvedToolEnvironment) error {
	r.imageMu.Lock()
	defer r.imageMu.Unlock()
	if err := parent.Err(); err != nil {
		return err
	}

	var base imageResponse
	if err := r.doJSON(parent, http.MethodGet, "/v1/images/"+url.PathEscape(environment.BaseImage), nil, http.StatusOK, &base); err != nil {
		return fmt.Errorf("inspect tool base image: %w", err)
	}
	if base.Name != environment.BaseImage || base.ContentDigest != environment.BaseImageDigest {
		return fmt.Errorf("tool base image %q digest mismatch: expected %s, got %s", environment.BaseImage, environment.BaseImageDigest, base.ContentDigest)
	}
	if base.Kind != "rootfs" {
		return fmt.Errorf("tool base image %q must be a direct-kernel rootfs image", environment.BaseImage)
	}

	var existing imageResponse
	err := r.doJSON(parent, http.MethodGet, "/v1/images/"+url.PathEscape(environment.Image), nil, http.StatusOK, &existing)
	if err == nil {
		if err := verifyResolvedToolImage(existing, environment, false); err != nil {
			return err
		}
		environment.ImageDigest = existing.ContentDigest
		return nil
	}
	var apiErr *huskerAPIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		return fmt.Errorf("inspect resolved tool image: %w", err)
	}

	miseBinary, err := r.verifyMiseBinary()
	if err != nil {
		return err
	}
	timeout := r.toolConfig.PrepareTimeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	suffix, err := secureSuffix()
	if err != nil {
		return fmt.Errorf("create tool preparer identity: %w", err)
	}
	vmName := "werkt-prepare-" + suffix
	lifetime := timeout + r.cleanupTimeout + guestCommandGrace
	if err := r.createVM(ctx, vmName, "werkt/tools/"+environment.IdentityDigest, environment.BaseImage, "filtered", environment.PreparationEgress, lifetime); err != nil {
		return fmt.Errorf("create tool preparation VM: %w", err)
	}
	defer r.cleanupVM(vmName)
	if err := r.waitReady(ctx, vmName); err != nil {
		return fmt.Errorf("wait for tool preparation VM: %w", err)
	}
	if err := r.uploadFile(ctx, vmName, guestMiseUploadPath, miseBinary, 0o755); err != nil {
		return fmt.Errorf("upload verified mise binary: %w", err)
	}
	miseConfig, err := renderMiseConfig(environment.Tools)
	if err != nil {
		return err
	}
	setupEnvironment := miseEnvironment()
	for _, command := range []execRequest{
		{Command: "/bin/mkdir", Args: []string{"-p", "/opt/werkt/bin", guestMiseDataDir, guestRuntimeBinDir, "/var/cache/werkt-mise", "/var/lib/werkt-mise", "/etc/werkt/mise"}, Timeout: 30},
		{Command: "/bin/cp", Args: []string{guestMiseUploadPath, guestMisePath}, Timeout: 30},
	} {
		response, err := r.exec(ctx, vmName, command)
		if err != nil || response.ExitCode != 0 {
			return fmt.Errorf("install mise in tool image: %w%s", errors.Join(err, exitCodeError(response)), errorLogs(formatLogs(response.Stdout, response.Stderr)))
		}
	}
	if err := r.uploadFile(ctx, vmName, "/etc/werkt/mise/config.toml", miseConfig, 0o644); err != nil {
		return fmt.Errorf("upload pinned mise configuration: %w", err)
	}
	architecture := map[string]string{"linux-amd64": "x86_64", "linux-arm64": "aarch64"}[environment.Platform]
	platformResponse, err := r.exec(ctx, vmName, execRequest{Command: "/bin/uname", Args: []string{"-m"}, Timeout: 30})
	if err != nil {
		return fmt.Errorf("inspect tool preparation VM platform: %w", err)
	}
	if platformResponse.ExitCode != 0 {
		return fmt.Errorf("inspect tool preparation VM platform: %w", exitCodeError(platformResponse))
	}
	if strings.TrimSpace(platformResponse.Stdout) != architecture {
		return fmt.Errorf("tool preparation VM platform mismatch: expected %s, got %q", architecture, strings.TrimSpace(platformResponse.Stdout))
	}
	versionResponse, err := r.exec(ctx, vmName, execRequest{Command: guestMisePath, Args: []string{"--version"}, Timeout: 30})
	if err != nil {
		return fmt.Errorf("inspect mise version: %w", err)
	}
	if versionResponse.ExitCode != 0 {
		return fmt.Errorf("inspect mise version: %w", exitCodeError(versionResponse))
	}
	if !strings.Contains(versionResponse.Stdout, environment.Installer.Version) {
		return fmt.Errorf("mise version mismatch: expected %s, got %q", environment.Installer.Version, strings.TrimSpace(versionResponse.Stdout))
	}
	response, err := r.exec(ctx, vmName, execRequest{Command: guestMisePath, Args: []string{"install"}, Environment: setupEnvironment, Timeout: durationSeconds(timeout)})
	if err != nil || response.ExitCode != 0 {
		return fmt.Errorf("install tools with mise: %w%s", errors.Join(err, exitCodeError(response)), errorLogs(formatLogs(response.Stdout, response.Stderr)))
	}
	for _, tool := range environment.Tools {
		entry := toolCatalog[tool.Name]
		where, err := r.exec(ctx, vmName, execRequest{Command: guestMisePath, Args: []string{"where", tool.Backend + "@" + tool.Version}, Environment: setupEnvironment, Timeout: 30})
		installRoot := strings.TrimSpace(where.Stdout)
		if err != nil || where.ExitCode != 0 {
			return fmt.Errorf("resolve prepared %s install root: %w%s", tool.Name, errors.Join(err, exitCodeError(where)), errorLogs(formatLogs(where.Stdout, where.Stderr)))
		}
		if !path.IsAbs(installRoot) || path.Clean(installRoot) != installRoot || strings.ContainsAny(installRoot, "\x00\r\n") ||
			!strings.HasPrefix(installRoot, guestMiseDataDir+"/installs/") {
			return fmt.Errorf("resolve prepared %s install root: mise returned unsafe path %q", tool.Name, installRoot)
		}
		for _, executable := range tool.Executables {
			resolvedPath := path.Join(installRoot, executable.RelativePath)
			checked, err := r.exec(ctx, vmName, execRequest{Command: "/usr/bin/test", Args: []string{"-x", resolvedPath}, Timeout: 30})
			if err != nil || checked.ExitCode != 0 {
				return fmt.Errorf("verify prepared %s executable %s: %w%s", tool.Name, executable.Name, errors.Join(err, exitCodeError(checked)), errorLogs(formatLogs(checked.Stdout, checked.Stderr)))
			}
			if err := r.uploadFile(ctx, vmName, path.Join(guestRuntimeBinDir, executable.Name), renderExecutableWrapper(resolvedPath), 0o755); err != nil {
				return fmt.Errorf("publish prepared %s executable %s: %w", tool.Name, executable.Name, err)
			}
		}
		command := path.Join(guestRuntimeBinDir, entry.SmokeCommand[0])
		response, err := r.exec(ctx, vmName, execRequest{Command: command, Args: entry.SmokeCommand[1:], Environment: setupEnvironment, Timeout: 60})
		if err != nil || response.ExitCode != 0 {
			return fmt.Errorf("verify prepared %s tool: %w%s", tool.Name, errors.Join(err, exitCodeError(response)), errorLogs(formatLogs(response.Stdout, response.Stderr)))
		}
	}
	flushed, err := r.exec(ctx, vmName, execRequest{Command: "/bin/sync", Timeout: 60})
	if err != nil || flushed.ExitCode != 0 {
		return fmt.Errorf("flush prepared tool image: %w%s", errors.Join(err, exitCodeError(flushed)), errorLogs(formatLogs(flushed.Stdout, flushed.Stderr)))
	}
	if err := r.doJSON(ctx, http.MethodPost, vmPath(vmName)+"/stop", nil, http.StatusNoContent, nil); err != nil {
		return fmt.Errorf("stop tool preparation VM: %w", err)
	}
	var committed imageResponse
	err = r.doJSON(ctx, http.MethodPost, vmPath(vmName)+"/commit-image", commitImageRequest{Name: environment.Image}, http.StatusCreated, &committed)
	if err != nil {
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict {
			return fmt.Errorf("commit prepared tool image: %w", err)
		}
		if err := r.doJSON(ctx, http.MethodGet, "/v1/images/"+url.PathEscape(environment.Image), nil, http.StatusOK, &committed); err != nil {
			return fmt.Errorf("inspect concurrently committed tool image: %w", err)
		}
	}
	if err := verifyResolvedToolImage(committed, environment, false); err != nil {
		return err
	}
	environment.ImageDigest = committed.ContentDigest
	return nil
}

func renderExecutableWrapper(target string) []byte {
	quoted := "'" + strings.ReplaceAll(target, "'", `'"'"'`) + "'"
	return []byte("#!/bin/sh\nexec " + quoted + " \"$@\"\n")
}

func renderMiseConfig(tools []domain.ResolvedTool) ([]byte, error) {
	var config strings.Builder
	config.WriteString("[tools]\n")
	for _, tool := range tools {
		if tool.Backend == "" || tool.Version == "" {
			return nil, fmt.Errorf("render mise config for incomplete runtime.tools.%s", tool.Name)
		}
		config.WriteString(strconv.Quote(tool.Backend))
		if tool.Artifact == nil {
			config.WriteString(" = ")
			config.WriteString(strconv.Quote(tool.Version))
			config.WriteByte('\n')
			continue
		}
		artifact := tool.Artifact
		fmt.Fprintf(&config, " = { version = %s, url = %s, checksum = %s, size = %s, format = %s, strip_components = %d }\n",
			strconv.Quote(tool.Version), strconv.Quote(artifact.URL), strconv.Quote(artifact.Digest),
			strconv.Quote(strconv.FormatInt(artifact.SizeBytes, 10)), strconv.Quote(artifact.Format), artifact.StripComponents)
	}
	return []byte(config.String()), nil
}

func (r *HuskerRunner) verifyToolImage(ctx context.Context, environment *domain.ResolvedToolEnvironment) error {
	if environment == nil || environment.ImageDigest == "" {
		return errors.New("revision has no prepared runtime.tools image digest")
	}
	var image imageResponse
	if err := r.doJSON(ctx, http.MethodGet, "/v1/images/"+url.PathEscape(environment.Image), nil, http.StatusOK, &image); err != nil {
		return fmt.Errorf("inspect prepared runtime.tools image: %w", err)
	}
	return verifyResolvedToolImage(image, environment, true)
}

func verifyResolvedToolImage(image imageResponse, environment *domain.ResolvedToolEnvironment, requireDigest bool) error {
	if image.Name != environment.Image {
		return fmt.Errorf("husker returned tool image %q while resolving %q", image.Name, environment.Image)
	}
	if image.ParentImage != environment.BaseImage {
		return fmt.Errorf("tool image %q has unexpected parent %q", image.Name, image.ParentImage)
	}
	if !validSHA256Digest(image.ContentDigest) {
		return fmt.Errorf("tool image %q has no valid content digest", image.Name)
	}
	if requireDigest && image.ContentDigest != environment.ImageDigest {
		return fmt.Errorf("tool image %q digest mismatch: expected %s, got %s", image.Name, environment.ImageDigest, image.ContentDigest)
	}
	return nil
}

func miseEnvironment() map[string]string {
	return map[string]string{
		"HOME": "/root", "MISE_YES": "1", "MISE_PIN": "1", "MISE_LOG_LEVEL": "warn",
		"MISE_DATA_DIR": guestMiseDataDir, "MISE_CACHE_DIR": "/var/cache/werkt-mise",
		"MISE_CONFIG_DIR": "/etc/werkt/mise", "MISE_STATE_DIR": "/var/lib/werkt-mise",
		"MISE_GLOBAL_CONFIG_FILE": "/etc/werkt/mise/config.toml",
	}
}

func toolRuntimeEnvironment(environment *domain.ResolvedToolEnvironment) map[string]string {
	if environment == nil {
		return nil
	}
	values := miseEnvironment()
	toolPath := guestRuntimeBinDir
	if environment.Version == 1 {
		toolPath = guestMiseDataDir + "/shims"
	}
	values["PATH"] = strings.Join([]string{toolPath, "/opt/werkt/bin", "/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"}, ":")
	return values
}

func exitCodeError(response execResponse) error {
	if response.ExitCode == 0 {
		return nil
	}
	return fmt.Errorf("process exited with code %d", response.ExitCode)
}

type commitImageRequest struct {
	Name string `json:"name"`
}
