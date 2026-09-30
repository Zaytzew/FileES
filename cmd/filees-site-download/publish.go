package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// maxInstallerSize bounds what one run will hold in memory. The installer is
// about 20 MB; a manifest naming something far larger is not an installer.
const maxInstallerSize = 512 << 20

// Config is landing/download.json: which signed channel the download page
// follows, and the per-release notes shown on it.
type Config struct {
	Repo      string `json:"repo"`
	Channel   string `json:"channel"`
	Component string `json:"component"`
	// Platform is the single platform of a one-platform page; Platforms is
	// the list a page offering several downloads of one release uses. Exactly
	// one of the two is configured.
	Platform     string                  `json:"platform,omitempty"`
	Platforms    []string                `json:"platforms,omitempty"`
	KeyID        string                  `json:"key_id"`
	Notes        map[string]ReleaseNotes `json:"notes"`
	VersionNotes map[string]ReleaseNotes `json:"version_notes,omitempty"`
	Server       *ServerConfig           `json:"server,omitempty"`
}

// ReleaseNotes is the optional highlighted sentence for one release.
type ReleaseNotes struct {
	PL string `json:"pl"`
	EN string `json:"en"`
}

// releaseNotes uses the offered manifest version, never the website's source
// version. An exact release entry overrides the product-version fallback.
func (c Config) releaseNotes(releaseID, version string) ReleaseNotes {
	if notes, ok := c.Notes[releaseID]; ok {
		return notes
	}
	match := regexp.MustCompile(`^([0-9]+\.[0-9]+\.[0-9]+)(?:\.(?:r)?[0-9]+|\+r[0-9]+)?$`).FindStringSubmatch(version)
	if match == nil {
		return ReleaseNotes{}
	}
	return c.VersionNotes[match[1]]
}

func notesCaption(prefix, note string) string {
	if note = strings.TrimSpace(note); note != "" {
		return prefix + note
	}
	return ""
}

// Dater reports when a repository path last changed. The signing commit of a
// manifest signature is the honest "signed on" date; nothing inside the signed
// files carries one.
type Dater interface {
	LastChanged(ctx context.Context, repoPath string) (time.Time, error)
}

// State is what the previous successful publication was. It exists so that a
// channel moved backwards — by mistake or by someone with write access to the
// release repository — cannot take the download page back to an older release.
type State struct {
	ReleaseID     string                    `json:"release_id"`
	Sequence      uint64                    `json:"sequence"`
	SecurityEpoch uint64                    `json:"security_epoch"`
	Installers    map[string]StateInstaller `json:"installers,omitempty"`
	Server        *ServerState              `json:"server,omitempty"`
	// NotesSince is the sequence of the release published before this one;
	// the "what's new" list counts from there.
	NotesSince uint64 `json:"notes_since,omitempty"`
}

// StateInstaller is what one platform published last.
type StateInstaller struct {
	Source string `json:"source"`
	SHA256 string `json:"sha256"`
}

// Publisher turns the release currently promoted on a signed channel into a
// static download directory: the installer, SHA256SUMS and index.html.
type Publisher struct {
	Resolver  *releaseenvelope.Resolver
	Fetcher   releaseenvelope.Fetcher
	Dater     Dater
	Config    Config
	Template  []byte
	OutDir    string
	StatePath string
}

// Result says what a run did.
type Result struct {
	Changed    bool
	ReleaseID  string
	Version    string
	Installers []string
}

var placeholderPattern = regexp.MustCompile(`\{\{[A-Z_0-9]+\}\}`)

// installerNamePatterns is what each platform's installer may be called. A
// page that offers a download names the file it offers, so an artifact of an
// unexpected shape is a refusal rather than a link nobody can check.
var installerNamePatterns = map[string]*regexp.Regexp{
	"windows-amd64": regexp.MustCompile(`^filees-[0-9][0-9.]*\.msi$`),
	"linux-amd64":   regexp.MustCompile(`^FileES-[0-9][0-9.]*-x86_64\.AppImage$`),
}

// platformDownload is one platform's verified installer within one release.
type platformDownload struct {
	Platform  string
	Manifest  string
	Version   string
	Installer releaseenvelope.Artifact
	SignedAt  time.Time
}

// platforms is the configured list: several platforms of one release, or the
// single one a one-platform page still configures.
func (c Config) platforms() []string {
	if len(c.Platforms) > 0 {
		return c.Platforms
	}
	if c.Platform != "" {
		return []string{c.Platform}
	}
	return nil
}

// Publish resolves, verifies and — only if something changed — replaces the
// output directory. On any error the previous publication stays as it was.
func (p Publisher) Publish(ctx context.Context) (Result, error) {
	if p.Resolver == nil || p.Fetcher == nil || p.Dater == nil {
		return Result{}, errors.New("publisher is incomplete")
	}
	if !filepath.IsAbs(p.OutDir) || !filepath.IsAbs(p.StatePath) {
		return Result{}, errors.New("output directory and state path must be absolute")
	}
	wanted := p.Config.platforms()
	if len(wanted) == 0 {
		return Result{}, errors.New("no platform is configured")
	}
	channelPath := "channels/" + p.Config.Channel + ".v2.json"
	var downloads []platformDownload
	var envelope *releaseenvelope.Envelope
	for _, platform := range wanted {
		resolved, err := p.Resolver.Resolve(ctx, channelPath, p.Config.Component, platform)
		if err != nil {
			return Result{}, err
		}
		// Every platform on one page must come from one release: the envelope
		// is a release-level document, and a page mixing two of them would
		// offer downloads that never existed together.
		if len(downloads) > 0 && resolved.Envelope.ReleaseID != envelope.ReleaseID {
			return Result{}, fmt.Errorf("channel %s resolves %s to release %s and %s to %s", p.Config.Channel, downloads[0].Platform, envelope.ReleaseID, platform, resolved.Envelope.ReleaseID)
		}
		envelope = resolved.Envelope
		installer, err := selectInstaller(platform, resolved.Manifest.Artifacts)
		if err != nil {
			return Result{}, err
		}
		signaturePath := resolved.Component.Manifest + ".sig"
		signedAt, err := p.Dater.LastChanged(ctx, signaturePath)
		if err != nil {
			return Result{}, fmt.Errorf("read signing date of %s: %w", signaturePath, err)
		}
		downloads = append(downloads, platformDownload{
			Platform: platform, Manifest: resolved.Component.Manifest,
			Version: resolved.Manifest.Version, Installer: installer, SignedAt: signedAt,
		})
	}
	sort.Slice(downloads, func(i, j int) bool { return downloads[i].Platform < downloads[j].Platform })

	previous, err := loadState(p.StatePath)
	if err != nil {
		return Result{}, err
	}
	if previous != nil {
		if envelope.SecurityEpoch < previous.SecurityEpoch ||
			(envelope.SecurityEpoch == previous.SecurityEpoch && envelope.Sequence < previous.Sequence) {
			return Result{}, fmt.Errorf("channel %s points at %s (sequence %d, epoch %d), older than the published %s (sequence %d, epoch %d); refusing to go back",
				p.Config.Channel, envelope.ReleaseID, envelope.Sequence, envelope.SecurityEpoch,
				previous.ReleaseID, previous.Sequence, previous.SecurityEpoch)
		}
	}
	server, err := p.serverDownload(ctx, previous)
	if err != nil {
		return Result{}, err
	}
	notes, err := signedNotes(ctx, p.Fetcher, p.Resolver.Verifier, p.Config.KeyID, envelope.ReleaseID, envelope.Sequence, p.Config.Component)
	if err != nil {
		return Result{}, err
	}
	var since uint64
	if previous != nil {
		since = notesSince(previous.ReleaseID, previous.Sequence, previous.NotesSince, envelope.ReleaseID)
	}
	// One list for what both desktop platforms share, one per platform for
	// what only it gets, one for the server card. Each is empty without notes.
	desktopNew := selection(notes, since, "desktop")
	cards := map[string]string{
		"DESKTOP_WHATS_NEW_PL": whatsNewHTML(desktopNew, "pl"),
		"DESKTOP_WHATS_NEW_EN": whatsNewHTML(desktopNew, "en"),
	}
	lists := []releasenotes.Selection{desktopNew}
	for _, download := range downloads {
		scope, _, _ := strings.Cut(download.Platform, "-")
		platformNew := selection(notes, since, scope)
		prefix := placeholderPrefix(download.Platform) + "_"
		cards[prefix+"WHATS_NEW_PL"] = whatsNewHTML(platformNew, "pl")
		cards[prefix+"WHATS_NEW_EN"] = whatsNewHTML(platformNew, "en")
		lists = append(lists, platformNew)
	}
	if server != nil {
		cards["SERVER_WHATS_NEW_PL"] = whatsNewHTML(server.whatsNew, "pl")
		cards["SERVER_WHATS_NEW_EN"] = whatsNewHTML(server.whatsNew, "en")
		lists = append(lists, server.whatsNew)
	}
	// Each invocation publishes one independently verified channel. The label
	// follows that configuration, never the revision or another channel's state.
	template := []byte(strings.ReplaceAll(string(p.Template), "{{CHANNEL}}", html.EscapeString(p.Config.Channel)))
	if server != nil {
		template = server.render(template)
	}
	page, err := renderPage(template, envelope, downloads, p.Config.releaseNotes(envelope.ReleaseID, downloads[0].Version), cards)
	if err != nil {
		return Result{}, err
	}
	allDownloads := append([]platformDownload(nil), downloads...)
	if server != nil {
		allDownloads = append(allDownloads, server.download)
	}
	sums := checksumFile(allDownloads)
	result := Result{ReleaseID: envelope.ReleaseID, Version: downloads[0].Version}
	// Public header metadata travels with the verified publication, not the
	// private rollback state or the source revision of the landing page.
	metadata, err := json.Marshal(struct {
		Channel   string         `json:"channel"`
		ReleaseID string         `json:"release_id"`
		Version   string         `json:"version"`
		WhatsNew  []metadataItem `json:"whats_new,omitempty"`
	}{p.Config.Channel, result.ReleaseID, result.Version, metadataItems(lists...)})
	if err != nil {
		return Result{}, err
	}
	for _, download := range allDownloads {
		result.Installers = append(result.Installers, download.Installer.Source)
	}

	if upToDate(p.OutDir, allDownloads, page, sums, metadata) {
		return result, p.saveState(envelope, downloads, server, since)
	}

	files := map[string][]byte{"SHA256SUMS": sums, "index.html": page, "release.json": metadata}
	for _, download := range downloads {
		data, err := p.Fetcher.Cat(ctx, path.Join(path.Dir(download.Manifest), download.Installer.Source))
		if err != nil {
			return Result{}, fmt.Errorf("fetch installer for %s: %w", download.Platform, err)
		}
		if int64(len(data)) != download.Installer.Size {
			return Result{}, fmt.Errorf("installer size mismatch for %s: got %d, signed manifest says %d", download.Platform, len(data), download.Installer.Size)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != download.Installer.SHA256 {
			return Result{}, fmt.Errorf("installer for %s does not match the signed manifest", download.Platform)
		}
		files[download.Installer.Source] = data
	}
	if server != nil {
		files[server.download.Installer.Source] = server.data
	}
	if err := replaceDirectory(p.OutDir, files); err != nil {
		return Result{}, err
	}
	result.Changed = true
	return result, p.saveState(envelope, downloads, server, since)
}

// checksumFile lists every offered installer, in the order the page shows them.
func checksumFile(downloads []platformDownload) []byte {
	var out bytes.Buffer
	for _, download := range downloads {
		out.WriteString(download.Installer.SHA256 + "  " + download.Installer.Source + "\n")
	}
	return out.Bytes()
}

func selectInstaller(platform string, artifacts []releaseenvelope.Artifact) (releaseenvelope.Artifact, error) {
	pattern, known := installerNamePatterns[platform]
	if !known {
		return releaseenvelope.Artifact{}, fmt.Errorf("platform %q has no known installer name", platform)
	}
	var found []releaseenvelope.Artifact
	for _, artifact := range artifacts {
		if artifact.Kind == "installer" {
			found = append(found, artifact)
		}
	}
	if len(found) != 1 {
		return releaseenvelope.Artifact{}, fmt.Errorf("signed manifest for %s names %d installers, want exactly one", platform, len(found))
	}
	installer := found[0]
	if !pattern.MatchString(installer.Source) {
		return releaseenvelope.Artifact{}, fmt.Errorf("installer %q of %s does not match %s", installer.Source, platform, pattern)
	}
	if installer.Size > maxInstallerSize {
		return releaseenvelope.Artifact{}, fmt.Errorf("installer size %d exceeds %d", installer.Size, maxInstallerSize)
	}
	return installer, nil
}

// placeholderPrefix turns a platform into the prefix its placeholders carry:
// windows-amd64 becomes WINDOWS_AMD64.
func placeholderPrefix(platform string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - ('a' - 'A')
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			return r
		default:
			return '_'
		}
	}, platform)
}

// cards are the "what's new" lists, already HTML built by whatsNewHTML from
// escaped text; every other value is text and is escaped here.
func renderPage(template []byte, envelope *releaseenvelope.Envelope, downloads []platformDownload, notes ReleaseNotes, cards map[string]string) ([]byte, error) {
	signedAt := downloads[0].SignedAt
	values := map[string]string{
		"VERSION":    downloads[0].Version,
		"RELEASE_ID": envelope.ReleaseID,
		"NOTES_PL":   notesCaption("Co nowego w tej wersji: ", notes.PL),
		"NOTES_EN":   notesCaption("What’s new in this version: ", notes.EN),
	}
	for _, download := range downloads {
		// One release, one version. Two platforms disagreeing about it would
		// mean the envelope carries manifests that were never built together.
		if download.Version != downloads[0].Version {
			return nil, fmt.Errorf("release %s has version %s for %s and %s for %s", envelope.ReleaseID, downloads[0].Version, downloads[0].Platform, download.Version, download.Platform)
		}
		if download.SignedAt.After(signedAt) {
			signedAt = download.SignedAt
		}
		megabytes := float64(download.Installer.Size) / (1 << 20)
		size := strconv.FormatFloat(megabytes, 'f', 1, 64)
		prefix := placeholderPrefix(download.Platform) + "_"
		values[prefix+"FILE"] = download.Installer.Source
		values[prefix+"SHA256"] = download.Installer.SHA256
		values[prefix+"SIZE_PL"] = strings.Replace(size, ".", ",", 1) + " MB"
		values[prefix+"SIZE_EN"] = size + " MB"
	}
	values["SIGNED_DATE"] = signedAt.UTC().Format("2006-01-02")
	page := string(template)
	for key, value := range values {
		page = strings.ReplaceAll(page, "{{"+key+"}}", html.EscapeString(value))
	}
	for key, value := range cards {
		page = strings.ReplaceAll(page, "{{"+key+"}}", value)
	}
	// A release without notes leaves no empty highlighted box behind.
	page = regexp.MustCompile(`\s*<p class="note"></p>`).ReplaceAllString(page, "")
	if leftover := placeholderPattern.FindString(page); leftover != "" {
		return nil, fmt.Errorf("download template placeholder not filled: %s", leftover)
	}
	return []byte(page), nil
}

func upToDate(dir string, downloads []platformDownload, page, sums, metadata []byte) bool {
	currentMetadata, err := os.ReadFile(filepath.Join(dir, "release.json"))
	if err != nil || !bytes.Equal(currentMetadata, metadata) {
		return false
	}
	current, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil || !bytes.Equal(current, page) {
		return false
	}
	currentSums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil || !bytes.Equal(currentSums, sums) {
		return false
	}
	for _, download := range downloads {
		data, err := os.ReadFile(filepath.Join(dir, download.Installer.Source))
		if err != nil || int64(len(data)) != download.Installer.Size {
			return false
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != download.Installer.SHA256 {
			return false
		}
	}
	return true
}

// replaceDirectory builds the new publication next to the old one and swaps
// the two with renames, so a reader sees either the old directory or the new
// one, never a half-written page next to a missing installer. Only this
// program's own sibling names are ever removed.
func replaceDirectory(dir string, files map[string][]byte) error {
	parent := filepath.Dir(dir)
	base := filepath.Base(dir)
	staging, err := os.MkdirTemp(parent, "."+base+".new-")
	if err != nil {
		return fmt.Errorf("prepare publication: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(staging)
		}
	}()
	if err := os.Chmod(staging, 0o755); err != nil {
		return err
	}
	stagingRoot := filepath.Clean(staging)
	for name, data := range files {
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			return fmt.Errorf("publication path %q escapes the directory", name)
		}
		target := filepath.Join(stagingRoot, filepath.FromSlash(name))
		if rel, err := filepath.Rel(stagingRoot, target); err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("publication path %q escapes the directory", name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("prepare %s: %w", name, err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	previous := filepath.Join(parent, "."+base+".previous")
	if err := os.RemoveAll(previous); err != nil {
		return err
	}
	hadPrevious := false
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", dir)
		}
		if err := os.Rename(dir, previous); err != nil {
			return fmt.Errorf("move previous publication aside: %w", err)
		}
		hadPrevious = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(staging, dir); err != nil {
		if hadPrevious {
			_ = os.Rename(previous, dir)
		}
		return fmt.Errorf("publish new directory: %w", err)
	}
	keep = true
	if hadPrevious {
		_ = os.RemoveAll(previous)
	}
	return nil
}

func loadState(statePath string) (*State, error) {
	data, err := os.ReadFile(statePath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("read state %s: %w", statePath, err)
	}
	return &state, nil
}

func (p Publisher) saveState(envelope *releaseenvelope.Envelope, downloads []platformDownload, server *serverBundle, since uint64) error {
	state := State{ReleaseID: envelope.ReleaseID, Sequence: envelope.Sequence, SecurityEpoch: envelope.SecurityEpoch, Installers: map[string]StateInstaller{}, NotesSince: since}
	for _, download := range downloads {
		state.Installers[download.Platform] = StateInstaller{Source: download.Installer.Source, SHA256: download.Installer.SHA256}
	}
	if server != nil {
		state.Server = &server.state
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temp := p.StatePath + ".tmp"
	if err := os.WriteFile(temp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(temp, p.StatePath)
}
