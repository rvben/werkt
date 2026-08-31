package runner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rvben/werkt/internal/domain"
)

const (
	guestMiseUploadPath = "/tmp/werkt-mise"
	guestMisePath       = "/opt/werkt/bin/mise"
	guestMiseDataDir    = "/opt/werkt/mise"
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
	setupEnvironment := miseEnvironment()
	for _, command := range []execRequest{
		{Command: "/bin/mkdir", Args: []string{"-p", "/opt/werkt/bin", guestMiseDataDir, "/var/cache/werkt-mise", "/var/lib/werkt-mise", "/etc/werkt/mise"}, Timeout: 30},
		{Command: "/bin/cp", Args: []string{guestMiseUploadPath, guestMisePath}, Timeout: 30},
	} {
		response, err := r.exec(ctx, vmName, command)
		if err != nil || response.ExitCode != 0 {
			return fmt.Errorf("install mise in tool image: %w%s", errors.Join(err, exitCodeError(response)), errorLogs(formatLogs(response.Stdout, response.Stderr)))
		}
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
	specs := make([]string, 0, len(environment.Tools))
	for _, tool := range environment.Tools {
		specs = append(specs, tool.Backend+"@"+tool.Version)
	}
	for _, args := range [][]string{
		append([]string{"install"}, specs...),
		append([]string{"use", "--global", "--pin"}, specs...),
	} {
		response, err := r.exec(ctx, vmName, execRequest{Command: guestMisePath, Args: args, Environment: setupEnvironment, Timeout: durationSeconds(timeout)})
		if err != nil || response.ExitCode != 0 {
			return fmt.Errorf("prepare tools with mise: %w%s", errors.Join(err, exitCodeError(response)), errorLogs(formatLogs(response.Stdout, response.Stderr)))
		}
	}
	if hasPinnedToolArtifacts(environment.Tools) {
		platform := map[string]string{"linux-amd64": "linux-x64", "linux-arm64": "linux-arm64"}[environment.Platform]
		response, err := r.exec(ctx, vmName, execRequest{
			Command: guestMisePath, Args: []string{"lock", "--global", "--platform", platform},
			Environment: setupEnvironment, Timeout: durationSeconds(timeout),
		})
		if err != nil || response.ExitCode != 0 {
			return fmt.Errorf("lock prepared tool artifacts: %w%s", errors.Join(err, exitCodeError(response)), errorLogs(formatLogs(response.Stdout, response.Stderr)))
		}
		lock, err := r.exec(ctx, vmName, execRequest{Command: "/bin/cat", Args: []string{"/etc/werkt/mise/mise.lock"}, Timeout: 30})
		if err != nil || lock.ExitCode != 0 {
			return fmt.Errorf("read prepared tool lock: %w%s", errors.Join(err, exitCodeError(lock)), errorLogs(formatLogs(lock.Stdout, lock.Stderr)))
		}
		if err := verifyPinnedToolArtifacts(lock.Stdout, environment.Tools); err != nil {
			return err
		}
	}
	for _, tool := range environment.Tools {
		entry := toolCatalog[tool.Name]
		args := []string{"exec", tool.Backend + "@" + tool.Version, "--"}
		args = append(args, entry.SmokeCommand...)
		response, err := r.exec(ctx, vmName, execRequest{Command: guestMisePath, Args: args, Environment: setupEnvironment, Timeout: 60})
		if err != nil || response.ExitCode != 0 {
			return fmt.Errorf("verify prepared %s tool: %w%s", tool.Name, errors.Join(err, exitCodeError(response)), errorLogs(formatLogs(response.Stdout, response.Stderr)))
		}
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

func hasPinnedToolArtifacts(tools []domain.ResolvedTool) bool {
	for _, tool := range tools {
		if tool.Artifact != nil {
			return true
		}
	}
	return false
}

func verifyPinnedToolArtifacts(lock string, tools []domain.ResolvedTool) error {
	for _, tool := range tools {
		if tool.Artifact == nil {
			continue
		}
		if !strings.Contains(lock, `url = "`+tool.Artifact.URL+`"`) ||
			!strings.Contains(lock, `checksum = "`+tool.Artifact.Digest+`"`) {
			return fmt.Errorf("mise lock does not bind runtime.tools.%s@%s to catalog artifact %s", tool.Name, tool.Version, tool.Artifact.Digest)
		}
	}
	return nil
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
	values["PATH"] = strings.Join([]string{guestMiseDataDir + "/shims", "/opt/werkt/bin", "/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"}, ":")
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
