package clientupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"filees/internal/releaseenvelope"
)

const (
	defaultMaxBundleSize = 512 << 20
	defaultMaxFiles      = 4096
)

type ArtifactFetcher interface {
	Cat(context.Context, string) ([]byte, error)
}

type BundleStager struct {
	Fetcher       ArtifactFetcher
	Root          string
	MaxBundleSize int64
	MaxFiles      int
}

type StagedBundle struct {
	Root     string
	Artifact releaseenvelope.Artifact
}

func (stager BundleStager) Stage(ctx context.Context, resolved *releaseenvelope.Resolved) (*StagedBundle, error) {
	if stager.Fetcher == nil || resolved == nil || resolved.Manifest == nil {
		return nil, errors.New("bundle stager is incomplete")
	}
	artifact, err := selectBundle(resolved.Manifest.Artifacts)
	if err != nil {
		return nil, err
	}
	maxSize := stager.MaxBundleSize
	if maxSize <= 0 {
		maxSize = defaultMaxBundleSize
	}
	if artifact.Size > maxSize {
		return nil, fmt.Errorf("bundle size %d exceeds limit %d", artifact.Size, maxSize)
	}
	base := path.Dir(resolved.Component.Manifest)
	artifactPath := path.Join(base, artifact.Source)
	data, err := stager.Fetcher.Cat(ctx, artifactPath)
	if err != nil {
		return nil, fmt.Errorf("fetch bundle: %w", err)
	}
	if int64(len(data)) != artifact.Size {
		return nil, fmt.Errorf("bundle size mismatch: got %d, want %d", len(data), artifact.Size)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != artifact.SHA256 {
		return nil, errors.New("bundle SHA-256 mismatch")
	}
	root := strings.TrimSpace(stager.Root)
	if root == "" {
		root = os.TempDir()
	}
	temp, err := os.MkdirTemp(root, "filees-client-bundle-*")
	if err != nil {
		return nil, err
	}
	if err := extractTarGzip(data, temp, stager.fileLimit(), maxSize); err != nil {
		os.RemoveAll(temp)
		return nil, err
	}
	return &StagedBundle{Root: temp, Artifact: artifact}, nil
}

func (bundle *StagedBundle) Remove() error {
	if bundle == nil || bundle.Root == "" {
		return nil
	}
	return os.RemoveAll(bundle.Root)
}

func (stager BundleStager) fileLimit() int {
	if stager.MaxFiles > 0 {
		return stager.MaxFiles
	}
	return defaultMaxFiles
}

func selectBundle(artifacts []releaseenvelope.Artifact) (releaseenvelope.Artifact, error) {
	var selected releaseenvelope.Artifact
	for _, artifact := range artifacts {
		if artifact.Kind != "bundle" {
			continue
		}
		if selected.Source != "" {
			return releaseenvelope.Artifact{}, errors.New("artifact manifest contains more than one bundle")
		}
		selected = artifact
	}
	if selected.Source == "" {
		return releaseenvelope.Artifact{}, errors.New("artifact manifest contains no bundle")
	}
	return selected, nil
}

func extractTarGzip(data []byte, destination string, maxFiles int, maxBytes int64) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("open bundle gzip: %w", err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	seen := make(map[string]struct{})
	files := 0
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read bundle tar: %w", err)
		}
		name, err := safeArchivePath(header.Name, header.Typeflag == tar.TypeDir)
		if err != nil {
			return err
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("duplicate bundle entry %q", name)
		}
		seen[name] = struct{}{}
		files++
		if files > maxFiles {
			return fmt.Errorf("bundle contains more than %d entries", maxFiles)
		}
		target := filepath.Join(destination, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > maxBytes-total {
				return errors.New("expanded bundle exceeds size limit")
			}
			total += header.Size
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(header.Mode) & 0o777
			if mode&0o111 != 0 {
				mode = 0o755
			} else {
				mode = 0o644
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(file, reader, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("bundle entry %q has forbidden type %d", name, header.Typeflag)
		}
	}
	return nil
}

func safeArchivePath(value string, directory bool) (string, error) {
	value = strings.TrimPrefix(strings.ReplaceAll(value, "\\", "/"), "./")
	if directory {
		value = strings.TrimSuffix(value, "/")
	} else if strings.HasSuffix(value, "/") {
		return "", fmt.Errorf("unsafe bundle path %q", value)
	}
	clean := path.Clean(value)
	if value == "" || clean == "." || clean != value || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("unsafe bundle path %q", value)
	}
	return clean, nil
}
