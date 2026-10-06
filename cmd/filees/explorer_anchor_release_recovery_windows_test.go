//go:build windows && !nocfapi

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filees/pkg/client"
	"golang.org/x/sys/windows"
)

type testReleaseExclusion func(context.Context, string, string) (client.PathExclusion, error)

func (f testReleaseExclusion) InspectPathExclusion(ctx context.Context, wc, rel string) (client.PathExclusion, error) {
	return f(ctx, wc, rel)
}

func testReleaseSnapshot(j *anchorReleaseJournal) client.PathExclusion {
	return client.PathExclusion{Excluded: true, WC: filepath.ToSlash(j.identity.WC), Path: j.identity.Rel,
		URL: j.identity.RepoURL + "/" + j.identity.Rel, RepositoryRootURL: j.identity.RepoURL,
		RepositoryUUID: j.identity.RepoUUID, Revision: j.identity.Revision}
}

func requestTestReleaseExclusion(t *testing.T, j *anchorReleaseJournal) {
	t.Helper()
	for _, phase := range []string{"prepare_requested", "exclude_requested"} {
		if err := j.request(t.Context(), phase); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAnchorReleaseRecoveryRefusesWithoutChangingWork(t *testing.T) {
	for _, name := range []string{"early", "not-excluded", "svn-error", "uuid", "url", "revision", "wc", "path", "new-work", "replaced-file", "directory", "cancelled", "changed-copy", "old-record"} {
		t.Run(name, func(t *testing.T) {
			j, body := newTestAnchorReleaseJournal(t)
			if name != "early" {
				requestTestReleaseExclusion(t, j)
			}
			path := filepath.Join(j.identity.WC, j.identity.Rel)
			work := body
			if name == "new-work" || name == "replaced-file" {
				work = []byte("new unpublished work must remain")
				if name == "replaced-file" {
					if err := os.Rename(path, path+"-saved"); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(path, work, 0600); err != nil {
					t.Fatal(err)
				}
			} else if name == "directory" {
				if err := os.Rename(path, path+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if name != "early" {
				if err := os.Rename(path, path+"-saved"); err != nil {
					t.Fatal(err)
				}
			}
			if name == "changed-copy" {
				if err := os.WriteFile(filepath.Join(j.store, j.id, "data.bin"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "old-record" {
				// Older private evidence cannot invent the missing UUID binding.
				raw := []byte("{\"Version\":1}")
				if err := os.WriteFile(filepath.Join(j.store, j.id, "00.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			got := testReleaseSnapshot(j)
			switch name {
			case "not-excluded":
				got.Excluded = false
			case "uuid":
				got.RepositoryUUID = "another"
			case "url":
				got.URL += "-other"
			case "revision":
				got.Revision++
			case "wc":
				got.WC += "-other"
			case "path":
				got.Path += "-other"
			}
			ctx := t.Context()
			if name == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			restores := 0
			ops := anchorReleaseRecoveryIO{svn: testReleaseExclusion(func(context.Context, string, string) (client.PathExclusion, error) {
				if name == "svn-error" {
					return got, errors.New("unavailable")
				}
				return got, nil
			}), inspect: func(context.Context, string) (cfapiAnswer, error) {
				t.Fatal("obstruction read as a remote seed")
				return cfapiAnswer{}, nil
			},
				restore: func(context.Context, string, int64) error { restores++; return nil }}
			out, err := j.recoverSeed(ctx, ops)
			if err == nil || out.seedObserved || restores != 0 {
				t.Fatalf("unsafe recovery %+v %v, restores=%d", out, err, restores)
			}
			if name == "early" || name == "new-work" || name == "replaced-file" {
				if current, err := os.ReadFile(path); err != nil || !bytes.Equal(current, work) {
					t.Fatal("work changed")
				}
			}
			if name != "changed-copy" {
				if current, err := os.ReadFile(filepath.Join(j.store, j.id, "data.bin")); err != nil || !bytes.Equal(current, body) {
					t.Fatal("backup changed")
				}
			}
		})
	}
}

func TestAnchorReleaseRecoveryRequiresEffectNotSuccessfulReceipt(t *testing.T) {
	j, body := newTestAnchorReleaseJournal(t)
	requestTestReleaseExclusion(t, j)
	path := filepath.Join(j.identity.WC, j.identity.Rel)
	if err := os.Rename(path, path+"-saved"); err != nil {
		t.Fatal(err)
	}
	checks, restores := 0, 0
	ops := anchorReleaseRecoveryIO{svn: testReleaseExclusion(func(context.Context, string, string) (client.PathExclusion, error) {
		checks++
		return testReleaseSnapshot(j), nil
	}),
		inspect: func(context.Context, string) (cfapiAnswer, error) {
			t.Fatal("no file to inspect")
			return cfapiAnswer{}, nil
		},
		restore: func(ctx context.Context, rel string, size int64) error {
			restores++
			if rel != j.identity.Rel || size != int64(len(body)) {
				t.Fatal("wrong restoration arguments")
			}
			if r, err := j.inspect(ctx); err != nil || r.Phase != "restore_requested" {
				t.Fatal("effect before intent")
			}
			if err := os.Rename(j.identity.WC, j.identity.WC+"-moved"); err == nil {
				t.Fatal("parent replacement admitted")
			}
			wide, _ := windows.UTF16PtrFromString(filepath.Join(j.store, j.id, "data.bin"))
			h, err := windows.CreateFile(wide, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
			if err == nil {
				windows.CloseHandle(h)
				t.Fatal("backup writer admitted")
			}
			_, err = j.recoverSeed(ctx, anchorReleaseRecoveryIO{svn: opsReaderForTest(j), inspect: func(context.Context, string) (cfapiAnswer, error) { return cfapiAnswer{}, nil }, restore: func(context.Context, string, int64) error { t.Fatal("second writer"); return nil }})
			if err == nil {
				t.Fatal("duplicate recovery writer admitted")
			}
			return nil // A success ACK without a created seed is not success.
		}}
	out, err := j.recoverSeed(t.Context(), ops)
	if err == nil || out.seedObserved || restores != 1 || checks != 2 {
		t.Fatalf("unconfirmed effect: %+v %v %d/%d", out, err, restores, checks)
	}
}

func opsReaderForTest(j *anchorReleaseJournal) testReleaseExclusion {
	return func(context.Context, string, string) (client.PathExclusion, error) {
		return testReleaseSnapshot(j), nil
	}
}

func TestAnchorReleaseRecoveryPreservesObjectAppearingDuringRestore(t *testing.T) {
	j, body := newTestAnchorReleaseJournal(t)
	requestTestReleaseExclusion(t, j)
	path := filepath.Join(j.identity.WC, j.identity.Rel)
	if err := os.Rename(path, path+"-saved"); err != nil {
		t.Fatal(err)
	}
	newWork := []byte("new local file at the creation boundary")
	ops := anchorReleaseRecoveryIO{svn: opsReaderForTest(j), inspect: func(context.Context, string) (cfapiAnswer, error) {
		t.Fatal("new ordinary work inspected")
		return cfapiAnswer{}, nil
	},
		restore: func(context.Context, string, int64) error {
			if err := os.WriteFile(path, newWork, 0600); err != nil {
				t.Fatal(err)
			}
			return errors.New("no-replace refusal")
		}}
	if out, err := j.recoverSeed(t.Context(), ops); err == nil || out.seedObserved {
		t.Fatal("new work became release success")
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, newWork) {
		t.Fatal("new work lost")
	}
	if got, err := os.ReadFile(filepath.Join(j.store, j.id, "data.bin")); err != nil || !bytes.Equal(got, body) {
		t.Fatal("backup lost")
	}
}

func TestAnchorReleaseRecoveryRechecksSVNAfterIntent(t *testing.T) {
	j, _ := newTestAnchorReleaseJournal(t)
	requestTestReleaseExclusion(t, j)
	path := filepath.Join(j.identity.WC, j.identity.Rel)
	if err := os.Rename(path, path+"-saved"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	ops := anchorReleaseRecoveryIO{svn: testReleaseExclusion(func(context.Context, string, string) (client.PathExclusion, error) {
		calls++
		got := testReleaseSnapshot(j)
		if calls > 1 {
			got.Revision++
		}
		return got, nil
	}), inspect: func(context.Context, string) (cfapiAnswer, error) {
		t.Fatal("absent file inspected")
		return cfapiAnswer{}, nil
	},
		restore: func(context.Context, string, int64) error { t.Fatal("stale exclusion used"); return nil }}
	if out, err := j.recoverSeed(t.Context(), ops); err == nil || out.seedObserved || calls != 2 {
		t.Fatalf("no recheck: %+v %v %d", out, err, calls)
	}
	if r, err := j.inspect(t.Context()); err != nil || r.Phase != "restore_requested" {
		t.Fatal("uncertain intent not retained")
	}
}

func TestAnchorReleaseEvidenceRequiresUUIDAndBoundedWrites(t *testing.T) {
	j, _ := newTestAnchorReleaseJournal(t)
	bad := j.identity
	bad.RepoUUID = ""
	if err := validateAnchorReleaseIdentity(bad); err == nil {
		t.Fatal("missing UUID accepted")
	}
	r, err := j.inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r.Identity.ServerID = string(bytes.Repeat([]byte("x"), 17*1024))
	if err := writeAnchorReleaseRecord(filepath.Join(j.store, j.id), 1, r); err == nil {
		t.Fatal("unreadable oversized record written")
	}
	if _, err := os.Stat(filepath.Join(j.store, j.id, "01.json")); !os.IsNotExist(err) {
		t.Fatal("oversized intent became evidence")
	}
}
