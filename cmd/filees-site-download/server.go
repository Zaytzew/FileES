package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"filees/internal/releaseenvelope"
	"filees/internal/serverinstall/manifest"
)

// ServerConfig follows the server's v1 channel independently of desktop v2.
type ServerConfig struct {
	Channel  string `json:"channel"`
	Platform string `json:"platform"`
}

// ServerState records independent freshness and a verified local archive cache.
type ServerState struct {
	ReleaseID      string                   `json:"release_id"`
	Sequence       uint64                   `json:"sequence"`
	SecurityEpoch  uint64                   `json:"security_epoch"`
	ManifestSHA256 string                   `json:"manifest_sha256"`
	Artifact       releaseenvelope.Artifact `json:"artifact"`
}

type serverBundle struct {
	state    ServerState
	download platformDownload
	data     []byte
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (p Publisher) signedServerFile(ctx context.Context, name string) ([]byte, []byte, error) {
	data, err := p.Fetcher.Cat(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	sig, err := p.Fetcher.Cat(ctx, name+".sig")
	if err != nil {
		return nil, nil, err
	}
	if p.Resolver.Verifier == nil {
		return nil, nil, fmt.Errorf("server signature verifier is missing")
	}
	if err := p.Resolver.Verifier.Verify(ctx, p.Config.KeyID, data, sig); err != nil {
		return nil, nil, fmt.Errorf("verify server %s: %w", name, err)
	}
	return data, sig, nil
}

func (p Publisher) serverDownload(ctx context.Context, previous *State) (*serverBundle, error) {
	config := p.Config.Server
	if config == nil {
		return nil, nil
	}
	if !manifest.ValidIdentifier(config.Channel) || config.Platform != "openbsd-amd64" {
		return nil, fmt.Errorf("invalid server channel or platform")
	}
	channelData, _, err := p.signedServerFile(ctx, manifest.ChannelPath(config.Channel))
	if err != nil {
		return nil, err
	}
	channel, err := manifest.ParseChannel(channelData)
	if err != nil {
		return nil, err
	}
	manifestPath := manifest.ExpandPlatform(channel.Manifest, config.Platform)
	raw, signature, err := p.signedServerFile(ctx, manifestPath)
	if err != nil {
		return nil, err
	}
	m, err := manifest.Parse(raw)
	if err != nil {
		return nil, err
	}
	if m.ReleaseID != channel.ReleaseID || m.Platform != config.Platform || m.Sequence != channel.Sequence || m.SecurityEpoch != channel.SecurityEpoch {
		return nil, fmt.Errorf("server channel and manifest identity/freshness mismatch")
	}
	if previous != nil && previous.Server != nil {
		old := previous.Server
		if m.SecurityEpoch < old.SecurityEpoch || (m.SecurityEpoch == old.SecurityEpoch && m.Sequence < old.Sequence) {
			return nil, fmt.Errorf("refusing server rollback from %s to %s", old.ReleaseID, m.ReleaseID)
		}
	}
	signedAt, err := p.Dater.LastChanged(ctx, manifestPath+".sig")
	if err != nil {
		return nil, err
	}
	name := "FileES-" + m.ReleaseID + "-" + m.Platform + ".tar.gz"
	bundle := &serverBundle{state: ServerState{ReleaseID: m.ReleaseID, Sequence: m.Sequence, SecurityEpoch: m.SecurityEpoch, ManifestSHA256: digest(raw)}, download: platformDownload{Platform: m.Platform, Manifest: manifestPath, SignedAt: signedAt}}
	// The cache belongs to the publisher, outside the release repository. Recheck
	// its bytes against the digest recorded after verification, on every run.
	if previous != nil && previous.Server != nil {
		old := previous.Server
		if old.ManifestSHA256 == digest(raw) && old.Artifact.Source == name {
			data, err := os.ReadFile(filepath.Join(p.OutDir, name))
			if err == nil && int64(len(data)) == old.Artifact.Size && digest(data) == old.Artifact.SHA256 {
				bundle.data = data
				bundle.state.Artifact = old.Artifact
				bundle.download.Installer = old.Artifact
				return bundle, nil
			}
		}
	}
	files := map[string][]byte{"manifest.json": raw, "manifest.json.sig": signature}
	hashes := map[string]string{}
	total := 0
	for _, f := range m.Files {
		// Release archives contain payloads, never absolute installation targets or
		// set-id modes. The installer applies the signed ownership/mode policy.
		source := strings.TrimSpace(f.Source)
		if source == "manifest.json" || source == "manifest.json.sig" || source == "README.txt" {
			return nil, fmt.Errorf("reserved server archive path %q", source)
		}
		if hash, exists := hashes[source]; exists {
			if hash != strings.ToLower(f.SHA256) {
				return nil, fmt.Errorf("conflicting server hashes for %s", source)
			}
			continue
		}
		data, err := p.Fetcher.Cat(ctx, path.Join(path.Dir(manifestPath), source))
		if err != nil {
			return nil, err
		}
		total += len(data)
		if total > maxInstallerSize {
			return nil, fmt.Errorf("server bundle exceeds size limit")
		}
		if digest(data) != strings.ToLower(f.SHA256) {
			return nil, fmt.Errorf("server payload %s does not match signed manifest", source)
		}
		files[source] = data
		hashes[source] = strings.ToLower(f.SHA256)
	}
	files["README.txt"] = []byte("FileES server " + m.ReleaseID + " / " + m.Platform + "\n\nOriginal release binaries, manifest.json and manifest.json.sig.\nThe website verified the manifest signature and every payload SHA-256.\nThe tar.gz wrapper and this README are generated by the website publisher;\nthey are not separately signed release artifacts.\n\nExtract into a new directory. Do not copy binaries over a running server.\nStart with bin/filees-install --help and the server installation manual:\nhttps://manual.filees.space\nThe installer configures ownership and permissions; the archive does not.\nThis is a binary bundle, not an unattended or offline installation image.\n")
	data, err := serverArchive(m.ReleaseID+"-"+m.Platform, files)
	if err != nil {
		return nil, err
	}
	artifact := releaseenvelope.Artifact{Source: name, SHA256: digest(data), Size: int64(len(data)), Kind: "bundle"}
	bundle.data, bundle.state.Artifact, bundle.download.Installer = data, artifact, artifact
	return bundle, nil
}

// serverArchive is reproducible: sorted paths, fixed timestamps, no host IDs.
func serverArchive(root string, files map[string][]byte) ([]byte, error) {
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		mode := int64(0644)
		if strings.HasPrefix(name, "bin/") {
			mode = 0755
		}
		data := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: root + "/" + name, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (b *serverBundle) render(template []byte) []byte {
	size := strconv.FormatFloat(float64(b.download.Installer.Size)/(1<<20), 'f', 1, 64)
	values := map[string]string{"RELEASE_ID": b.state.ReleaseID, "FILE": b.download.Installer.Source, "SHA256": b.download.Installer.SHA256, "SIGNED_DATE": b.download.SignedAt.UTC().Format("2006-01-02"), "SIZE_EN": size + " MB", "SIZE_PL": strings.Replace(size, ".", ",", 1) + " MB"}
	page := string(template)
	for key, value := range values {
		page = strings.ReplaceAll(page, "{{SERVER_"+key+"}}", html.EscapeString(value))
	}
	return []byte(page)
}
