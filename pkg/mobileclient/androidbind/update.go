package androidbind

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"filees/internal/androidrelease"
	"filees/internal/releaseenvelope"
)

// The phone has its own signed channel, the way the OpenBSD server has
// channels/beta.json and the desktop has channels/beta.v2.json. It does not
// read the desktop envelope. filees.space only mirrors the bytes; the
// signature is checked here with the release key pinned in this binary.
const updateChannel = androidrelease.ChannelPath

var (
	updateBaseURL    = "https://filees.space/android/"
	updateKeyID      = "release-2026-a"
	releasePublicKey = "untrusted comment: FileES release signing key 2026-08 public key\nRWQD0r1b9z+s3Li93y+G0L7FhxVZntgLyx+qSmWO1OhPl0ifxbbNMEhE\n"
)

// UpdateOffer is what the phone shows and, when an APK is actually signed
// into the channel, what it downloads. State is current, available, absent
// or error. URL is empty unless state is available.
type UpdateOffer struct {
	State    string `json:"state"`
	Message  string `json:"message"`
	Version  string `json:"version,omitempty"`
	Sequence uint64 `json:"sequence,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Size     int64  `json:"size,omitempty"`
	URL      string `json:"url,omitempty"`
}

// InspectMobileUpdate reads the signed Android channel. currentVersionName
// is this APK's versionName. rememberedSequence is the last sequence this
// install accepted, or 0. A missing channel is absent, not a desktop release.
func InspectMobileUpdate(currentVersionName string, rememberedSequence int64) (string, error) {
	offer, err := inspectMobileUpdate(context.Background(), updateBaseURL, currentVersionName, uint64(rememberedSequence))
	if err != nil {
		offer = UpdateOffer{State: "error", Message: err.Error()}
	}
	raw, marshalErr := json.Marshal(offer)
	if marshalErr != nil {
		return "", marshalErr
	}
	return string(raw), nil
}

func inspectMobileUpdate(ctx context.Context, base, currentVersion string, remembered uint64) (UpdateOffer, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	fetcher := httpsFetcher{base: strings.TrimRight(base, "/") + "/"}
	verifier := releaseenvelope.Ed25519Verifier{Keys: map[string][]byte{updateKeyID: []byte(releasePublicKey)}}
	channelBody, err := fetcher.Cat(ctx, updateChannel)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return UpdateOffer{State: "absent", Message: "android channel is not published"}, nil
		}
		return UpdateOffer{}, err
	}
	channelSig, err := fetcher.Cat(ctx, updateChannel+".sig")
	if err != nil {
		return UpdateOffer{}, err
	}
	if err := verifier.Verify(ctx, updateKeyID, channelBody, channelSig); err != nil {
		return UpdateOffer{}, err
	}
	channel, err := androidrelease.ParseChannel(channelBody)
	if err != nil {
		return UpdateOffer{}, err
	}
	if remembered > 0 && channel.Sequence < remembered {
		return UpdateOffer{}, fmt.Errorf("channel sequence %d is older than the installed %d", channel.Sequence, remembered)
	}
	manifestBody, err := fetcher.Cat(ctx, channel.Manifest)
	if err != nil {
		return UpdateOffer{}, err
	}
	manifestSig, err := fetcher.Cat(ctx, channel.Manifest+".sig")
	if err != nil {
		return UpdateOffer{}, err
	}
	if err := verifier.Verify(ctx, updateKeyID, manifestBody, manifestSig); err != nil {
		return UpdateOffer{}, err
	}
	manifest, err := androidrelease.ParseManifest(manifestBody)
	if err != nil {
		return UpdateOffer{}, err
	}
	if manifest.ReleaseID != channel.ReleaseID || manifest.Sequence != channel.Sequence || manifest.SecurityEpoch != channel.SecurityEpoch {
		return UpdateOffer{}, fmt.Errorf("android channel and manifest do not name the same release")
	}
	dir := channel.Manifest
	if i := strings.LastIndex(dir, "/"); i >= 0 {
		dir = dir[:i+1]
	} else {
		dir = ""
	}
	offer := UpdateOffer{
		State:    "available",
		Version:  manifest.Version,
		Sequence: channel.Sequence,
		SHA256:   manifest.APK.SHA256,
		Size:     manifest.APK.Size,
		URL:      fetcher.base + dir + manifest.APK.Source,
	}
	// The manifest version is 0.1.17.1585; the APK says 0.1.17+r1585.
	// Those are the same release. Deciding that before the download avoids
	// pulling the whole APK just to learn we already have it. A remembered
	// sequence is the same fact after an install through this updater.
	if (remembered > 0 && channel.Sequence == remembered) || sameAndroidRelease(currentVersion, manifest.Version) {
		offer.State = "current"
		offer.URL = ""
		offer.SHA256 = ""
		offer.Size = 0
	}
	return offer, nil
}

func sameAndroidRelease(installed, published string) bool {
	installed = canonicalAndroidVersion(installed)
	published = canonicalAndroidVersion(published)
	return installed != "" && installed == published
}

func canonicalAndroidVersion(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "+r", ".")
	value = strings.ReplaceAll(value, "+", ".")
	return value
}

type httpsFetcher struct{ base string }

func (f httpsFetcher) Cat(ctx context.Context, name string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.base+name, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", name, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}
