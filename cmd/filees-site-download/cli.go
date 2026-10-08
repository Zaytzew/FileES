package main

import (
	"context"
	"fmt"
	"html"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"filees/internal/releaseenvelope"
	"filees/internal/releasenotes"
)

// CLIConfig follows the standalone command-line client's own channel. Its
// card is the template block between <!--CLI--> markers.
type CLIConfig struct {
	Channel   string   `json:"channel"`
	Platforms []string `json:"platforms"`
}

// CLIState keeps the standalone client's freshness apart from the desktop's:
// the two are released on different days from different tracks.
type CLIState struct {
	ReleaseID     string `json:"release_id"`
	Sequence      uint64 `json:"sequence"`
	SecurityEpoch uint64 `json:"security_epoch"`
	NotesSince    uint64 `json:"notes_since,omitempty"`
}

type cliBundle struct {
	state     CLIState
	version   string
	downloads []platformDownload
	data      map[string][]byte
	whatsNew  releasenotes.Selection
}

// cliArchivePattern is what the signed manifest may call the archive.
var cliArchivePattern = regexp.MustCompile(`^filees-client-[a-z0-9]+-[a-z0-9]+\.tar\.gz$`)

// cliBlock is the standalone client's card; it leaves the page whole while
// that track has no signed release, or is not configured for a page.
var cliBlock = regexp.MustCompile(`(?s)[ \t]*<!--CLI-->.*?<!--/CLI-->\n?`)

func cliPlatform(platform string) bool {
	return platform == "linux-amd64" || platform == "openbsd-amd64"
}

// cliDownload resolves the standalone client exactly as a desktop release is
// resolved - signed envelope, signed manifest, identity binding, expiry - and
// offers the signed archive itself, byte for byte, under a name that carries
// the release. A channel that does not exist yet is not an error.
func (p Publisher) cliDownload(ctx context.Context, config *CLIConfig, old *CLIState) (*cliBundle, error) {
	if config == nil {
		return nil, nil
	}
	if !identifier.MatchString(config.Channel) || len(config.Platforms) == 0 {
		return nil, fmt.Errorf("invalid cli channel or platforms")
	}
	channelPath := "channels/" + config.Channel + ".json"
	if _, err := p.Fetcher.Cat(ctx, channelPath); err != nil && unpublished(err) {
		return nil, nil
	}
	bundle := &cliBundle{data: map[string][]byte{}}
	var envelope *releaseenvelope.Envelope
	var signedAt time.Time
	for _, platform := range config.Platforms {
		if !cliPlatform(platform) {
			return nil, fmt.Errorf("cli platform %q is not offered", platform)
		}
		resolved, err := p.Resolver.Resolve(ctx, channelPath, "cli", platform)
		if err != nil {
			return nil, err
		}
		if envelope != nil && (resolved.Envelope.ReleaseID != envelope.ReleaseID || resolved.Manifest.Version != bundle.version) {
			return nil, fmt.Errorf("cli channel %s offers %s from two releases or versions", config.Channel, platform)
		}
		envelope, bundle.version = resolved.Envelope, resolved.Manifest.Version
		if len(resolved.Manifest.Artifacts) != 1 || resolved.Manifest.Artifacts[0].Kind != "bundle" {
			return nil, fmt.Errorf("signed cli manifest for %s must name exactly one bundle", platform)
		}
		signed := resolved.Manifest.Artifacts[0]
		if !cliArchivePattern.MatchString(signed.Source) || signed.Size > maxInstallerSize {
			return nil, fmt.Errorf("cli archive %q of %s has an unexpected name or size", signed.Source, platform)
		}
		at, err := p.Dater.LastChanged(ctx, resolved.Component.Manifest+".sig")
		if err != nil {
			return nil, fmt.Errorf("read signing date of %s: %w", resolved.Component.Manifest, err)
		}
		if at.After(signedAt) {
			signedAt = at
		}
		name := "FileES-" + envelope.ReleaseID + "-" + platform + ".tar.gz"
		// The publication is the cache: bytes already offered are re-checked
		// against the signed manifest on every run, and fetched only otherwise.
		data, err := os.ReadFile(filepath.Join(p.OutDir, name))
		if err != nil || int64(len(data)) != signed.Size || digest(data) != signed.SHA256 {
			data, err = p.Fetcher.Cat(ctx, path.Join(path.Dir(resolved.Component.Manifest), signed.Source))
			if err != nil {
				return nil, fmt.Errorf("fetch cli archive for %s: %w", platform, err)
			}
			if int64(len(data)) != signed.Size || digest(data) != signed.SHA256 {
				return nil, fmt.Errorf("cli archive for %s does not match the signed manifest", platform)
			}
		}
		bundle.data[name] = data
		bundle.downloads = append(bundle.downloads, platformDownload{
			Platform: platform, Manifest: resolved.Component.Manifest, Version: resolved.Manifest.Version,
			Installer: releaseenvelope.Artifact{Source: name, SHA256: signed.SHA256, Size: signed.Size, Kind: "bundle"},
		})
	}
	if old != nil {
		if envelope.SecurityEpoch < old.SecurityEpoch || (envelope.SecurityEpoch == old.SecurityEpoch && envelope.Sequence < old.Sequence) {
			return nil, fmt.Errorf("refusing cli rollback from %s to %s", old.ReleaseID, envelope.ReleaseID)
		}
	}
	sort.Slice(bundle.downloads, func(i, j int) bool { return bundle.downloads[i].Platform < bundle.downloads[j].Platform })
	for i := range bundle.downloads {
		bundle.downloads[i].SignedAt = signedAt
	}
	bundle.state = CLIState{ReleaseID: envelope.ReleaseID, Sequence: envelope.Sequence, SecurityEpoch: envelope.SecurityEpoch}
	notes, err := signedNotes(ctx, p.Fetcher, p.Resolver.Verifier, p.Config.KeyID, envelope.ReleaseID, envelope.Sequence, "cli")
	if err != nil {
		return nil, err
	}
	if old != nil {
		bundle.state.NotesSince = notesSince(old.ReleaseID, old.Sequence, old.NotesSince, envelope.ReleaseID)
	}
	// The client's notes are its own document; whatever scope an item was
	// written under, it belongs on this one card.
	bundle.whatsNew = selection(notes, bundle.state.NotesSince, "desktop", "windows", "linux", "server", "android")
	return bundle, nil
}

func (b *cliBundle) render(template []byte) []byte {
	values := map[string]string{
		"CLI_VERSION":     b.version,
		"CLI_RELEASE_ID":  b.state.ReleaseID,
		"CLI_SIGNED_DATE": b.downloads[0].SignedAt.UTC().Format("2006-01-02"),
	}
	for _, download := range b.downloads {
		size := strconv.FormatFloat(float64(download.Installer.Size)/(1<<20), 'f', 1, 64)
		prefix := "CLI_" + placeholderPrefix(download.Platform) + "_"
		values[prefix+"FILE"] = download.Installer.Source
		values[prefix+"SHA256"] = download.Installer.SHA256
		values[prefix+"SIZE_EN"] = size + " MB"
		values[prefix+"SIZE_PL"] = strings.Replace(size, ".", ",", 1) + " MB"
	}
	page := string(template)
	for key, value := range values {
		page = strings.ReplaceAll(page, "{{"+key+"}}", html.EscapeString(value))
	}
	page = strings.ReplaceAll(page, "{{CLI_WHATS_NEW_PL}}", whatsNewHTML(b.whatsNew, "pl"))
	page = strings.ReplaceAll(page, "{{CLI_WHATS_NEW_EN}}", whatsNewHTML(b.whatsNew, "en"))
	return []byte(page)
}
