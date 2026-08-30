package python

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

const ArtifactDirectory = ".werkt-sdk/python"

//go:embed werkt/*.py werkt/connectors/*.py
var source embed.FS

func Digest() string {
	paths := make([]string, 0)
	_ = fs.WalkDir(source, "werkt", func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			paths = append(paths, path)
		}
		return err
	})
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		value, _ := source.ReadFile(path)
		_, _ = hash.Write([]byte(path))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(value)
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func RevisionHash(packageHash string) string {
	value := sha256.Sum256([]byte(packageHash + "\x00werkt-python-sdk:" + Digest()))
	return hex.EncodeToString(value[:])
}

func Install(root string) error {
	target := filepath.Join(root, filepath.FromSlash(ArtifactDirectory))
	if _, err := os.Stat(target); err == nil {
		return errors.New("automation package collides with the reserved Python SDK artifact path")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return fs.WalkDir(source, "werkt", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel("werkt", path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, "werkt", relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o750)
		}
		value, err := source.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, value, 0o640)
	})
}
