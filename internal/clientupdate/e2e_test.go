//go:build !windows

package clientupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filees/internal/releaseenvelope"
	"filees/internal/releasepublish"
	"filees/internal/serverinstall/svnfetch"
)

func TestSignedSVNReleaseEndToEnd(t *testing.T) {
	for _, program := range []string{"svn", "svnadmin", "sha256sum", "sed", "install"} {
		if _, err := exec.LookPath(program); err != nil {
			t.Skipf("%s is required for release E2E", program)
		}
	}
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	if output, err := exec.Command("svnadmin", "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v: %s", err, output)
	}

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyNumber := []byte("E2EKEY01")
	publicFile := signifyFile("FileES E2E public key", append(append([]byte("Ed"), keyNumber...), public...))
	publish := filepath.Join(root, "publish")
	bundle := makeE2EBundle(t, root)
	bundleData, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(bundleData)

	writeRelease := func(channel, releaseID, version string, sequence uint64, artifact []byte, corruptSignature bool) {
		t.Helper()
		manifestPath := filepath.ToSlash(filepath.Join("releases", releaseID, "desktop", "linux-amd64", "manifest.json"))
		artifactPath := filepath.Join(publish, filepath.FromSlash(filepath.Dir(manifestPath)), "filees-client-linux-amd64.tar.gz")
		writeE2EFile(t, artifactPath, artifact, 0o644)
		manifest := releaseenvelope.ArtifactManifest{
			SchemaVersion: 2, ReleaseID: releaseID, Sequence: sequence, SecurityEpoch: 1,
			KeyID: "e2e-key", Component: "desktop", Platform: "linux-amd64", Version: version,
			Artifacts: []releaseenvelope.Artifact{{Source: "filees-client-linux-amd64.tar.gz", SHA256: hex.EncodeToString(digest[:]), Size: int64(len(bundleData)), Kind: "bundle"}},
		}
		manifestData := marshalE2EJSON(t, manifest)
		writeE2EFile(t, filepath.Join(publish, filepath.FromSlash(manifestPath)), manifestData, 0o644)
		writeE2EFile(t, filepath.Join(publish, filepath.FromSlash(manifestPath+".sig")), signE2E(private, keyNumber, manifestData), 0o644)
		envelope := releaseenvelope.Envelope{
			SchemaVersion: 2, ReleaseID: releaseID, Sequence: sequence, SecurityEpoch: 1, KeyID: "e2e-key",
			ExpiresAt:  time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			Components: []releaseenvelope.Component{{Name: "desktop", Platform: "linux-amd64", Manifest: manifestPath}},
		}
		envelopeData := marshalE2EJSON(t, envelope)
		channelPath := filepath.Join(publish, "channels", channel+".v2.json")
		writeE2EFile(t, channelPath, envelopeData, 0o644)
		signature := signE2E(private, keyNumber, envelopeData)
		if corruptSignature {
			signature[len(signature)-3] ^= 1
		}
		writeE2EFile(t, channelPath+".sig", signature, 0o644)
	}

	writeRelease("stable", "release-2", "2.0.0", 2, bundleData, false)
	writeRelease("rollback", "release-1", "1.0.0", 1, bundleData, false)
	writeRelease("bad-signature", "release-4", "4.0.0", 4, bundleData, true)
	corruptBundle := append([]byte(nil), bundleData...)
	corruptBundle[len(corruptBundle)/2] ^= 1
	writeRelease("corrupt-artifact", "release-3", "3.0.0", 3, corruptBundle, false)
	if output, err := exec.Command("svn", "import", "--non-interactive", publish, "file://"+repository, "-m", "E2E signed releases").CombinedOutput(); err != nil {
		t.Fatalf("svn import: %v: %s", err, output)
	}

	fetcher := svnfetch.SVN{RepoURL: "file://" + repository, Timeout: 10 * time.Second}
	resolver := &releaseenvelope.Resolver{
		Fetcher: fetcher, Verifier: releaseenvelope.Ed25519Verifier{Keys: map[string][]byte{"e2e-key": publicFile}},
		TrustedKeys: []string{"e2e-key"},
	}
	home := filepath.Join(root, "home")
	fakeBin := filepath.Join(root, "test-bin")
	writeE2EFile(t, filepath.Join(fakeBin, "systemctl"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	stage := filepath.Join(root, "stage")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	installer := LinuxInstaller{
		Stager: BundleStager{Fetcher: fetcher, Root: stage},
		Paths:  LinuxPaths{Home: home, Prefix: filepath.Join(home, ".local"), DataHome: filepath.Join(home, ".local", "share"), ConfigHome: filepath.Join(home, ".config")},
		Runner: ExecRunner{Env: []string{"PATH=" + fakeBin + string(os.PathListSeparator) + os.Getenv("PATH")}},
	}
	service := &Service{
		Resolver: resolver, Installer: installer, State: StateStore{Path: filepath.Join(home, ".local", "state", "filees", "update.json")},
		ChannelPath: "channels/stable.v2.json", Component: "desktop", Platform: "linux-amd64", CurrentVersion: "0.9.0",
	}
	ctx := context.Background()
	status, err := service.Status(ctx)
	if err != nil || status.State != "available" || status.AvailableVersion != "2.0.0" {
		t.Fatalf("status = %+v, %v", status, err)
	}
	plan, err := service.Plan(ctx)
	if err != nil || !plan.RestartRequired || len(plan.Changes) != 6 {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	result, err := service.Apply(ctx)
	if err != nil || result.InstalledVersion != "2.0.0" || !result.RestartRequired {
		t.Fatalf("apply = %+v, %v", result, err)
	}
	for _, installed := range []string{".local/bin/filees", ".local/bin/filees-gui", ".config/filees/config.json"} {
		if _, err := os.Stat(filepath.Join(home, installed)); err != nil {
			t.Fatalf("installed %s: %v", installed, err)
		}
	}
	state, err := service.State.Load()
	if err != nil || state.HighestSequence != 2 || state.InstalledVersion != "2.0.0" {
		t.Fatalf("state = %+v, %v", state, err)
	}

	for _, test := range []struct{ channel, contains string }{
		{"rollback", "rollback"},
		{"bad-signature", "signature"},
		{"corrupt-artifact", "SHA-256"},
	} {
		t.Run(test.channel, func(t *testing.T) {
			service.ChannelPath = "channels/" + test.channel + ".v2.json"
			_, err := service.Plan(ctx)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %v, want substring %q", err, test.contains)
			}
		})
	}
}

func makeE2EBundle(t *testing.T, root string) string {
	t.Helper()
	_, current, _, _ := runtime.Caller(0)
	project := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	payload := filepath.Join(root, "payload")
	copyFile := func(source, target string, mode os.FileMode) {
		data, err := os.ReadFile(filepath.Join(project, source))
		if err != nil {
			t.Fatal(err)
		}
		writeE2EFile(t, filepath.Join(payload, target), data, mode)
	}
	copyFile("packaging/linux/install-user.sh", "install-user.sh", 0o755)
	copyFile("packaging/linux/filees-gui.desktop", "share/applications/filees-gui.desktop", 0o644)
	copyFile("packaging/linux/filees.service", "share/systemd/user/filees.service", 0o644)
	copyFile("packaging/linux/config.example.json", "share/filees/config.example.json", 0o644)
	copyFile("branded-assets/filees-space-symbol-square.svg", "share/icons/hicolor/scalable/apps/filees-gui.svg", 0o644)
	writeE2EFile(t, filepath.Join(payload, "bin/filees"), []byte("#!/bin/sh\n[ \"$1\" = config-check ]\n"), 0o755)
	writeE2EFile(t, filepath.Join(payload, "bin/filees-gui"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	writeE2EFile(t, filepath.Join(payload, "VERSION"), []byte("2.0.0\n"), 0o644)
	var sums strings.Builder
	for _, name := range []string{"VERSION", "bin/filees", "bin/filees-gui", "share/applications/filees-gui.desktop", "share/filees/config.example.json", "share/icons/hicolor/scalable/apps/filees-gui.svg", "share/systemd/user/filees.service"} {
		data, err := os.ReadFile(filepath.Join(payload, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		fmt.Fprintf(&sums, "%x  %s\n", digest, name)
	}
	writeE2EFile(t, filepath.Join(payload, "SHA256SUMS"), []byte(sums.String()), 0o644)
	output := filepath.Join(root, "filees-client-linux-amd64.tar.gz")
	if err := releasepublish.BundleDirectory(payload, output); err != nil {
		t.Fatal(err)
	}
	return output
}

func marshalE2EJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func signE2E(private ed25519.PrivateKey, keyNumber, message []byte) []byte {
	payload := append(append([]byte("Ed"), keyNumber...), ed25519.Sign(private, message)...)
	return signifyFile("FileES E2E signature", payload)
}

func signifyFile(comment string, payload []byte) []byte {
	return []byte("untrusted comment: " + comment + "\n" + base64.StdEncoding.EncodeToString(payload) + "\n")
}

func writeE2EFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}
