package androidbind

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"filees/internal/releaseenvelope"
)

// The phone follows the same signed beta channel as the public download
// page. filees.space only mirrors the bytes; the signature is checked here
// with the release key pinned in this binary. There is no Play Store.
const (
	updateChannel   = "channels/beta.v2.json"
	updateComponent = "mobile"
	updatePlatform  = "android"
)

var (
	updateBaseURL    = "https://filees.space/download/"
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

// InspectMobileUpdate reads the signed beta channel and looks for the
// mobile/android installer. currentVersionName is this APK's versionName.
// rememberedSequence is the last sequence this install accepted, or 0.
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
	resolver := releaseenvelope.Resolver{
		Fetcher:     fetcher,
		Verifier:    releaseenvelope.Ed25519Verifier{Keys: map[string][]byte{updateKeyID: []byte(releasePublicKey)}},
		TrustedKeys: []string{updateKeyID},
	}
	resolved, err := resolver.Resolve(ctx, updateChannel, updateComponent, updatePlatform)
	if err != nil {
		if strings.Contains(err.Error(), "no component mobile/android") {
			return UpdateOffer{State: "absent", Message: "no android build on this channel"}, nil
		}
		return UpdateOffer{}, err
	}
	if remembered > 0 && resolved.Envelope.Sequence < remembered {
		return UpdateOffer{}, fmt.Errorf("channel sequence %d is older than the installed %d", resolved.Envelope.Sequence, remembered)
	}
	installer, err := androidInstaller(resolved.Manifest.Artifacts)
	if err != nil {
		return UpdateOffer{}, err
	}
	offer := UpdateOffer{
		State:    "available",
		Version:  resolved.Manifest.Version,
		Sequence: resolved.Envelope.Sequence,
		SHA256:   installer.SHA256,
		Size:     installer.Size,
		URL:      fetcher.base + resolved.Component.Manifest[:strings.LastIndex(resolved.Component.Manifest, "/")+1] + installer.Source,
	}
	// Sequence, not versionName: the APK's versionName may contain '+' and
	// the signed manifest version may not. Accepting a sequence means this
	// install already took that release.
	if remembered > 0 && resolved.Envelope.Sequence == remembered {
		offer.State = "current"
		offer.URL = ""
		offer.SHA256 = ""
		offer.Size = 0
	}
	return offer, nil
}

func androidInstaller(artifacts []releaseenvelope.Artifact) (releaseenvelope.Artifact, error) {
	var found []releaseenvelope.Artifact
	for _, artifact := range artifacts {
		if artifact.Kind == "installer" && strings.HasSuffix(artifact.Source, ".apk") {
			found = append(found, artifact)
		}
	}
	if len(found) != 1 {
		return releaseenvelope.Artifact{}, fmt.Errorf("android manifest names %d apk installers, want one", len(found))
	}
	return found[0], nil
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
