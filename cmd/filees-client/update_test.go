package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"filees/internal/clientupdate"
	"filees/internal/releaseenvelope"
)

type cliUpdateFetcher struct {
	files     map[string][]byte
	calls     []string
	onArchive func()
}

func (f *cliUpdateFetcher) Cat(_ context.Context, name string) ([]byte, error) {
	f.calls = append(f.calls, name)
	data, ok := f.files[name]
	if !ok {
		return nil, errors.New("missing " + name)
	}
	if strings.HasSuffix(name, ".tar.gz") && f.onArchive != nil {
		f.onArchive()
	}
	return data, nil
}

func updateFixture(t *testing.T, platform string, sequence uint64, files map[string][]byte) (cliUpdater, *cliUpdateFetcher) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyTag := []byte("cli-test")
	signify := func(payload []byte) []byte {
		data := append(append([]byte("Ed"), keyTag...), payload...)
		return []byte("untrusted comment: CLI installer test\n" + base64.StdEncoding.EncodeToString(data) + "\n")
	}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gz)
	for name, data := range files {
		mode := int64(0o644)
		if name == "bin/filees-client" {
			mode = 0o755
		}
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Size: int64(len(data)), Mode: mode, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	id := "r" + fmtUint(sequence) + "-cli"
	manifestPath := "releases/" + id + "/cli/" + platform + "/manifest.json"
	archiveName := "filees-client-" + platform + ".tar.gz"
	digest := sha256.Sum256(archive.Bytes())
	manifest := releaseenvelope.ArtifactManifest{SchemaVersion: 2, ReleaseID: id, Sequence: sequence, SecurityEpoch: 1,
		KeyID: "test", Component: "cli", Platform: platform, Version: "0.1.0",
		Artifacts: []releaseenvelope.Artifact{{Source: archiveName, SHA256: hex.EncodeToString(digest[:]), Size: int64(archive.Len()), Kind: "bundle"}}}
	envelope := releaseenvelope.Envelope{SchemaVersion: 2, ReleaseID: id, Sequence: sequence, SecurityEpoch: 1,
		KeyID: "test", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		Components: []releaseenvelope.Component{{Name: "cli", Platform: platform, Manifest: manifestPath}}}
	manifestData, _ := json.Marshal(manifest)
	envelopeData, _ := json.Marshal(envelope)
	fetcher := &cliUpdateFetcher{files: map[string][]byte{
		"channels/cli.json":     envelopeData,
		"channels/cli.json.sig": signify(ed25519.Sign(private, envelopeData)),
		manifestPath:            manifestData,
		manifestPath + ".sig":   signify(ed25519.Sign(private, manifestData)),
		filepath.ToSlash(filepath.Join(filepath.Dir(manifestPath), archiveName)): archive.Bytes(),
	}}
	return cliUpdater{resolver: releaseenvelope.Resolver{Fetcher: fetcher,
		Verifier: releaseenvelope.Ed25519Verifier{Keys: map[string][]byte{"test": signify(public)}}, TrustedKeys: []string{"test"}},
		fetcher: fetcher, prefix: filepath.Join(t.TempDir(), "prefix"), platform: platform}, fetcher
}

func fmtUint(number uint64) string {
	// Avoid depending on a product formatting function in the signed fixture.
	return strconv.FormatUint(number, 10)
}

func cliTestPayload() map[string][]byte {
	// Synthetic ELF header: filesystem/verification tests do not execute it.
	elf := make([]byte, 64)
	copy(elf, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(elf[18:20], 62)
	return map[string][]byte{"bin/filees-client": elf,
		"share/doc/filees-client/README.md": []byte("signed README\n"),
		"share/man/man1/filees-client.1":    []byte(".Dt FILEES-CLIENT 1\n")}
}

func TestCLIInstallSignedBothLayoutsAndPreservesConfiguration(t *testing.T) {
	for _, platform := range []string{"linux-amd64", "openbsd-amd64"} {
		t.Run(platform, func(t *testing.T) {
			u, _ := updateFixture(t, platform, 7, cliTestPayload())
			config := filepath.Join(u.prefix, "etc", "filees", "sync.conf")
			identity := filepath.Join(u.prefix, "activation", "identity")
			for _, name := range []string{config, identity} {
				if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(name, []byte("unchanged"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := u.run(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			man := filepath.Join(u.prefix, "share", "man", "man1", "filees-client.1")
			if platform == "openbsd-amd64" {
				man = filepath.Join(u.prefix, "man", "man1", "filees-client.1")
			}
			if _, err := os.Stat(man); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{config, identity} {
				data, err := os.ReadFile(name)
				if err != nil || string(data) != "unchanged" {
					t.Fatalf("configuration/identity changed: %s", name)
				}
			}
			program := filepath.Join(u.prefix, "bin", "filees-client")
			before, err := os.Stat(program)
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && before.Mode().Perm() != 0o755 {
				t.Fatalf("not executable: %v", before.Mode())
			}
			if err := u.run(context.Background(), false); err != nil {
				t.Fatal(err)
			}
			after, _ := os.Stat(program)
			if !os.SameFile(before, after) {
				t.Fatal("identical retry replaced the program")
			}
			state, err := (clientupdate.StateStore{Path: filepath.Join(u.prefix, "share", "filees-client-update", "state.json")}).Load()
			if err != nil || state.HighestSequence != 7 || state.InstalledVersion != "0.1.0" {
				t.Fatalf("state: %+v %v", state, err)
			}
		})
	}
}

func TestCLIInstallRejectsUntrustedOrUnexpectedPayloadBeforeReplacement(t *testing.T) {
	for _, scenario := range []string{"channel-signature", "manifest-signature", "archive-digest", "missing-binary", "extra-config", "traversal", "wrong-elf"} {
		t.Run(scenario, func(t *testing.T) {
			payload := cliTestPayload()
			switch scenario {
			case "missing-binary":
				delete(payload, "bin/filees-client")
			case "extra-config":
				payload["etc/filees/sync.conf"] = []byte("replace configuration")
			case "traversal":
				payload["../escape"] = []byte("escape")
			case "wrong-elf":
				payload["bin/filees-client"] = []byte("wrong platform")
			}
			u, f := updateFixture(t, "linux-amd64", 7, payload)
			for name := range f.files {
				if (scenario == "channel-signature" && name == "channels/cli.json.sig") ||
					(scenario == "manifest-signature" && strings.HasSuffix(name, "manifest.json.sig")) ||
					(scenario == "archive-digest" && strings.HasSuffix(name, ".tar.gz")) {
					f.files[name] = append(append([]byte(nil), f.files[name]...), 'X')
				}
			}
			program := filepath.Join(u.prefix, "bin", "filees-client")
			if err := os.MkdirAll(filepath.Dir(program), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(program, []byte("old program"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := u.run(context.Background(), false); err == nil {
				t.Fatal("accepted bad release")
			}
			old, err := os.ReadFile(program)
			if err != nil || string(old) != "old program" {
				t.Fatal("changed installed binary")
			}
			if _, err := os.Stat(filepath.Join(u.prefix, "share", "filees-client-update", "state.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("advanced watermark before payload validation")
			}
		})
	}
}

func TestCLIInstallInterruptionKeepsWatermarkAndAllowsExactRetry(t *testing.T) {
	u, f := updateFixture(t, "linux-amd64", 9, cliTestPayload())
	program := filepath.Join(u.prefix, "bin", "filees-client")
	if err := os.MkdirAll(filepath.Dir(program), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, []byte("previous"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.onArchive = cancel
	if err := u.run(ctx, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("interruption: %v", err)
	}
	old, _ := os.ReadFile(program)
	if string(old) != "previous" {
		t.Fatal("interruption replaced binary")
	}
	state, err := (clientupdate.StateStore{Path: filepath.Join(u.prefix, "share", "filees-client-update", "state.json")}).Load()
	if err != nil || state.HighestSequence != 9 || state.InstalledVersion != "" {
		t.Fatalf("watermark: %+v %v", state, err)
	}
	older, olderFetcher := updateFixture(t, "linux-amd64", 8, cliTestPayload())
	older.prefix = u.prefix
	if err := older.run(context.Background(), false); err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("rollback: %v", err)
	}
	if len(olderFetcher.calls) != 4 {
		t.Fatal("downloaded older archive before rollback rejection")
	}
	f.onArchive = nil
	if err := u.run(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(filepath.Join(u.prefix, "share", "filees-client-update", "previous-filees-client"))
	if err != nil || string(backup) != "previous" {
		t.Fatal("previous binary not preserved")
	}
}

func TestCLIInstallCheckDoesNotDownloadArchive(t *testing.T) {
	u, f := updateFixture(t, "linux-amd64", 7, cliTestPayload())
	if err := u.run(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 4 {
		t.Fatalf("check fetched payload: %v", f.calls)
	}
	if _, err := os.Stat(filepath.Join(u.prefix, "bin", "filees-client")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("check installed binary")
	}
}

func TestCLIInstallRefusesConcurrentInstaller(t *testing.T) {
	u, _ := updateFixture(t, "linux-amd64", 7, cliTestPayload())
	locked, release := make(chan struct{}), make(chan struct{})
	u.sandbox = func(_ []string, _ string) error { close(locked); <-release; return nil }
	done := make(chan error, 1)
	go func() { done <- u.run(context.Background(), false) }()
	<-locked
	other := u
	other.sandbox = nil
	err := other.run(context.Background(), false)
	close(release)
	if first := <-done; first != nil {
		t.Fatal(first)
	}
	if err == nil || !strings.Contains(err.Error(), "another CLI installation") {
		t.Fatalf("concurrent install: %v", err)
	}
}

func TestCLIInstallRefusesSymlinkedDirectoryAndBinary(t *testing.T) {
	for _, directory := range []bool{true, false} {
		u, _ := updateFixture(t, "linux-amd64", 7, cliTestPayload())
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.MkdirAll(outside, 0o755); err != nil {
			t.Fatal(err)
		}
		protected := filepath.Join(outside, "protected")
		if err := os.WriteFile(protected, []byte("untouched"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(u.prefix, "bin")
		target := outside
		if !directory {
			link, target = filepath.Join(link, "filees-client"), protected
		}
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		if err := u.run(context.Background(), false); err == nil {
			t.Fatal("followed install symlink")
		}
		data, _ := os.ReadFile(protected)
		if string(data) != "untouched" {
			t.Fatal("modified outside target")
		}
	}
}

func TestCLIInstallDevelopmentBuildCannotObtainTrustFromNetwork(t *testing.T) {
	if _, err := cliReleaseKey(); err == nil {
		t.Fatal("development build acquired trust")
	}
}

func TestCLIInstallRefusesWrongPlatformAndOlderBootstrap(t *testing.T) {
	u, f := updateFixture(t, "openbsd-amd64", 7, cliTestPayload())
	u.platform = "linux-amd64"
	if err := u.run(context.Background(), false); err == nil {
		t.Fatal("accepted other platform's signed channel")
	}
	if len(f.calls) != 2 {
		t.Fatal("read payload for wrong platform")
	}
	u, f = updateFixture(t, "linux-amd64", 7, cliTestPayload())
	u.minimumSequence = 8
	if err := u.run(context.Background(), false); err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("bootstrap downgrade: %v", err)
	}
	if len(f.calls) != 4 {
		t.Fatal("downloaded older bootstrap payload")
	}
}
