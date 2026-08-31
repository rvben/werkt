package runner

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"time"

	"github.com/rvben/werkt/internal/domain"
	"github.com/rvben/werkt/internal/provenance"
)

const (
	maxCompressedBuildArtifactBytes uint64 = 1 << 30
	maxExpandedBuildArtifactBytes   int64  = 4 << 30
	maxBuildArtifactEntries                = 100_000
)

// Build compiles an automation in a disposable VM and replaces directory with
// the exact workspace produced by that VM. The caller only publishes it after
// this method succeeds.
func (r *HuskerRunner) Build(parent context.Context, directory string, value domain.Manifest, reporter domain.DeploymentStepReporter) error {
	if len(value.Runtime.Build) == 0 && len(value.Deployment.Checks) == 0 {
		return nil
	}
	toolEnvironment := value.Runtime.ResolvedTools
	rootFS := ""
	if len(value.Runtime.Tools) > 0 {
		if toolEnvironment == nil {
			return errors.New("runtime.tools revision is missing its resolved environment")
		}
		rootFS = toolEnvironment.Image
	} else {
		if _, err := provenance.PinHuskerImages(value); err != nil {
			return err
		}
		rootFS = value.Runtime.BuildImage
		if rootFS == "" {
			rootFS = value.Runtime.Image
		}
		if rootFS == "" {
			rootFS = r.rootFS
		}
		if strings.TrimSpace(rootFS) == "" {
			return errors.New("runtime.buildImage, runtime.image, or the husker rootfs fallback is required for deployment commands")
		}
	}

	source, err := archiveDirectory(directory)
	if err != nil {
		return fmt.Errorf("package build input: %w", err)
	}
	suffix, err := secureSuffix()
	if err != nil {
		return fmt.Errorf("create build identity: %w", err)
	}
	vmName := "werkt-build-" + suffix
	guestRoot := "/tmp/" + vmName
	inputPath := guestRoot + ".tar.gz"
	workspacePath := guestRoot + "/work"
	outputPath := guestRoot + ".built.tar.gz"
	commandBudget := time.Duration(0)
	if len(value.Runtime.Build) > 0 {
		commandBudget += r.buildTimeout
	}
	for _, check := range value.Deployment.Checks {
		commandBudget += check.TimeoutDuration()
	}
	lifetime := r.provisionTimeout + commandBudget + r.cleanupTimeout + 2*guestCommandGrace

	provisionContext, cancelProvision := context.WithTimeout(parent, r.provisionTimeout)
	defer cancelProvision()
	if toolEnvironment != nil {
		if err := r.verifyToolImage(provisionContext, toolEnvironment); err != nil {
			return err
		}
	} else {
		rootFS, err = r.ensureOCIImage(provisionContext, rootFS)
		if err != nil {
			return fmt.Errorf("prepare husker build image: %w", err)
		}
	}
	owner := "werkt/build/" + value.Metadata.Name
	if err := r.createVM(provisionContext, vmName, owner, rootFS, r.buildNetwork, nil, lifetime); err != nil {
		return fmt.Errorf("create husker build VM: %w", err)
	}
	defer r.cleanupVM(vmName)

	if err := r.waitReady(provisionContext, vmName); err != nil {
		return fmt.Errorf("wait for husker build VM: %w", err)
	}
	if err := r.uploadFile(provisionContext, vmName, inputPath, source, 0o600); err != nil {
		return fmt.Errorf("upload build input: %w", err)
	}
	if _, err := r.exec(provisionContext, vmName, execRequest{
		Command: "/bin/mkdir",
		Args:    []string{"-p", workspacePath},
		Timeout: 30,
	}); err != nil {
		return fmt.Errorf("prepare build workspace: %w", err)
	}
	if _, err := r.exec(provisionContext, vmName, execRequest{
		Command: "/bin/tar",
		Args:    []string{"-xzf", inputPath, "-C", workspacePath},
		Timeout: 30,
	}); err != nil {
		return fmt.Errorf("extract build input: %w", err)
	}
	cancelProvision()

	buildContext, cancelBuild := context.WithTimeout(parent, commandBudget+r.provisionTimeout+guestCommandGrace)
	defer cancelBuild()
	if len(value.Runtime.Build) > 0 {
		if err := r.runPromotionCommand(buildContext, vmName, workspacePath, promotionEnvironment(value.Runtime),
			"build", "build", value.Runtime.Build, r.buildTimeout, reporter); err != nil {
			return fmt.Errorf("build automation in husker VM: %w", err)
		}
	}
	for _, check := range value.Deployment.Checks {
		if err := r.runPromotionCommand(buildContext, vmName, workspacePath, promotionEnvironment(value.Runtime),
			"check:"+check.ID, "check", check.Command, check.TimeoutDuration(), reporter); err != nil {
			return fmt.Errorf("check %s in husker VM: %w", check.ID, err)
		}
	}
	if _, err := r.exec(buildContext, vmName, execRequest{
		Command: "/bin/tar",
		Args:    []string{"-czf", outputPath, "-C", workspacePath, "."},
		Timeout: durationSeconds(r.provisionTimeout),
	}); err != nil {
		return fmt.Errorf("package build output: %w", err)
	}
	artifact, err := r.readFileLimited(buildContext, vmName, outputPath, maxCompressedBuildArtifactBytes)
	if err != nil {
		return fmt.Errorf("download build output: %w", err)
	}
	if err := replaceDirectoryFromArchive(directory, artifact); err != nil {
		return fmt.Errorf("promote build output: %w", err)
	}
	return nil
}

func (r *HuskerRunner) runPromotionCommand(parent context.Context, vmName, workspacePath string, environment map[string]string, id, kind string, command []string, timeout time.Duration, reporter domain.DeploymentStepReporter) error {
	if reporter != nil {
		if err := reporter(domain.DeploymentStepUpdate{ID: id, Kind: kind, Status: domain.DeploymentStepRunning}); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(parent, timeout+guestCommandGrace)
	defer cancel()
	response, executeErr := r.exec(ctx, vmName, execRequest{
		Command:     command[0],
		Args:        command[1:],
		WorkingDir:  workspacePath,
		Environment: environment,
		Timeout:     durationSeconds(timeout),
	})
	logs := formatLogs(response.Stdout, response.Stderr)
	commandErr := executeErr
	if ctx.Err() != nil {
		commandErr = fmt.Errorf("exceeded timeout %s: %w", timeout, ctx.Err())
	} else if executeErr == nil && response.ExitCode == 124 {
		commandErr = fmt.Errorf("exceeded timeout %s", timeout)
	} else if executeErr == nil && response.ExitCode != 0 {
		commandErr = fmt.Errorf("process exited with code %d", response.ExitCode)
	}
	status := domain.DeploymentStepSucceeded
	message := ""
	if commandErr != nil {
		status = domain.DeploymentStepFailed
		message = commandErr.Error()
	}
	if reporter != nil {
		if err := reporter(domain.DeploymentStepUpdate{ID: id, Kind: kind, Status: status, Logs: logs, Error: message}); err != nil {
			return errors.Join(commandErr, err)
		}
	}
	if commandErr != nil {
		return fmt.Errorf("%w%s", commandErr, errorLogs(logs))
	}
	return nil
}

func errorLogs(logs string) string {
	if logs == "" {
		return ""
	}
	return "\n" + strings.TrimSpace(logs)
}

func secureSuffix() (string, error) {
	var value [8]byte
	if _, err := cryptorand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func replaceDirectoryFromArchive(directory string, compressed []byte) error {
	parent := filepath.Dir(directory)
	staging, err := os.MkdirTemp(parent, ".built-")
	if err != nil {
		return err
	}
	promoted := false
	defer func() {
		if !promoted {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := extractBuildArchive(staging, compressed); err != nil {
		return err
	}

	suffix, err := secureSuffix()
	if err != nil {
		return err
	}
	backup := filepath.Join(parent, ".pre-build-"+suffix)
	if err := os.Rename(directory, backup); err != nil {
		return err
	}
	if err := os.Rename(staging, directory); err != nil {
		restoreErr := os.Rename(backup, directory)
		return errors.Join(err, restoreErr)
	}
	promoted = true
	if err := os.RemoveAll(backup); err != nil {
		slog.Warn("built artifact was promoted but its pre-build directory could not be removed", "path", backup, "error", err)
	}
	return nil
}

func extractBuildArchive(destination string, compressed []byte) error {
	gzipReader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		_ = gzipReader.Close()
		return err
	}
	defer root.Close() //nolint:errcheck // extraction errors take precedence

	tarReader := tar.NewReader(gzipReader)
	var expanded int64
	entries := 0
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = gzipReader.Close()
			return fmt.Errorf("read tar stream: %w", err)
		}
		entries++
		if entries > maxBuildArtifactEntries {
			_ = gzipReader.Close()
			return fmt.Errorf("build artifact exceeds the %d-entry limit", maxBuildArtifactEntries)
		}
		clean, err := safeArchivePath(header.Name)
		if err != nil {
			_ = gzipReader.Close()
			return err
		}
		if clean == "." {
			if header.Typeflag == tar.TypeDir {
				continue
			}
			_ = gzipReader.Close()
			return errors.New("build archive contains a file at its root path")
		}
		target := filepath.FromSlash(clean)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(target, 0o750); err != nil {
				_ = gzipReader.Close()
				return err
			}
		case tar.TypeReg, tar.TypeRegA: //nolint:staticcheck // TypeRegA is valid in legacy tar archives
			if header.Size < 0 || expanded > maxExpandedBuildArtifactBytes-header.Size {
				_ = gzipReader.Close()
				return fmt.Errorf("expanded build artifact exceeds %d bytes", maxExpandedBuildArtifactBytes)
			}
			expanded += header.Size
			if err := root.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				_ = gzipReader.Close()
				return err
			}
			mode := os.FileMode(header.Mode).Perm()
			if mode == 0 {
				mode = 0o640
			}
			file, err := root.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				_ = gzipReader.Close()
				return err
			}
			_, copyErr := io.CopyN(file, tarReader, header.Size)
			closeErr := file.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				_ = gzipReader.Close()
				return err
			}
		default:
			_ = gzipReader.Close()
			return fmt.Errorf("build archive contains unsupported entry %q (type %d)", header.Name, header.Typeflag)
		}
	}
	if err := gzipReader.Close(); err != nil {
		return fmt.Errorf("close gzip stream: %w", err)
	}
	return nil
}

func safeArchivePath(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, '\x00') || strings.Contains(name, "\\") {
		return "", fmt.Errorf("build archive contains unsafe path %q", name)
	}
	clean := pathpkg.Clean(name)
	if pathpkg.IsAbs(clean) || !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", fmt.Errorf("build archive contains unsafe path %q", name)
	}
	return clean, nil
}
