package packageio

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	ErrCompressedLimit = errors.New("compressed package exceeds the configured limit")
	ErrExpandedLimit   = errors.New("expanded package exceeds the configured limit")
	ErrEntryLimit      = errors.New("package contains too many entries")
	ErrUnsafeArchive   = errors.New("package archive contains an unsafe entry")
	ErrInvalidArchive  = errors.New("package is not a valid gzip-compressed tar archive")
)

type Limits struct {
	CompressedBytes int64
	ExpandedBytes   int64
	Entries         int
}

func DefaultLimits() Limits {
	return Limits{CompressedBytes: 64 << 20, ExpandedBytes: 256 << 20, Entries: 10_000}
}

// Receive streams a bounded package to disk while calculating the digest of
// the exact compressed representation received from the caller.
func Receive(reader io.Reader, destination string, maxBytes int64) (string, error) {
	if maxBytes <= 0 {
		return "", errors.New("compressed package limit must be positive")
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	succeeded := false
	defer func() {
		_ = file.Close()
		if !succeeded {
			_ = os.Remove(destination)
		}
	}()

	hash := sha256.New()
	limited := &io.LimitedReader{R: reader, N: maxBytes + 1}
	written, copyErr := io.Copy(io.MultiWriter(file, hash), limited)
	if copyErr != nil {
		return "", fmt.Errorf("receive package: %w", copyErr)
	}
	if written > maxBytes {
		return "", ErrCompressedLimit
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	succeeded = true
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Extract expands only directories and regular files. Paths, entry count,
// expanded bytes, and permissions are constrained before anything is written.
func Extract(archivePath, destination string, limits Limits) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close() //nolint:errcheck // the archive is opened read-only
	gzipReader, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArchive, err)
	}
	defer gzipReader.Close() //nolint:errcheck // the gzip stream is opened read-only

	if err := os.MkdirAll(destination, 0o750); err != nil {
		return err
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close() //nolint:errcheck // extraction errors take precedence

	tarReader := tar.NewReader(gzipReader)
	seen := make(map[string]struct{})
	var expanded int64
	entries := 0
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidArchive, err)
		}
		entries++
		if entries > limits.Entries {
			return fmt.Errorf("%w: maximum is %d", ErrEntryLimit, limits.Entries)
		}
		clean, err := safeArchivePath(header.Name)
		if err != nil {
			return err
		}
		if clean == "." {
			if header.Typeflag == tar.TypeDir {
				continue
			}
			return fmt.Errorf("%w: root path is not a directory", ErrUnsafeArchive)
		}
		identity := strings.ToLower(clean)
		if _, exists := seen[identity]; exists {
			return fmt.Errorf("%w: duplicate path %q", ErrUnsafeArchive, header.Name)
		}
		seen[identity] = struct{}{}
		target := filepath.FromSlash(clean)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA: //nolint:staticcheck // TypeRegA is valid in legacy tar archives
			if header.Size < 0 || expanded > limits.ExpandedBytes-header.Size {
				return fmt.Errorf("%w: maximum is %d bytes", ErrExpandedLimit, limits.ExpandedBytes)
			}
			expanded += header.Size
			if err := root.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			mode := os.FileMode(0o640 | (header.Mode & 0o110))
			file, err := root.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(file, tarReader, header.Size)
			closeErr := file.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: unsupported entry %q (type %d)", ErrUnsafeArchive, header.Name, header.Typeflag)
		}
	}
	if err := gzipReader.Close(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArchive, err)
	}
	return nil
}

// WriteArchive produces a deterministic gzip-compressed tar stream rooted at
// the package directory and returns the digest of its compressed bytes.
func WriteArchive(sourceDirectory string, destination io.Writer) (string, error) {
	absolute, err := filepath.Abs(sourceDirectory)
	if err != nil {
		return "", err
	}
	paths := make([]string, 0)
	err = filepath.WalkDir(absolute, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(absolute, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.IsDir() && ignoredDirectory(entry.Name()) {
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("automation packages cannot contain symbolic links: %s", relative)
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("unsupported package entry: %s", relative)
			}
		}
		paths = append(paths, relative)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)

	hash := sha256.New()
	gzipWriter, err := gzip.NewWriterLevel(io.MultiWriter(destination, hash), gzip.BestSpeed)
	if err != nil {
		return "", err
	}
	gzipWriter.ModTime = time.Unix(0, 0)
	gzipWriter.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	for _, relative := range paths {
		path := filepath.Join(absolute, relative)
		info, err := os.Stat(path)
		if err != nil {
			return "", closeWriters(tarWriter, gzipWriter, err)
		}
		header := &tar.Header{
			Name:    filepath.ToSlash(relative),
			Mode:    int64(info.Mode().Perm()),
			ModTime: time.Unix(0, 0),
		}
		if info.IsDir() {
			header.Typeflag = tar.TypeDir
			header.Name += "/"
		} else {
			header.Typeflag = tar.TypeReg
			header.Size = info.Size()
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return "", closeWriters(tarWriter, gzipWriter, err)
		}
		if info.IsDir() {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			return "", closeWriters(tarWriter, gzipWriter, err)
		}
		_, copyErr := io.CopyN(tarWriter, file, info.Size())
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return "", closeWriters(tarWriter, gzipWriter, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		_ = gzipWriter.Close()
		return "", err
	}
	if err := gzipWriter.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func closeWriters(tarWriter *tar.Writer, gzipWriter *gzip.Writer, original error) error {
	return errors.Join(original, tarWriter.Close(), gzipWriter.Close())
}

func safeArchivePath(name string) (string, error) {
	if name == "" || len(name) > 4096 || strings.ContainsRune(name, '\x00') || strings.Contains(name, "\\") {
		return "", fmt.Errorf("%w: path %q", ErrUnsafeArchive, name)
	}
	clean := pathpkg.Clean(name)
	if pathpkg.IsAbs(clean) || !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", fmt.Errorf("%w: path %q", ErrUnsafeArchive, name)
	}
	return clean, nil
}

func ignoredDirectory(name string) bool {
	switch name {
	case ".git", "target", "__pycache__", ".pytest_cache", ".venv", "node_modules":
		return true
	default:
		return false
	}
}
