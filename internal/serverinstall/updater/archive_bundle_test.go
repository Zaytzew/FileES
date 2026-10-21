//go:build !windows

package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"filees/internal/serverinstall/manifest"
	"filees/internal/serverinstall/state"
)

func archiveBundleFixture(t *testing.T) (*Runner, *manifest.Manifest, []StagedFile, string) {
	t.Helper()
	r, _ := testRunner(t)
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "packaging", "server", "openbsd-binary-policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m manifest.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m.SchemaVersion, m.ReleaseID, m.Sequence, m.SecurityEpoch = manifest.SchemaVersion, "archive-fixture", 2, 1
	fetcher := mapFetcher{}
	for i := range m.Files {
		data := []byte("signed component: " + m.Files[i].Source)
		digest := sha256.Sum256(data)
		m.Files[i].SHA256 = hex.EncodeToString(digest[:])
		fetcher[m.Files[i].Source] = data
	}
	m.ArchiveFormats = &manifest.ArchiveFormats{Schema: manifest.ArchiveFormatsSchema, Runtime: "filees.archive-protection/v1",
		Policy: "filees.repository-archive-policy/v1", Admission: "filees.archive-admission/v1", ReleaseAudit: "filees.archive-admin-release/v1"}
	m.ArchiveFormats.PayloadSHA256, err = m.ArchivePayloadSHA256()
	if err != nil {
		t.Fatal(err)
	}
	r.Fetcher = fetcher
	staged, root, err := r.stageFiles(t.Context(), &m)
	if err != nil {
		t.Fatal(err)
	}
	return r, &m, staged, root
}

func TestArchiveBundleStagingRejectsIncompleteOrChangedPayload(t *testing.T) {
	for _, change := range []string{"missing", "extra", "hash", "metadata", "mode", "ownership", "target", "source", "symlink", "parent symlink", "root symlink", "directory", "canceled", "resolved target alias"} {
		t.Run(change, func(t *testing.T) {
			r, m, staged, root := archiveBundleFixture(t)
			if err := r.verifyArchiveStaged(t.Context(), m, staged, root); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			first := staged[0].StagePath
			switch change {
			case "missing":
				staged = staged[1:]
			case "extra":
				staged = append(staged, staged[0])
			case "hash":
				if err := os.Chmod(first, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(first, []byte("substituted"), 0600); err != nil {
					t.Fatal(err)
				}
			case "metadata":
				staged[0].ManifestFile.SHA256 = strings.Repeat("cd", 32)
			case "mode":
				staged[0].Mode = 0777
			case "ownership":
				staged[0].Ownership.UID++
			case "target":
				staged[0].Target += "-foreign"
			case "source":
				staged[0].StagePath = staged[1].StagePath
			case "symlink", "directory":
				if err := os.Remove(first); err != nil {
					t.Fatal(err)
				}
				var err error
				if change == "symlink" {
					err = os.Symlink(staged[1].StagePath, first)
				} else {
					err = os.Mkdir(first, 0700)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "parent symlink":
				dir := filepath.Dir(first)
				if err := os.Rename(dir, dir+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dir+"-saved", dir); err != nil {
					t.Fatal(err)
				}
			case "root symlink":
				if err := os.Rename(root, root+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(root+"-saved", root); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "resolved target alias":
				r.Config.SbinDir = filepath.Join(r.Config.LibexecDir, "filees")
			}
			if err := r.verifyArchiveStaged(ctx, m, staged, root); err == nil {
				t.Fatal("changed signed payload accepted")
			}
		})
	}
}

func TestArchiveBundleDeclarationDoesNotBypassProtectedHost(t *testing.T) {
	r, _, _, _ := archiveBundleFixture(t)
	gate := filepath.Join(t.TempDir(), "activation")
	if err := os.MkdirAll(filepath.Join(gate, "archive-admission"), 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(r.Config.SysconfDir, "server.json")
	if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"activation": map[string]string{"root": gate}})
	if err := os.WriteFile(config, raw, 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	err := r.withArchiveInstallAdmission(t.Context(), func() error { called = true; return nil })
	if !errors.Is(err, ErrArchiveMaintenance) || called {
		t.Fatalf("archive declaration bypassed admission: %v, %v", err, called)
	}
	for _, allow := range []bool{false, true} {
		if err := r.Apply(t.Context(), Options{ReleaseID: "archive-fixture", AllowRollback: allow, Yes: true}); !errors.Is(err, ErrArchiveMaintenance) {
			t.Fatal("valid staged declaration bypassed Apply admission", err)
		}
	}
}

func TestArchiveBundleDeclarationStillRequiresSignatureAndFreshness(t *testing.T) {
	r, m, _, _ := archiveBundleFixture(t)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Ephemeral fixture key, never a release key or anything read from disk.
	prefix := []byte{'E', 'd', 1, 2, 3, 4, 5, 6, 7, 8}
	wrap := func(payload []byte) []byte {
		return []byte("untrusted comment: isolated archive bundle fixture\n" + base64.StdEncoding.EncodeToString(append(append([]byte{}, prefix...), payload...)) + "\n")
	}
	r.Config.VerifySignature, r.Config.SignifyPubkey = true, wrap(public)
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := manifest.ReleaseManifestPath(m.ReleaseID, m.Platform)
	fetcher := mapFetcher{path: raw, manifest.SigPath(path): wrap(ed25519.Sign(private, raw))}
	r.Fetcher = fetcher
	parsed, err := r.ResolveManifest(t.Context(), m.ReleaseID)
	if err != nil {
		t.Fatal("valid signed archive declaration refused", err)
	}
	installed := &state.State{InstalledRelease: "newer-release", HighestSequence: m.Sequence + 1, SecurityEpoch: m.SecurityEpoch}
	if err := r.checkFreshness(parsed, installed, Options{}); !errors.Is(err, state.ErrRollback) {
		t.Fatal("archive declaration bypassed release freshness", err)
	}
	m.CreatedAt = "2026-10-21T14:00:00Z"
	m.ArchiveFormats.PayloadSHA256, err = m.ArchivePayloadSHA256()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ValidateArchiveFormats(); err != nil {
		t.Fatal(err)
	}
	fetcher[path], err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ResolveManifest(t.Context(), m.ReleaseID); err == nil || !strings.Contains(err.Error(), "signature verification failed") {
		t.Fatal("self-consistent but unsigned replacement accepted", err)
	}
}

func TestArchiveBundleNativeSandbox(t *testing.T) {
	if runtime.GOOS != "openbsd" {
		t.Skip("actual unveil/pledge requires OpenBSD")
	}
	if os.Getenv("FILEES_TEST_ARCHIVE_BUNDLE_SANDBOX") != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestArchiveBundleNativeSandbox$", "-test.v")
		cmd.Env = append(os.Environ(), "FILEES_TEST_ARCHIVE_BUNDLE_SANDBOX=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("archive staging under actual sandbox: %v %s", err, out)
		}
		return
	}
	r, m, staged, root := archiveBundleFixture(t)
	r.Config.ConfigPath = filepath.Join(r.Config.SysconfDir, "install.conf")
	svn, err := exec.LookPath("svn")
	if err != nil {
		t.Fatal(err)
	}
	r.Config.SVNPath = svn
	sandboxEnabled = func() bool { return true }
	specs, err := r.baseUnveils()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.applyUnveils(specs); err != nil {
		t.Fatal(err)
	}
	if err := r.verifyArchiveStaged(t.Context(), m, staged, root); err != nil {
		t.Fatal("verification after unveil", err)
	}
	if err := r.reducePledge(); err != nil {
		t.Fatal(err)
	}
	if err := r.verifyArchiveStaged(t.Context(), m, staged, root); err != nil {
		t.Fatal("verification after pledge", err)
	}
}

func TestArchiveBundleSharedReadOnlySource(t *testing.T) {
	r, m, staged, _ := archiveBundleFixture(t)
	var shared []StagedFile
	for _, sf := range staged {
		if sf.ManifestFile.Source == "bin/filees-recovery-entry" {
			shared = append(shared, sf)
		}
	}
	if len(shared) != 2 || shared[0].StagePath != shared[1].StagePath || shared[0].Mode == shared[1].Mode {
		t.Fatal("shared payload source or distinct target modes were lost", m.ReleaseID)
	}
	if os.Geteuid() != 0 {
		data, err := os.ReadFile(shared[0].StagePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(shared[0].StagePath, data, shared[1].Mode); err == nil {
			t.Fatal("fixture did not reproduce the old repeated read-only stage write refusal")
		} else {
			t.Logf("old repeated write refuses, new source reuse succeeded: %v", err)
		}
	}
	m.ArchiveFormats = nil
	if _, _, err := r.stageFiles(t.Context(), m); err != nil {
		t.Fatal("ordinary release cannot reuse read-only source", err)
	}
}
