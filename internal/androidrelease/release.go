// Package androidrelease is the signed channel for the Android companion.
// It is a separate track from the desktop v2 envelope and from the OpenBSD
// server channel: its own file, its own sequence, the same release key.
package androidrelease

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ChannelPath is the promoted document in FILEES-BIN and on the public mirror.
const ChannelPath = "channels/android.json"

// Channel is the schema-1 pointer, the same shape as the server channel but
// a different file, so promoting it cannot replace channels/beta.json.
type Channel struct {
	SchemaVersion int    `json:"schema_version"`
	ReleaseID     string `json:"release_id"`
	Manifest      string `json:"manifest"`
	Sequence      uint64 `json:"sequence"`
	SecurityEpoch uint64 `json:"security_epoch"`
}

// Manifest describes the one APK of that release. It is not an installer
// manifest for OpenBSD and not a desktop component.
type Manifest struct {
	SchemaVersion int    `json:"schema_version"`
	ReleaseID     string `json:"release_id"`
	Platform      string `json:"platform"`
	Sequence      uint64 `json:"sequence"`
	SecurityEpoch uint64 `json:"security_epoch"`
	Version       string `json:"version"`
	APK           APK    `json:"apk"`
}

// APK is the file that sits next to the manifest.
type APK struct {
	Source string `json:"source"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func ParseChannel(data []byte) (Channel, error) {
	var channel Channel
	if err := decode(data, &channel); err != nil {
		return Channel{}, fmt.Errorf("android channel: %w", err)
	}
	if channel.SchemaVersion != 1 || channel.Sequence == 0 || channel.SecurityEpoch == 0 {
		return Channel{}, fmt.Errorf("android channel is not schema 1 with a positive sequence")
	}
	if channel.ReleaseID == "" || !strings.HasPrefix(channel.Manifest, "releases/") || !strings.HasSuffix(channel.Manifest, "/android/manifest.json") || strings.Contains(channel.Manifest, "..") {
		return Channel{}, fmt.Errorf("android channel manifest path is invalid")
	}
	return channel, nil
}

func ParseManifest(data []byte) (Manifest, error) {
	var manifest Manifest
	if err := decode(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("android manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 || manifest.Platform != "android" || manifest.Sequence == 0 || manifest.SecurityEpoch == 0 {
		return Manifest{}, fmt.Errorf("android manifest is not a schema 1 android release")
	}
	apk := manifest.APK
	if manifest.Version == "" || apk.Size <= 0 || len(apk.SHA256) != 64 || strings.Contains(apk.Source, "/") || !strings.HasSuffix(apk.Source, ".apk") {
		return Manifest{}, fmt.Errorf("android manifest apk is invalid")
	}
	return manifest, nil
}

func decode(data []byte, out any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}
