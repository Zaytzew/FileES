// Command filees-client-release writes the two documents a desktop client
// needs in order to update itself: the artifact manifest for one platform, and
// the channel envelope that points at it.
//
// They are produced together on purpose. Both carry the release ID, the
// sequence, the security epoch and the signing key ID, the resolver refuses the
// pair if any of them disagree, and generating them separately means four
// chances to type a number differently. One command, one set of inputs.
//
// Nothing here signs anything. This host holds only the public release key -
// the same rule the server release follows - so the output is a candidate that
// the signing machine reviews, signs and promotes into channels/.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"

	"filees/internal/releaseenvelope"
	"filees/pkg/config"
)

func main() {
	bundle := flag.String("bundle", "", "path to the client bundle tar.gz")
	component := flag.String("component", config.DesktopUpdateComponent, "component name")
	platform := flag.String("platform", "", "target platform, e.g. windows-amd64")
	releaseID := flag.String("release-id", "", "release identifier, e.g. r819")
	version := flag.String("version", "", "client version this bundle installs")
	sequence := flag.Uint64("sequence", 0, "monotonic release sequence")
	securityEpoch := flag.Uint64("security-epoch", 1, "security epoch")
	keyID := flag.String("key-id", "", "identifier of the key that will sign this release")
	expires := flag.String("expires", "", "envelope expiry, RFC3339 (default: one year from now)")
	releaseRoot := flag.String("release-root", "", "directory receiving manifest.json, beside the bundle")
	channelOut := flag.String("channel-out", "", "path for the channel envelope candidate")
	mergeChannel := flag.String("merge-channel", "", "existing envelope whose other components are carried forward")
	flag.Parse()

	if *bundle == "" || *platform == "" || *releaseID == "" || *version == "" || *keyID == "" ||
		*releaseRoot == "" || *channelOut == "" || *sequence == 0 || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: filees-client-release -bundle FILE.tar.gz -platform windows-amd64 \\")
		fmt.Fprintln(os.Stderr, "         -release-id rNNN -version X.Y.Z.N -sequence N -key-id KEY \\")
		fmt.Fprintln(os.Stderr, "         -release-root DIR -channel-out FILE [-merge-channel FILE]")
		os.Exit(2)
	}
	if err := run(*bundle, *component, *platform, *releaseID, *version, *keyID, *expires,
		*releaseRoot, *channelOut, *mergeChannel, *sequence, *securityEpoch); err != nil {
		fmt.Fprintln(os.Stderr, "filees-client-release:", err)
		os.Exit(1)
	}
}

func run(bundlePath, component, platform, releaseID, version, keyID, expires,
	releaseRoot, channelOut, mergeChannel string, sequence, securityEpoch uint64) error {

	data, err := os.ReadFile(bundlePath)
	if err != nil {
		return fmt.Errorf("read bundle: %w", err)
	}
	digest := sha256.Sum256(data)

	// The manifest names the artifact relative to its own directory, because
	// that is how the client resolves it: path.Dir(component.Manifest) joined
	// with the source. A name with a directory in it would resolve somewhere
	// nobody staged.
	source := filepath.Base(bundlePath)
	if source != path.Base(source) || source == "." || source == ".." {
		return fmt.Errorf("bundle name %q is not a plain file name", source)
	}
	manifest := releaseenvelope.ArtifactManifest{
		SchemaVersion: releaseenvelope.SchemaVersion,
		ReleaseID:     releaseID,
		Sequence:      sequence,
		SecurityEpoch: securityEpoch,
		KeyID:         keyID,
		Component:     component,
		Platform:      platform,
		Version:       version,
		Artifacts: []releaseenvelope.Artifact{{
			Source: source,
			SHA256: hex.EncodeToString(digest[:]),
			Size:   int64(len(data)),
			Kind:   "bundle",
		}},
	}
	manifestPath := filepath.Join(releaseRoot, "manifest.json")
	manifestBytes, err := marshal(manifest)
	if err != nil {
		return err
	}
	// Parsed back through the client's own reader before it is written. A
	// release that the thing meant to consume it would reject is worth catching
	// here rather than on somebody's machine after it has been signed.
	if _, err := releaseenvelope.ParseArtifactManifest(manifestBytes); err != nil {
		return fmt.Errorf("the generated manifest would be refused: %w", err)
	}

	expiry := expires
	if expiry == "" {
		expiry = time.Now().UTC().AddDate(1, 0, 0).Format(time.RFC3339)
	}
	if _, err := time.Parse(time.RFC3339, expiry); err != nil {
		return fmt.Errorf("expires must be RFC3339: %w", err)
	}
	components, err := mergedComponents(mergeChannel, component, platform, releaseID, sequence, securityEpoch, keyID)
	if err != nil {
		return err
	}
	envelope := releaseenvelope.Envelope{
		SchemaVersion: releaseenvelope.SchemaVersion,
		ReleaseID:     releaseID,
		Sequence:      sequence,
		SecurityEpoch: securityEpoch,
		KeyID:         keyID,
		ExpiresAt:     expiry,
		Components:    components,
	}
	envelopeBytes, err := marshal(envelope)
	if err != nil {
		return err
	}
	if _, err := releaseenvelope.ParseEnvelope(envelopeBytes, time.Now()); err != nil {
		return fmt.Errorf("the generated envelope would be refused: %w", err)
	}

	if err := os.MkdirAll(releaseRoot, 0o755); err != nil {
		return err
	}
	if err := writeNew(manifestPath, manifestBytes); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(channelOut), 0o755); err != nil {
		return err
	}
	if err := writeNew(channelOut, envelopeBytes); err != nil {
		return err
	}
	fmt.Printf("wrote %s\nwrote %s\n", manifestPath, channelOut)
	fmt.Printf("sign both on the signing machine, then promote the envelope to channels/\n")
	return nil
}

// mergedComponents keeps every other platform already assembled for this
// exact release.
//
// The envelope is a release-level document covering every platform at once.
// An entry from an older channel cannot be carried verbatim: the resolver
// requires every referenced manifest to repeat this envelope's release ID,
// sequence, epoch and key ID. Combining platforms therefore happens against a
// candidate for the same release; an older live channel is only safe when it
// contains no other platform that would need carrying.
func mergedComponents(existingPath, component, platform, releaseID string, sequence, securityEpoch uint64, keyID string) ([]releaseenvelope.Component, error) {
	manifestPath := path.Join("releases", releaseID, component, platform, "manifest.json")
	components := []releaseenvelope.Component{{Name: component, Platform: platform, Manifest: manifestPath}}
	if existingPath == "" {
		return components, nil
	}
	raw, err := os.ReadFile(existingPath)
	if errors.Is(err, os.ErrNotExist) {
		return components, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the channel being merged: %w", err)
	}
	// Zero time: an expired envelope is still a perfectly good list of the
	// platforms a channel carries, and refusing to merge it would quietly drop
	// them at exactly the moment somebody is renewing the release.
	previous, err := releaseenvelope.ParseEnvelope(raw, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("the channel being merged is not a valid envelope: %w", err)
	}
	if previous.ReleaseID != releaseID || previous.Sequence != sequence ||
		previous.SecurityEpoch != securityEpoch || previous.KeyID != keyID {
		for _, existing := range previous.Components {
			if existing.Name != component || existing.Platform != platform {
				return nil, fmt.Errorf("cannot carry %s/%s from release %s into %s: every component manifest must match the new envelope identity", existing.Name, existing.Platform, previous.ReleaseID, releaseID)
			}
		}
		// Replacing the only matching platform needs no carried entry.
		return components, nil
	}
	for _, existing := range previous.Components {
		if existing.Name == component && existing.Platform == platform {
			continue
		}
		components = append(components, existing)
	}
	sort.Slice(components, func(i, j int) bool {
		if components[i].Name != components[j].Name {
			return components[i].Name < components[j].Name
		}
		return components[i].Platform < components[j].Platform
	})
	return components, nil
}

// writeNew refuses to overwrite. A release is immutable, and the server release
// script guards the same way: a rebuilt release that quietly replaces a signed
// one is how two different binaries end up sharing an identifier.
func writeNew(path string, data []byte) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("refusing to overwrite %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func marshal(value any) ([]byte, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}
