package releasepublish

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	installmanifest "filees/internal/serverinstall/manifest"
)

type Spec struct {
	ReleaseID   string                           `json:"release_id"`
	Platform    string                           `json:"platform"`
	SVNRevision string                           `json:"svn_revision,omitempty"`
	Files       []FileSpec                       `json:"files"`
	Configs     []installmanifest.ConfigContract `json:"configs,omitempty"`
	Orphans     []installmanifest.Orphan         `json:"orphans,omitempty"`
}

type FileSpec struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind,omitempty"`
	Mode   string `json:"mode,omitempty"`
}

func LoadSpec(path string) (Spec, error) {
	f, err := os.Open(path)
	if err != nil {
		return Spec{}, err
	}
	defer f.Close()
	var spec Spec
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return Spec{}, fmt.Errorf("decode release spec: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return Spec{}, errors.New("release spec contains trailing JSON value")
		}
		return Spec{}, fmt.Errorf("release spec contains trailing data: %w", err)
	}
	return spec, nil
}

func Generate(payloadRoot string, spec Spec) ([]byte, error) {
	if strings.TrimSpace(spec.ReleaseID) == "" || strings.TrimSpace(spec.Platform) == "" {
		return nil, errors.New("release_id and platform are required")
	}
	if len(spec.Files) == 0 {
		return nil, errors.New("at least one file is required")
	}
	root, err := filepath.Abs(payloadRoot)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve payload root: %w", err)
	}
	result := installmanifest.Manifest{
		SchemaVersion: 1,
		ReleaseID:     strings.TrimSpace(spec.ReleaseID),
		Platform:      strings.TrimSpace(spec.Platform),
		SVNRevision:   strings.TrimSpace(spec.SVNRevision),
		Configs:       spec.Configs,
		Orphans:       spec.Orphans,
	}
	seenSources := make(map[string]struct{}, len(spec.Files))
	seenTargets := make(map[string]struct{}, len(spec.Files))
	for index, entry := range spec.Files {
		source, err := cleanRelative(entry.Source)
		if err != nil {
			return nil, fmt.Errorf("file %d source: %w", index, err)
		}
		target := strings.TrimSpace(entry.Target)
		if target == "" {
			return nil, fmt.Errorf("file %d target is required", index)
		}
		if _, exists := seenSources[source]; exists {
			return nil, fmt.Errorf("duplicate source %q", source)
		}
		if _, exists := seenTargets[target]; exists {
			return nil, fmt.Errorf("duplicate target %q", target)
		}
		seenSources[source] = struct{}{}
		seenTargets[target] = struct{}{}
		mode, err := cleanMode(entry.Mode)
		if err != nil {
			return nil, fmt.Errorf("file %d mode: %w", index, err)
		}
		sourcePath, err := confinedPath(root, source)
		if err != nil {
			return nil, fmt.Errorf("file %d source: %w", index, err)
		}
		digest, err := fileDigest(sourcePath)
		if err != nil {
			return nil, fmt.Errorf("hash %s: %w", source, err)
		}
		result.Files = append(result.Files, installmanifest.File{
			Source: source, Target: target, Kind: strings.TrimSpace(entry.Kind), Mode: mode, SHA256: digest,
		})
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Source < result.Files[j].Source })
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func WriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".manifest-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func cleanRelative(path string) (string, error) {
	path = filepath.ToSlash(strings.TrimSpace(path))
	clean := filepath.ToSlash(filepath.Clean(path))
	if path == "" || clean == "." || strings.HasPrefix(clean, "../") || clean == ".." || filepath.IsAbs(path) {
		return "", errors.New("must be a relative path within the payload root")
	}
	return clean, nil
}

func cleanMode(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	mode, err := strconv.ParseUint(value, 8, 12)
	if err != nil || mode > 0o7777 {
		return "", fmt.Errorf("%q is not an octal permission mode", value)
	}
	return fmt.Sprintf("%04o", mode), nil
}

func confinedPath(root, relative string) (string, error) {
	path, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("symlink resolves outside the payload root")
	}
	return path, nil
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("payload entry is not a regular file")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
