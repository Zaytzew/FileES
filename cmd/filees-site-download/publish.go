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
	"strconv"
	"strings"
	"time"

	"filees/internal/releaseenvelope"
)

// maxInstallerSize bounds what one run will hold in memory. The installer is
// about 20 MB; a manifest naming something far larger is not an installer.
const maxInstallerSize = 512 << 20

// Config is landing/download.json: which signed channel the download page
// follows, and the per-release notes shown on it.
type Config struct {
	Repo      string                  `json:"repo"`
	Channel   string                  `json:"channel"`
	Component string                  `json:"component"`
	Platform  string                  `json:"platform"`
	KeyID     string                  `json:"key_id"`
	Notes     map[string]ReleaseNotes `json:"notes"`
}

// ReleaseNotes is the optional highlighted sentence for one release.
type ReleaseNotes struct {
	PL string `json:"pl"`
	EN string `json:"en"`
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
	ReleaseID     string `json:"release_id"`
	Sequence      uint64 `json:"sequence"`
	SecurityEpoch uint64 `json:"security_epoch"`
	Installer     string `json:"installer"`
	SHA256        string `json:"sha256"`
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
	Changed   bool
	ReleaseID string
	Version   string
	Installer string
	SHA256    string
}

var placeholderPattern = regexp.MustCompile(`\{\{[A-Z_]+\}\}`)
var installerNamePattern = regexp.MustCompile(`^filees-[0-9][0-9.]*\.msi$`)

// Publish resolves, verifies and — only if something changed — replaces the
// output directory. On any error the previous publication stays as it was.
func (p Publisher) Publish(ctx context.Context) (Result, error) {
	if p.Resolver == nil || p.Fetcher == nil || p.Dater == nil {
		return Result{}, errors.New("publisher is incomplete")
	}
	if !filepath.IsAbs(p.OutDir) || !filepath.IsAbs(p.StatePath) {
		return Result{}, errors.New("output directory and state path must be absolute")
	}
	channelPath := "channels/" + p.Config.Channel + ".v2.json"
	resolved, err := p.Resolver.Resolve(ctx, channelPath, p.Config.Component, p.Config.Platform)
	if err != nil {
		return Result{}, err
	}
	envelope := resolved.Envelope
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
	installer, err := selectInstaller(resolved.Manifest.Artifacts)
	if err != nil {
		return Result{}, err
	}
	signaturePath := resolved.Component.Manifest + ".sig"
	signedAt, err := p.Dater.LastChanged(ctx, signaturePath)
	if err != nil {
		return Result{}, fmt.Errorf("read signing date of %s: %w", signaturePath, err)
	}
	page, err := renderPage(p.Template, resolved, installer, signedAt, p.Config.Notes[envelope.ReleaseID])
	if err != nil {
		return Result{}, err
	}
	sums := []byte(installer.SHA256 + "  " + installer.Source + "\n")
	result := Result{ReleaseID: envelope.ReleaseID, Version: resolved.Manifest.Version, Installer: installer.Source, SHA256: installer.SHA256}

	if upToDate(p.OutDir, installer, page, sums) {
		return result, p.saveState(resolved, installer)
	}

	data, err := p.Fetcher.Cat(ctx, path.Join(path.Dir(resolved.Component.Manifest), installer.Source))
	if err != nil {
		return Result{}, fmt.Errorf("fetch installer: %w", err)
	}
	if int64(len(data)) != installer.Size {
		return Result{}, fmt.Errorf("installer size mismatch: got %d, signed manifest says %d", len(data), installer.Size)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != installer.SHA256 {
		return Result{}, errors.New("installer SHA-256 does not match the signed manifest")
	}
	if err := replaceDirectory(p.OutDir, map[string][]byte{
		installer.Source: data,
		"SHA256SUMS":     sums,
		"index.html":     page,
	}); err != nil {
		return Result{}, err
	}
	result.Changed = true
	return result, p.saveState(resolved, installer)
}

func selectInstaller(artifacts []releaseenvelope.Artifact) (releaseenvelope.Artifact, error) {
	var found []releaseenvelope.Artifact
	for _, artifact := range artifacts {
		if artifact.Kind == "installer" {
			found = append(found, artifact)
		}
	}
	if len(found) != 1 {
		return releaseenvelope.Artifact{}, fmt.Errorf("signed manifest names %d installers, want exactly one", len(found))
	}
	installer := found[0]
	if !installerNamePattern.MatchString(installer.Source) {
		return releaseenvelope.Artifact{}, fmt.Errorf("installer %q is not a filees-<version>.msi", installer.Source)
	}
	if installer.Size > maxInstallerSize {
		return releaseenvelope.Artifact{}, fmt.Errorf("installer size %d exceeds %d", installer.Size, maxInstallerSize)
	}
	return installer, nil
}

func renderPage(template []byte, resolved *releaseenvelope.Resolved, installer releaseenvelope.Artifact, signedAt time.Time, notes ReleaseNotes) ([]byte, error) {
	megabytes := float64(installer.Size) / (1 << 20)
	size := strconv.FormatFloat(megabytes, 'f', 1, 64)
	values := map[string]string{
		"VERSION":     resolved.Manifest.Version,
		"RELEASE_ID":  resolved.Envelope.ReleaseID,
		"SIGNED_DATE": signedAt.UTC().Format("2006-01-02"),
		"MSI_FILE":    installer.Source,
		"SHA256":      installer.SHA256,
		"SIZE_PL":     strings.Replace(size, ".", ",", 1) + " MB",
		"SIZE_EN":     size + " MB",
		"NOTES_PL":    strings.TrimSpace(notes.PL),
		"NOTES_EN":    strings.TrimSpace(notes.EN),
	}
	page := string(template)
	for key, value := range values {
		page = strings.ReplaceAll(page, "{{"+key+"}}", html.EscapeString(value))
	}
	// A release without notes leaves no empty highlighted box behind.
	page = regexp.MustCompile(`\s*<p class="note"></p>`).ReplaceAllString(page, "")
	if leftover := placeholderPattern.FindString(page); leftover != "" {
		return nil, fmt.Errorf("download template placeholder not filled: %s", leftover)
	}
	return []byte(page), nil
}

func upToDate(dir string, installer releaseenvelope.Artifact, page, sums []byte) bool {
	current, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil || !bytes.Equal(current, page) {
		return false
	}
	currentSums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil || !bytes.Equal(currentSums, sums) {
		return false
	}
	data, err := os.ReadFile(filepath.Join(dir, installer.Source))
	if err != nil || int64(len(data)) != installer.Size {
		return false
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]) == installer.SHA256
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
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(staging, name), data, 0o644); err != nil {
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

func (p Publisher) saveState(resolved *releaseenvelope.Resolved, installer releaseenvelope.Artifact) error {
	data, err := json.MarshalIndent(State{
		ReleaseID: resolved.Envelope.ReleaseID, Sequence: resolved.Envelope.Sequence, SecurityEpoch: resolved.Envelope.SecurityEpoch,
		Installer: installer.Source, SHA256: installer.SHA256,
	}, "", "  ")
	if err != nil {
		return err
	}
	temp := p.StatePath + ".tmp"
	if err := os.WriteFile(temp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(temp, p.StatePath)
}
