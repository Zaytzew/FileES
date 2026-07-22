package releaseenvelope

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"
)

const SchemaVersion = 2

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	sha256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Envelope struct {
	SchemaVersion int         `json:"schema_version"`
	ReleaseID     string      `json:"release_id"`
	Sequence      uint64      `json:"sequence"`
	SecurityEpoch uint64      `json:"security_epoch"`
	KeyID         string      `json:"key_id"`
	ExpiresAt     string      `json:"expires_at"`
	Components    []Component `json:"components"`
}

type Component struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Manifest string `json:"manifest"`
}

type ArtifactManifest struct {
	SchemaVersion int        `json:"schema_version"`
	ReleaseID     string     `json:"release_id"`
	Sequence      uint64     `json:"sequence"`
	SecurityEpoch uint64     `json:"security_epoch"`
	KeyID         string     `json:"key_id"`
	Component     string     `json:"component"`
	Platform      string     `json:"platform"`
	Version       string     `json:"version"`
	Artifacts     []Artifact `json:"artifacts"`
}

type Artifact struct {
	Source string `json:"source"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Kind   string `json:"kind"`
}

func ParseEnvelope(data []byte, now time.Time) (*Envelope, error) {
	var envelope Envelope
	if err := decodeStrict(data, &envelope); err != nil {
		return nil, err
	}
	if envelope.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported envelope schema_version %d", envelope.SchemaVersion)
	}
	if err := validateIdentifier("release_id", envelope.ReleaseID); err != nil {
		return nil, err
	}
	if envelope.Sequence == 0 || envelope.SecurityEpoch == 0 {
		return nil, errors.New("sequence and security_epoch must be positive")
	}
	if err := validateIdentifier("key_id", envelope.KeyID); err != nil {
		return nil, err
	}
	expires, err := time.Parse(time.RFC3339, envelope.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("expires_at must be RFC3339: %w", err)
	}
	if !now.IsZero() && !expires.After(now) {
		return nil, fmt.Errorf("release envelope expired at %s", expires.UTC().Format(time.RFC3339))
	}
	if len(envelope.Components) == 0 {
		return nil, errors.New("release envelope has no components")
	}
	seen := make(map[string]struct{}, len(envelope.Components))
	for index, component := range envelope.Components {
		if err := validateIdentifier("component name", component.Name); err != nil {
			return nil, fmt.Errorf("component %d: %w", index, err)
		}
		if err := validateIdentifier("component platform", component.Platform); err != nil {
			return nil, fmt.Errorf("component %d: %w", index, err)
		}
		manifest, err := safeRelative(component.Manifest)
		if err != nil || !strings.HasSuffix(manifest, "/manifest.json") {
			return nil, fmt.Errorf("component %d manifest must be a safe path ending in /manifest.json", index)
		}
		envelope.Components[index].Manifest = manifest
		key := component.Name + "\x00" + component.Platform
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate component %s/%s", component.Name, component.Platform)
		}
		seen[key] = struct{}{}
	}
	return &envelope, nil
}

func ParseArtifactManifest(data []byte) (*ArtifactManifest, error) {
	var manifest ArtifactManifest
	if err := decodeStrict(data, &manifest); err != nil {
		return nil, err
	}
	if manifest.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported artifact manifest schema_version %d", manifest.SchemaVersion)
	}
	for name, value := range map[string]string{
		"release_id": manifest.ReleaseID, "key_id": manifest.KeyID, "component": manifest.Component,
		"platform": manifest.Platform, "version": manifest.Version,
	} {
		if err := validateIdentifier(name, value); err != nil {
			return nil, err
		}
	}
	if manifest.Sequence == 0 || manifest.SecurityEpoch == 0 {
		return nil, errors.New("sequence and security_epoch must be positive")
	}
	if len(manifest.Artifacts) == 0 {
		return nil, errors.New("artifact manifest has no artifacts")
	}
	seen := make(map[string]struct{}, len(manifest.Artifacts))
	for index, artifact := range manifest.Artifacts {
		source, err := safeRelative(artifact.Source)
		if err != nil {
			return nil, fmt.Errorf("artifact %d source: %w", index, err)
		}
		if !sha256Pattern.MatchString(artifact.SHA256) {
			return nil, fmt.Errorf("artifact %d sha256 must be 64 lowercase hexadecimal characters", index)
		}
		if artifact.Size <= 0 {
			return nil, fmt.Errorf("artifact %d size must be positive", index)
		}
		if err := validateIdentifier("artifact kind", artifact.Kind); err != nil {
			return nil, fmt.Errorf("artifact %d: %w", index, err)
		}
		if _, exists := seen[source]; exists {
			return nil, fmt.Errorf("duplicate artifact source %q", source)
		}
		seen[source] = struct{}{}
		manifest.Artifacts[index].Source = source
	}
	return &manifest, nil
}

func (envelope *Envelope) Select(component, platform string) (Component, error) {
	for _, candidate := range envelope.Components {
		if candidate.Name == component && candidate.Platform == platform {
			return candidate, nil
		}
	}
	return Component{}, fmt.Errorf("release has no component %s/%s", component, platform)
}

func (envelope *Envelope) ValidateManifest(component Component, manifest *ArtifactManifest) error {
	if manifest == nil {
		return errors.New("artifact manifest is nil")
	}
	if manifest.ReleaseID != envelope.ReleaseID || manifest.Sequence != envelope.Sequence ||
		manifest.SecurityEpoch != envelope.SecurityEpoch || manifest.KeyID != envelope.KeyID ||
		manifest.Component != component.Name || manifest.Platform != component.Platform {
		return errors.New("artifact manifest identity does not match release envelope")
	}
	return nil
}

func decodeStrict(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("document contains trailing JSON value")
		}
		return fmt.Errorf("document contains trailing data: %w", err)
	}
	return nil
}

func validateIdentifier(name, value string) error {
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("%s is invalid", name)
	}
	return nil
}

func safeRelative(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	clean := path.Clean(value)
	if value == "" || clean == "." || strings.HasPrefix(clean, "../") || clean == ".." || strings.HasPrefix(clean, "/") || clean != value {
		return "", errors.New("must be a canonical relative path")
	}
	return clean, nil
}
