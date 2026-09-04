package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filees/internal/releaseenvelope"
	"filees/pkg/config"
)

func stageBundle(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

func TestWindowsProducerAndSignerUseTheConsumerContract(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	prepare, err := os.ReadFile(filepath.Join(root, "tools", "prepare-client-release-windows.sh"))
	if err != nil {
		t.Fatal(err)
	}
	producer := string(prepare)
	for _, required := range []string{
		"COMPONENT=" + config.DesktopUpdateComponent,
		`release_root="$FILEES_BIN_WC/releases/$RELEASE_ID/$COMPONENT/$PLATFORM"`,
		`-channel-out "$FILEES_BIN_WC/releases/$RELEASE_ID/channel.v2.json"`,
		`FILEES_RELEASE_PUBKEY="$FILEES_RELEASE_PUBKEY"`,
		`FILEES_RELEASE_REPO_URL="$FILEES_RELEASE_REPO_URL"`,
		`packaging/build-client-bundle.sh`,
		`packaging/windows/build-msi.ps1`,
		`-installer "$installer"`,
	} {
		if !strings.Contains(producer, required) {
			t.Errorf("Windows producer is not bound to %q", required)
		}
	}
	signer, err := os.ReadFile(filepath.Join(root, "tools", "release-sign-and-publish.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(signer), `candidate="$release_root/channel.v2.json"`) {
		t.Error("producer and signing machine disagree on the neutral channel candidate name")
	}
	msi, err := os.ReadFile(filepath.Join(root, "packaging", "windows", "build-msi.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	msiBuild := string(msi)
	for _, required := range []string{
		`$msiVersion = "$major.$minor.$revision"`,
		`-d "ProductVersion=$msiVersion"`,
		`-d "BundleVersion=$bundleVersion"`,
	} {
		if !strings.Contains(msiBuild, required) {
			t.Errorf("MSI builder is not bound to %q", required)
		}
	}
	if strings.Contains(msiBuild, `-d "ProductVersion=$bundleVersion"`) {
		t.Error("MSI MajorUpgrade would ignore the SVN revision in the fourth version field")
	}
	wxs, err := os.ReadFile(filepath.Join(root, "packaging", "windows", "filees.wxs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wxs), `Value="$(var.BundleVersion)"`) {
		t.Error("the installed-version record no longer preserves the full bundle version")
	}
	if strings.Contains(string(wxs), `Name="logs\*"`) ||
		!strings.Contains(string(wxs), `Subdirectory="logs"`) {
		t.Error("WiX RemoveFile must express logs as a subdirectory, not as part of its filename wildcard")
	}
}

func TestWindowsReleaseWithoutInstallerIsRefused(t *testing.T) {
	root := t.TempDir()
	releaseRoot := filepath.Join(root, "releases", "r819", config.DesktopUpdateComponent, "windows-amd64")
	bundle := stageBundle(t, releaseRoot, "filees-client-windows-amd64.tar.gz", "pretend bundle")
	err := run(bundle, "", config.DesktopUpdateComponent, "windows-amd64", "r819", "0.1.15.819", "alpha-key", "",
		releaseRoot, filepath.Join(root, "channel.v2.json"), "", 819, 1)
	if err == nil || !strings.Contains(err.Error(), "requires the MSI") {
		t.Fatalf("missing installer = %v", err)
	}
}

func TestBothDocumentsAreWrittenAndWouldBeAccepted(t *testing.T) {
	root := t.TempDir()
	releaseRoot := filepath.Join(root, "releases", "r819", config.DesktopUpdateComponent, "windows-amd64")
	bundle := stageBundle(t, releaseRoot, "filees-client-windows-amd64.tar.gz", "pretend bundle")
	installer := stageBundle(t, releaseRoot, "filees-0.1.15.819.msi", "pretend MSI")
	channel := filepath.Join(root, "releases", "r819", "channel.v2.json")

	if err := run(bundle, installer, config.DesktopUpdateComponent, "windows-amd64", "r819", "0.1.15.819", "alpha-key", "",
		releaseRoot, channel, "", 819, 1); err != nil {
		t.Fatalf("run: %v", err)
	}

	manifestRaw, err := os.ReadFile(filepath.Join(releaseRoot, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Read back through the client's own parser, because that is the only
	// reader whose opinion matters.
	manifest, err := releaseenvelope.ParseArtifactManifest(manifestRaw)
	if err != nil {
		t.Fatalf("the client would refuse the manifest: %v", err)
	}
	if len(manifest.Artifacts) != 2 || manifest.Artifacts[0].Source != "filees-client-windows-amd64.tar.gz" || manifest.Artifacts[0].Kind != "bundle" || manifest.Artifacts[1].Source != "filees-0.1.15.819.msi" || manifest.Artifacts[1].Kind != "installer" {
		t.Fatalf("artifacts = %+v", manifest.Artifacts)
	}
	if manifest.Artifacts[0].Size != int64(len("pretend bundle")) || manifest.Artifacts[1].Size != int64(len("pretend MSI")) {
		t.Errorf("size = %d, want %d", manifest.Artifacts[0].Size, len("pretend bundle"))
	}

	envelopeRaw, err := os.ReadFile(channel)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := releaseenvelope.ParseEnvelope(envelopeRaw, time.Now())
	if err != nil {
		t.Fatalf("the client would refuse the envelope: %v", err)
	}
	if len(envelope.Components) != 1 || envelope.Components[0].Name != config.DesktopUpdateComponent || envelope.Components[0].Manifest != "releases/r819/desktop/windows-amd64/manifest.json" {
		t.Fatalf("components = %+v", envelope.Components)
	}
	// The two must agree on every field the resolver cross-checks, which is the
	// reason they are generated by one command.
	if envelope.ReleaseID != manifest.ReleaseID || envelope.Sequence != manifest.Sequence ||
		envelope.SecurityEpoch != manifest.SecurityEpoch || envelope.KeyID != manifest.KeyID {
		t.Error("the envelope and the manifest disagree on the release they describe")
	}
	component, err := envelope.Select(config.DesktopUpdateComponent, "windows-amd64")
	if err != nil {
		t.Fatalf("the configured desktop client cannot select the generated component: %v", err)
	}
	if err := envelope.ValidateManifest(component, manifest); err != nil {
		t.Fatalf("the configured desktop client would reject the generated pair: %v", err)
	}
}

// Publishing one platform must not drop the others.
//
// The envelope covers a whole release, so platforms may only be combined while
// they carry exactly the same release identity.
func TestPublishingAnotherPlatformInTheSameReleaseKeepsTheOthers(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "previous.v2.json")
	previous := releaseenvelope.Envelope{
		SchemaVersion: releaseenvelope.SchemaVersion, ReleaseID: "r819", Sequence: 819, SecurityEpoch: 1,
		KeyID: "alpha-key", ExpiresAt: time.Now().AddDate(1, 0, 0).UTC().Format(time.RFC3339),
		Components: []releaseenvelope.Component{
			{Name: config.DesktopUpdateComponent, Platform: "linux-amd64", Manifest: "releases/r819/desktop/linux-amd64/manifest.json"},
		},
	}
	encoded, err := json.MarshalIndent(previous, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, encoded, 0o644); err != nil {
		t.Fatal(err)
	}

	releaseRoot := filepath.Join(root, "releases", "r819", config.DesktopUpdateComponent, "windows-amd64")
	bundle := stageBundle(t, releaseRoot, "filees-client-windows-amd64.tar.gz", "pretend bundle")
	installer := stageBundle(t, releaseRoot, "filees-0.1.15.819.msi", "pretend MSI")
	channel := filepath.Join(root, "releases", "r819", "channel.v2.json")
	if err := run(bundle, installer, config.DesktopUpdateComponent, "windows-amd64", "r819", "0.1.15.819", "alpha-key", "",
		releaseRoot, channel, existing, 819, 1); err != nil {
		t.Fatalf("run: %v", err)
	}
	raw, err := os.ReadFile(channel)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := releaseenvelope.ParseEnvelope(raw, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	platforms := make([]string, 0, len(envelope.Components))
	for _, component := range envelope.Components {
		platforms = append(platforms, component.Platform)
	}
	if len(platforms) != 2 {
		t.Fatalf("platforms = %v, want linux-amd64 kept beside windows-amd64", platforms)
	}
	// The retained entry belongs to this same release and remains selectable.
	for _, component := range envelope.Components {
		if component.Platform == "linux-amd64" && component.Manifest != "releases/r819/desktop/linux-amd64/manifest.json" {
			t.Errorf("the linux component was repointed at this release: %s", component.Manifest)
		}
	}
}

// An entry from an older channel cannot be copied verbatim into a new envelope.
// The resolver cross-checks release_id, sequence, epoch and key_id between the
// envelope and every manifest; pretending to preserve the old entry would make
// the new channel unusable by that platform.
func TestPublishingAReleaseRefusesToCarryAnOlderPlatformsManifest(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "previous.v2.json")
	previous := releaseenvelope.Envelope{
		SchemaVersion: releaseenvelope.SchemaVersion, ReleaseID: "r800", Sequence: 800, SecurityEpoch: 1,
		KeyID: "alpha-key", ExpiresAt: time.Now().AddDate(1, 0, 0).UTC().Format(time.RFC3339),
		Components: []releaseenvelope.Component{
			{Name: config.DesktopUpdateComponent, Platform: "linux-amd64", Manifest: "releases/r800/desktop/linux-amd64/manifest.json"},
		},
	}
	encoded, err := json.MarshalIndent(previous, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, encoded, 0o644); err != nil {
		t.Fatal(err)
	}

	releaseRoot := filepath.Join(root, "releases", "r819", config.DesktopUpdateComponent, "windows-amd64")
	bundle := stageBundle(t, releaseRoot, "filees-client-windows-amd64.tar.gz", "pretend bundle")
	installer := stageBundle(t, releaseRoot, "filees-0.1.15.819.msi", "pretend MSI")
	channel := filepath.Join(root, "releases", "r819", "channel.v2.json")
	err = run(bundle, installer, config.DesktopUpdateComponent, "windows-amd64", "r819", "0.1.15.819", "alpha-key", "",
		releaseRoot, channel, existing, 819, 1)
	if err == nil || !strings.Contains(err.Error(), "cannot carry desktop/linux-amd64 from release r800 into r819") {
		t.Fatalf("cross-release merge = %v; want a fail-closed identity error", err)
	}
	if _, statErr := os.Stat(channel); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a rejected merge wrote the channel candidate: %v", statErr)
	}
}

// A release is immutable. A rebuild that quietly replaced a signed one is how
// two different binaries end up sharing an identifier.
func TestAnExistingReleaseIsNeverOverwritten(t *testing.T) {
	root := t.TempDir()
	releaseRoot := filepath.Join(root, "releases", "r819", config.DesktopUpdateComponent, "windows-amd64")
	bundle := stageBundle(t, releaseRoot, "filees-client-windows-amd64.tar.gz", "pretend bundle")
	installer := stageBundle(t, releaseRoot, "filees-0.1.15.819.msi", "pretend MSI")
	channel := filepath.Join(root, "releases", "r819", "channel.v2.json")
	if err := run(bundle, installer, config.DesktopUpdateComponent, "windows-amd64", "r819", "0.1.15.819", "alpha-key", "",
		releaseRoot, channel, "", 819, 1); err != nil {
		t.Fatalf("first run: %v", err)
	}
	err := run(bundle, installer, config.DesktopUpdateComponent, "windows-amd64", "r819", "0.1.15.819", "alpha-key", "",
		releaseRoot, channel, "", 819, 1)
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("second run = %v; a release must not be rewritten", err)
	}
}
