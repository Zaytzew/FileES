package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/internal/releaseenvelope"
)

const testTemplate = `<h1>{{VERSION}}</h1><p>{{RELEASE_ID}} {{SIGNED_DATE}} {{SIZE_PL}} {{SIZE_EN}}</p>
<a href="{{MSI_FILE}}" download>x</a><code>{{SHA256}}</code>
<p class="note">{{NOTES_PL}}</p><p class="note">{{NOTES_EN}}</p>`

type signer struct {
	private ed25519.PrivateKey
	number  []byte
	public  []byte
}

func newSigner(t *testing.T) signer {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	number := make([]byte, 8)
	if _, err := rand.Read(number); err != nil {
		t.Fatal(err)
	}
	payload := append(append([]byte("Ed"), number...), public...)
	return signer{private: private, number: number, public: signify("test release key", payload)}
}

func (s signer) sign(message []byte) []byte {
	return signify("test signature", append(append([]byte("Ed"), s.number...), ed25519.Sign(s.private, message)...))
}

func signify(comment string, payload []byte) []byte {
	return []byte("untrusted comment: " + comment + "\n" + base64.StdEncoding.EncodeToString(payload) + "\n")
}

// repo is an in-memory release repository that counts what was fetched.
type repo struct {
	files   map[string][]byte
	fetched map[string]int
}

func (r *repo) Cat(_ context.Context, name string) ([]byte, error) {
	r.fetched[name]++
	data, ok := r.files[name]
	if !ok {
		return nil, fmt.Errorf("%s: no such file", name)
	}
	return data, nil
}

type fixedDater struct{}

func (fixedDater) LastChanged(context.Context, string) (time.Time, error) {
	return time.Date(2026, 9, 16, 10, 55, 7, 0, time.UTC), nil
}

// release adds one signed release to the repository and points the channel at it.
func (r *repo) release(t *testing.T, key signer, id string, sequence uint64, installer []byte, recordedHash string) {
	t.Helper()
	version := fmt.Sprintf("0.1.16.%d", sequence)
	msi := "filees-" + version + ".msi"
	base := "releases/" + id + "/desktop/windows-amd64/"
	digest := sha256.Sum256(installer)
	hash := hex.EncodeToString(digest[:])
	if recordedHash != "" {
		hash = recordedHash
	}
	manifest, _ := json.MarshalIndent(map[string]any{
		"schema_version": 2, "release_id": id, "sequence": sequence, "security_epoch": 1, "key_id": "release-test",
		"component": "desktop", "platform": "windows-amd64", "version": version,
		"artifacts": []map[string]any{
			{"source": "filees-client-windows-amd64.tar.gz", "sha256": strings.Repeat("a", 64), "size": 10, "kind": "bundle"},
			{"source": msi, "sha256": hash, "size": len(installer), "kind": "installer"},
		},
	}, "", "  ")
	channel, _ := json.MarshalIndent(map[string]any{
		"schema_version": 2, "release_id": id, "sequence": sequence, "security_epoch": 1, "key_id": "release-test",
		"expires_at": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		"components": []map[string]any{{"name": "desktop", "platform": "windows-amd64", "manifest": base + "manifest.json"}},
	}, "", "  ")
	r.files[base+"manifest.json"] = manifest
	r.files[base+"manifest.json.sig"] = key.sign(manifest)
	r.files[base+msi] = installer
	r.files["channels/alpha.v2.json"] = channel
	r.files["channels/alpha.v2.json.sig"] = key.sign(channel)
}

func publisher(t *testing.T, r *repo, trusted signer, root string) Publisher {
	t.Helper()
	key, err := releaseenvelope.CanonicalSignifyPublicKey(trusted.public)
	if err != nil {
		t.Fatal(err)
	}
	return Publisher{
		Resolver: &releaseenvelope.Resolver{
			Fetcher:     r,
			Verifier:    releaseenvelope.Ed25519Verifier{Keys: map[string][]byte{"release-test": key}},
			TrustedKeys: []string{"release-test"},
		},
		Fetcher: r,
		Dater:   fixedDater{},
		Config: Config{Channel: "alpha", Component: "desktop", Platform: "windows-amd64", KeyID: "release-test",
			Notes: map[string]ReleaseNotes{"r1295": {PL: "Nowość: Wehikuł czasu", EN: "New: the time machine"}}},
		Template:  []byte(testTemplate),
		OutDir:    filepath.Join(root, "site", "download"),
		StatePath: filepath.Join(root, "state.json"),
	}
}

func newRepo() *repo { return &repo{files: map[string][]byte{}, fetched: map[string]int{}} }

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPublishesTheSignedChannelRelease(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "site"), 0o755)
	key := newSigner(t)
	r := newRepo()
	installer := []byte("MSI bytes of release 1295")
	r.release(t, key, "r1295", 1295, installer, "")
	p := publisher(t, r, key, root)

	result, err := p.Publish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.ReleaseID != "r1295" || result.Installer != "filees-0.1.16.1295.msi" {
		t.Fatalf("result = %+v", result)
	}
	if got := mustRead(t, filepath.Join(p.OutDir, "filees-0.1.16.1295.msi")); got != string(installer) {
		t.Fatalf("installer = %q", got)
	}
	digest := sha256.Sum256(installer)
	hash := hex.EncodeToString(digest[:])
	if got := mustRead(t, filepath.Join(p.OutDir, "SHA256SUMS")); got != hash+"  filees-0.1.16.1295.msi\n" {
		t.Fatalf("SHA256SUMS = %q", got)
	}
	page := mustRead(t, filepath.Join(p.OutDir, "index.html"))
	for _, want := range []string{"<h1>0.1.16.1295</h1>", "r1295 2026-09-16", hash, `href="filees-0.1.16.1295.msi"`, "Nowość: Wehikuł czasu", "New: the time machine"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q:\n%s", want, page)
		}
	}
	state, err := loadState(p.StatePath)
	if err != nil || state == nil || state.Sequence != 1295 || state.SHA256 != hash {
		t.Fatalf("state = %+v, %v", state, err)
	}
}

func TestAnUnchangedChannelDoesNotDownloadTheInstallerAgain(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "site"), 0o755)
	key := newSigner(t)
	r := newRepo()
	r.release(t, key, "r1295", 1295, []byte("installer"), "")
	p := publisher(t, r, key, root)
	if _, err := p.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := p.Publish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatal("second run replaced an up-to-date publication")
	}
	if n := r.fetched["releases/r1295/desktop/windows-amd64/filees-0.1.16.1295.msi"]; n != 1 {
		t.Fatalf("installer fetched %d times, want once", n)
	}
}

func TestAChannelMovedBackwardsIsRefused(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "site"), 0o755)
	key := newSigner(t)
	r := newRepo()
	r.release(t, key, "r1295", 1295, []byte("new installer"), "")
	p := publisher(t, r, key, root)
	if _, err := p.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.release(t, key, "r1247", 1247, []byte("old installer"), "")
	_, err := p.Publish(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refusing to go back") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(p.OutDir, "filees-0.1.16.1295.msi")); statErr != nil {
		t.Fatalf("the newer publication was touched: %v", statErr)
	}
}

func TestAnInstallerThatDoesNotMatchItsManifestKeepsThePreviousPage(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "site"), 0o755)
	key := newSigner(t)
	r := newRepo()
	r.release(t, key, "r1295", 1295, []byte("good installer"), "")
	p := publisher(t, r, key, root)
	if _, err := p.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, filepath.Join(p.OutDir, "index.html"))
	// Signed manifest records one hash; the repository serves other bytes.
	r.release(t, key, "r1300", 1300, []byte("swapped installer"), strings.Repeat("0", 64))
	_, err := p.Publish(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not match the signed manifest") {
		t.Fatalf("err = %v", err)
	}
	if after := mustRead(t, filepath.Join(p.OutDir, "index.html")); after != before {
		t.Fatal("a failed run changed the published page")
	}
	entries, _ := os.ReadDir(filepath.Join(root, "site"))
	if len(entries) != 1 {
		t.Fatalf("staging left behind: %v", entries)
	}
}

func TestAReleaseSignedByAnotherKeyIsNotPublished(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "site"), 0o755)
	trusted, attacker := newSigner(t), newSigner(t)
	r := newRepo()
	r.release(t, attacker, "r1295", 1295, []byte("installer"), "")
	p := publisher(t, r, trusted, root)
	_, err := p.Publish(context.Background())
	if err == nil || !strings.Contains(err.Error(), "verify release envelope") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(p.OutDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("something was published: %v", statErr)
	}
}

func TestAReleaseWithoutNotesHasNoEmptyNoteBox(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "site"), 0o755)
	key := newSigner(t)
	r := newRepo()
	r.release(t, key, "r1300", 1300, []byte("installer"), "")
	p := publisher(t, r, key, root)
	if _, err := p.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if page := mustRead(t, filepath.Join(p.OutDir, "index.html")); strings.Contains(page, `class="note"`) {
		t.Fatalf("empty note box left in page:\n%s", page)
	}
}
