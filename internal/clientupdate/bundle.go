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

// FileFetcher streams an artifact to a local file as it arrives
// (svnfetch.SVN), which is what makes download progress observable.
type FileFetcher interface {
	CatToFile(context.Context, string, string) error
}

func (stager BundleStager) root() string {
	if root := strings.TrimSpace(stager.Root); root != "" {
		return root
	}
	return os.TempDir()
}

func (stager BundleStager) maxSize() int64 {
	if stager.MaxBundleSize > 0 {
		return stager.MaxBundleSize
	}
	return defaultMaxBundleSize
}

func (stager BundleStager) bundle(resolved *releaseenvelope.Resolved) (releaseenvelope.Artifact, string, error) {
	if stager.Fetcher == nil || resolved == nil || resolved.Manifest == nil {
		return releaseenvelope.Artifact{}, "", errors.New("bundle stager is incomplete")
	}
	artifact, err := selectBundle(resolved.Manifest.Artifacts)
	if err != nil {
		return releaseenvelope.Artifact{}, "", err
	}
	if artifact.Size > stager.maxSize() {
		return releaseenvelope.Artifact{}, "", fmt.Errorf("bundle size %d exceeds limit %d", artifact.Size, stager.maxSize())
	}
	if !sha256Pattern(artifact.SHA256) {
		return releaseenvelope.Artifact{}, "", fmt.Errorf("bundle SHA-256 %q is malformed", artifact.SHA256)
	}
	return artifact, path.Join(path.Dir(resolved.Component.Manifest), artifact.Source), nil
}

// downloads holds verified bundles named by their SHA-256, so the one fetched
// in the background serves the plan and the installation after it. The owner's
// station, 2026-09-25: the plan fetched the whole bundle inside a 30-second
// call, failed with "context deadline exceeded" on a slow link, and the
// installation would have fetched it a second time.
func (stager BundleStager) downloads() string { return filepath.Join(stager.root(), "downloads") }

// Download fetches the release's bundle once and keeps it after checking size
// and SHA-256 against the signed manifest. A verified copy already on disk is
// used as it is; anything else in the directory is an older release's and is
// removed once this one is complete.
func (stager BundleStager) Download(ctx context.Context, resolved *releaseenvelope.Resolved) (string, error) {
	artifact, source, err := stager.bundle(resolved)
	if err != nil {
		return "", err
	}
	dir := stager.downloads()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	final := filepath.Join(dir, artifact.SHA256+".tar.gz")
	if verifyBundleFile(final, artifact) == nil {
		return final, nil
	}
	_ = os.Remove(final)
	partial := filepath.Join(dir, artifact.SHA256+".download")
	discard := func() { _ = os.Remove(partial); _ = os.Remove(partial + ".part") }
	discard()
	if streaming, ok := stager.Fetcher.(FileFetcher); ok {
		err = streaming.CatToFile(ctx, source, partial)
	} else {
		var data []byte
		if data, err = stager.Fetcher.Cat(ctx, source); err == nil {
			err = os.WriteFile(partial, data, 0o600)
		}
	}
	if err != nil {
		discard()
		return "", fmt.Errorf("fetch bundle: %w", err)
	}
	if err := verifyBundleFile(partial, artifact); err != nil {
		discard()
		return "", err
	}
	if err := os.Rename(partial, final); err != nil {
		discard()
		return "", err
	}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, entry := range entries {
			if entry.Name() != filepath.Base(final) {
				_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
			}
		}
	}
	return final, nil
}

// DownloadProgress reports how much of the release's bundle is on disk. ready
// means a complete copy is present; it is verified again before use.
func (stager BundleStager) DownloadProgress(resolved *releaseenvelope.Resolved) (have, total int64, ready bool) {
	artifact, _, err := stager.bundle(resolved)
	if err != nil {
		return 0, 0, false
	}
	dir := stager.downloads()
	if info, err := os.Stat(filepath.Join(dir, artifact.SHA256+".tar.gz")); err == nil && info.Size() == artifact.Size {
		return artifact.Size, artifact.Size, true
	}
	partial := filepath.Join(dir, artifact.SHA256+".download")
	for _, candidate := range []string{partial, partial + ".part"} { // the native helper writes .part, then renames
		if info, err := os.Stat(candidate); err == nil && info.Size() > have {
			have = info.Size()
		}
	}
	return min(have, artifact.Size), artifact.Size, false
}

func verifyBundleFile(name string, artifact releaseenvelope.Artifact) error {
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != artifact.Size {
		return fmt.Errorf("bundle size mismatch: got %d, want %d", info.Size(), artifact.Size)
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return err
	}
	if hex.EncodeToString(digest.Sum(nil)) != artifact.SHA256 {
		return errors.New("bundle SHA-256 mismatch")
	}
	return nil
}

func sha256Pattern(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func (stager BundleStager) Stage(ctx context.Context, resolved *releaseenvelope.Resolved) (*StagedBundle, error) {
	artifact, _, err := stager.bundle(resolved)
	if err != nil {
		return nil, err
	}
	maxSize := stager.maxSize()
	downloaded, err := stager.Download(ctx, resolved)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(downloaded)
	if err != nil {
		return nil, err
	}
	// Checked again on the bytes actually extracted: the file sat on disk
	// between the download and now.
	if int64(len(data)) != artifact.Size {
		return nil, fmt.Errorf("bundle size mismatch: got %d, want %d", len(data), artifact.Size)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != artifact.SHA256 {
		_ = os.Remove(downloaded)
		return nil, errors.New("bundle SHA-256 mismatch")
	}
	temp, err := os.MkdirTemp(stager.root(), "filees-client-bundle-*")
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
