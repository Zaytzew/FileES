package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"filees/internal/androidrelease"
	"filees/internal/releaseenvelope"
	"filees/internal/serverinstall/svnfetch"
)

// errAndroidUnpublished is not a failure of the desktop page. The companion
// channel simply has not been signed yet.
var errAndroidUnpublished = errors.New("android channel is not published")

type androidState struct {
	ReleaseID      string `json:"release_id"`
	Sequence       uint64 `json:"sequence"`
	SecurityEpoch  uint64 `json:"security_epoch"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

// publishAndroid mirrors channels/android.json and its APK into their own
// directory. It does not read the desktop envelope or the server channel.
func publishAndroid(ctx context.Context, fetcher svnfetch.Fetcher, verifier releaseenvelope.SignatureVerifier, keyID, outDir, statePath string) (bool, error) {
	channelBody, err := fetcher.Cat(ctx, androidrelease.ChannelPath)
	if err != nil {
		if unpublished(err) {
			return false, errAndroidUnpublished
		}
		return false, err
	}
	channelSig, err := fetcher.Cat(ctx, androidrelease.ChannelPath+".sig")
	if err != nil {
		return false, err
	}
	if err := verifier.Verify(ctx, keyID, channelBody, channelSig); err != nil {
		return false, fmt.Errorf("verify android channel: %w", err)
	}
	channel, err := androidrelease.ParseChannel(channelBody)
	if err != nil {
		return false, err
	}
	previous, err := loadAndroidState(statePath)
	if err != nil {
		return false, err
	}
	if previous != nil && (channel.SecurityEpoch < previous.SecurityEpoch || (channel.SecurityEpoch == previous.SecurityEpoch && channel.Sequence < previous.Sequence)) {
		return false, fmt.Errorf("refusing android rollback from %s to %s", previous.ReleaseID, channel.ReleaseID)
	}
	manifestBody, err := fetcher.Cat(ctx, channel.Manifest)
	if err != nil {
		return false, err
	}
	manifestSig, err := fetcher.Cat(ctx, channel.Manifest+".sig")
	if err != nil {
		return false, err
	}
	if err := verifier.Verify(ctx, keyID, manifestBody, manifestSig); err != nil {
		return false, fmt.Errorf("verify android manifest: %w", err)
	}
	manifest, err := androidrelease.ParseManifest(manifestBody)
	if err != nil {
		return false, err
	}
	if manifest.ReleaseID != channel.ReleaseID || manifest.Sequence != channel.Sequence || manifest.SecurityEpoch != channel.SecurityEpoch {
		return false, fmt.Errorf("android channel and manifest do not name the same release")
	}
	apkPath := path.Join(path.Dir(channel.Manifest), manifest.APK.Source)
	apk, err := fetcher.Cat(ctx, apkPath)
	if err != nil {
		return false, fmt.Errorf("fetch android apk: %w", err)
	}
	if int64(len(apk)) != manifest.APK.Size {
		return false, fmt.Errorf("android apk size mismatch: got %d, signed manifest says %d", len(apk), manifest.APK.Size)
	}
	sum := sha256.Sum256(apk)
	if hex.EncodeToString(sum[:]) != manifest.APK.SHA256 {
		return false, errors.New("android apk does not match the signed manifest")
	}
	manifestSHA := sha256.Sum256(manifestBody)
	next := androidState{ReleaseID: channel.ReleaseID, Sequence: channel.Sequence, SecurityEpoch: channel.SecurityEpoch, ManifestSHA256: hex.EncodeToString(manifestSHA[:])}
	if previous != nil && previous.ManifestSHA256 == next.ManifestSHA256 {
		onDisk, err := os.ReadFile(filepath.Join(outDir, filepath.FromSlash(apkPath)))
		if err == nil && int64(len(onDisk)) == manifest.APK.Size && sha256Equal(onDisk, manifest.APK.SHA256) {
			return false, nil
		}
	}
	files := map[string][]byte{
		androidrelease.ChannelPath:          channelBody,
		androidrelease.ChannelPath + ".sig": channelSig,
		channel.Manifest:                    manifestBody,
		channel.Manifest + ".sig":           manifestSig,
		apkPath:                             apk,
	}
	if err := replaceDirectory(outDir, files); err != nil {
		return false, err
	}
	if err := saveAndroidState(statePath, next); err != nil {
		return false, err
	}
	return true, nil
}

func unpublished(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "160013") || strings.Contains(text, "not found") || strings.Contains(text, "no such file")
}

func sha256Equal(data []byte, want string) bool {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) == want
}

func pathJoin(dir, name string) string {
	return dir + "/" + name
}

func loadAndroidState(statePath string) (*androidState, error) {
	data, err := os.ReadFile(statePath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state androidState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("read android state %s: %w", statePath, err)
	}
	return &state, nil
}

func saveAndroidState(statePath string, state androidState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return err
	}
	temp := statePath + ".tmp"
	if err := os.WriteFile(temp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(temp, statePath)
}
