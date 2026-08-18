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

	"github.com/rvben/werkt/internal/domain"
)

const (
	maxCompressedBuildArtifactBytes uint64 = 1 << 30
	maxExpandedBuildArtifactBytes   int64  = 4 << 30
	maxBuildArtifactEntries                = 100_000
)

// Build compiles an automation in a disposable VM and replaces directory with
// the exact workspace produced by that VM. The caller only publishes it after
// this method succeeds.
func (r *HuskerRunner) Build(parent context.Context, directory string, value domain.Manifest) error {
	if len(value.Runtime.Build) == 0 {
		return nil
	}
	rootFS := value.Runtime.BuildImage
	if rootFS == "" {
		rootFS = value.Runtime.Image
	}
	if rootFS == "" {
		rootFS = r.rootFS
	}
	if strings.TrimSpace(rootFS) == "" {
		return errors.New("runtime.buildImage, runtime.image, or the husker rootfs fallback is required for a build")
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
	lifetime := r.provisionTimeout + r.buildTimeout + r.cleanupTimeout + 2*guestCommandGrace

	provisionContext, cancelProvision := context.WithTimeout(parent, r.provisionTimeout)
	defer cancelProvision()
	owner := "werkt/build/" + value.Metadata.Name
	if err := r.createVM(provisionContext, vmName, owner, rootFS, r.buildNetwork, lifetime); err != nil {
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

	buildContext, cancelBuild := context.WithTimeout(parent, r.buildTimeout+r.provisionTimeout+guestCommandGrace)
	defer cancelBuild()
	command := value.Runtime.Build
	response, executeErr := r.exec(buildContext, vmName, execRequest{
		Command:     command[0],
		Args:        command[1:],
		WorkingDir:  workspacePath,
		Environment: value.Runtime.Environment,
		Timeout:     durationSeconds(r.buildTimeout),
	})
	logs := formatLogs(response.Stdout, response.Stderr)
	if executeErr != nil {
		if buildContext.Err() != nil {
			return fmt.Errorf("build exceeded timeout %s: %w", r.buildTimeout, buildContext.Err())
		}
		return fmt.Errorf("build automation in husker VM: %w%s", executeErr, errorLogs(logs))
	}
	if response.ExitCode == 124 {
		return fmt.Errorf("build exceeded timeout %s%s", r.buildTimeout, errorLogs(logs))
	}
	if response.ExitCode != 0 {
		return fmt.Errorf("build process exited with code %d%s", response.ExitCode, errorLogs(logs))
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
		target := filepath.Join(destination, filepath.FromSlash(clean))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o750); err != nil {
				_ = gzipReader.Close()
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || expanded > maxExpandedBuildArtifactBytes-header.Size {
				_ = gzipReader.Close()
				return fmt.Errorf("expanded build artifact exceeds %d bytes", maxExpandedBuildArtifactBytes)
			}
			expanded += header.Size
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				_ = gzipReader.Close()
				return err
			}
			mode := os.FileMode(header.Mode).Perm()
			if mode == 0 {
				mode = 0o640
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
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
	if pathpkg.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("build archive contains unsafe path %q", name)
	}
	return clean, nil
}
