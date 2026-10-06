//go:build windows && native_cfapi_probe && !nocfapi

package main

// Native prerequisites only: no user action, durable coordinator or deletion
// of the retained copy. Every path below is an owned synthetic fixture.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/client"
	"golang.org/x/sys/windows"
)

func (p *localStateProbe) prepareRelease(rel, usn string) string {
	p.t.Helper()
	p.write("prepare-release\tprobe\t" + rel + "\t" + usn)
	select {
	case fields := <-p.reply:
		if fields[0] != "release-prepared" || fields[1] != "probe" {
			p.t.Fatalf("wrong release receipt: %v", fields)
		}
		return fields[2]
	case <-time.After(3 * time.Second):
		p.t.Fatal("release preparation response timed out")
	}
	return ""
}

func (p *localStateProbe) restoreRelease(rel, size string) string {
	p.t.Helper()
	p.write("restore-release\tprobe\t" + rel + "\t" + size)
	select {
	case fields := <-p.reply:
		if fields[0] != "release-restored" || fields[1] != "probe" {
			p.t.Fatalf("wrong restore receipt: %v", fields)
		}
		return fields[2]
	case <-time.After(3 * time.Second):
		p.t.Fatal("release restoration response timed out")
	}
	return ""
}

func TestAnchorReleaseJournalNativePreparation(t *testing.T) {
	for _, name := range []string{"full", "readonly", "writer", "remote", "ordinary", "inside-wc", "stale", "cancel-after-copy", "copy-fences"} {
		t.Run(name, func(t *testing.T) {
			point, store := t.TempDir(), t.TempDir()
			p := newLocalStateProbe(t, point, "", "f\t8192\tremote.txt\tremote.txt\n")
			local := filepath.Join(point, "local.dwg")
			body := bytes.Repeat([]byte("owned source "), 24000)
			if err := os.WriteFile(local, body, 0600); err != nil {
				t.Fatal(err)
			}
			if name != "ordinary" {
				p.accept("local.dwg", true)
			}
			if name == "readonly" {
				if err := os.Chmod(local, 0400); err != nil {
					t.Fatal(err)
				}
				p.accept("local.dwg", true)
			}
			if name == "writer" {
				wide, _ := windows.UTF16PtrFromString(local)
				h, err := windows.CreateFile(wide, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer windows.CloseHandle(h)
			}
			identity := anchorReleaseIdentity{ServerID: "fixture", RepoID: "fixture-repo", RepoURL: "file:///fixture", WC: point, Rel: "local.dwg", Revision: 1}
			if name == "remote" {
				identity.Rel = "remote.txt"
			}
			if name == "inside-wc" {
				store = point
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			m := &anchorManager{helper: p.helper}
			calls := 0
			journal, err := prepareAnchorReleaseJournal(ctx, store, identity, func(ctx context.Context) (cfapiAnswer, error) {
				calls++
				state, err := m.call(ctx, "", "file-state", "--root", point, "--rel", identity.Rel)
				if name == "copy-fences" && calls == 2 {
					wide, _ := windows.UTF16PtrFromString(local)
					for _, access := range []uint32{windows.GENERIC_WRITE, windows.DELETE} {
						h, err := windows.CreateFile(wide, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
						if err == nil {
							windows.CloseHandle(h)
							t.Fatal("copy guard admitted write/rename")
						}
					}
				}
				if calls == 3 && name == "stale" {
					state.USN = "1"
				}
				if calls == 3 && name == "cancel-after-copy" {
					cancel()
				}
				return state, err
			})
			good := name == "full" || name == "readonly" || name == "copy-fences"
			if good {
				if err != nil || journal == nil {
					t.Fatalf("prepare %s: %v", name, err)
				}
				fresh := &anchorReleaseJournal{store: store, identity: identity, id: journal.id}
				if record, err := fresh.inspect(t.Context()); err != nil || record.Phase != "retained" || record.Size != int64(len(body)) {
					t.Fatalf("reload: %+v %v", record, err)
				}
			} else if err == nil || journal != nil {
				t.Fatal("unsafe preparation succeeded")
			}
			if got, err := os.ReadFile(local); err != nil || !bytes.Equal(got, body) {
				t.Fatal("preparation changed source")
			}
			if name == "cancel-after-copy" || name == "stale" {
				entries, err := os.ReadDir(store)
				if err != nil || len(entries) != 1 {
					t.Fatalf("retained failed transaction: %v", err)
				}
				dir := filepath.Join(store, entries[0].Name())
				if got, err := os.ReadFile(filepath.Join(dir, "data.bin")); err != nil || !bytes.Equal(got, body) {
					t.Fatal("failed preparation lost backup")
				}
				if _, err := os.Stat(filepath.Join(dir, "00.json")); !os.IsNotExist(err) {
					t.Fatal("failed preparation recorded admission")
				}
			}
			select {
			case <-p.fetch:
				t.Fatal("retaining bytes requested remote hydration")
			default:
			}
		})
	}
}

func TestAnchorPrepareReleaseRefusesUnsafeState(t *testing.T) {
	for _, name := range []string{"stale", "dirty", "unadopted", "partial", "ordinary", "writer", "reader", "readonly", "private", "escape", "invalid-token"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			p := newLocalStateProbe(t, root, "", "f\t8192\tremote.txt\tremote.txt\nf\t0\tunadopted.txt\tunadopted.txt\n")
			local := filepath.Join(root, "local.txt")
			body := []byte("synthetic published bytes")
			if err := os.WriteFile(local, body, 0600); err != nil {
				t.Fatal(err)
			}
			rel := "local.txt"
			if name != "ordinary" {
				p.accept(rel, name != "dirty")
			}
			token := p.state(rel).USN
			switch name {
			case "stale":
				body = []byte("new unpublished content")
				if err := os.WriteFile(local, body, 0600); err != nil {
					t.Fatal(err)
				}
			case "partial":
				rel, token = "remote.txt", p.state("remote.txt").USN
			case "unadopted":
				rel, token = "unadopted.txt", p.state("unadopted.txt").USN
			case "writer", "reader":
				wide, _ := windows.UTF16PtrFromString(local)
				access := uint32(windows.GENERIC_READ)
				if name == "writer" {
					access |= windows.GENERIC_WRITE
				}
				h, err := windows.CreateFile(wide, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer windows.CloseHandle(h)
			case "readonly":
				if err := os.Chmod(local, 0400); err != nil {
					t.Fatal(err)
				}
				// TRACK_ALL marks the attribute change dirty; the real observer
				// would recheck SVN before confirming the now-read-only file.
				p.accept(rel, true)
				token = p.state(rel).USN
			case "private":
				rel = ".filees/local.txt"
			case "escape":
				rel = "../local.txt"
			case "invalid-token":
				token = "17garbage"
			}
			before := p.state("local.txt")
			hr := p.prepareRelease(rel, token)
			if name == "readonly" {
				if hr != hresultOK {
					t.Fatalf("readonly local preparation: %s", hr)
				}
				if p.state("local.txt").Placeholder {
					t.Fatal("not reverted")
				}
				info, err := os.Stat(local)
				if err != nil || info.Mode().Perm()&0200 != 0 {
					t.Fatal("read-only attribute lost")
				}
			} else {
				if hr == hresultOK {
					t.Fatal("unsafe release accepted")
				}
				if after := p.state("local.txt"); after != before {
					t.Fatalf("refusal mutated state: %+v -> %+v", before, after)
				}
			}
			if got, err := os.ReadFile(local); err != nil || !bytes.Equal(got, body) {
				t.Fatalf("local bytes lost: %v", err)
			}
			select {
			case <-p.fetch:
				t.Fatal("preparation/refusal fetched remote bytes")
			default:
			}
		})
	}
}

func TestAnchorRestoreReleaseRefusesObstructions(t *testing.T) {
	root := t.TempDir()
	content := filepath.Join(t.TempDir(), "content")
	body := []byte("synthetic restoration content")
	if err := os.WriteFile(content, body, 0600); err != nil {
		t.Fatal(err)
	}
	p := newLocalStateProbe(t, root, content, "")
	local := filepath.Join(root, "occupied.txt")
	if err := os.WriteFile(local, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	link := filepath.Join(root, "foreign")
	if out, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, external).CombinedOutput(); err != nil {
		t.Fatalf("owned junction fixture: %s %v", out, err)
	}
	for _, args := range [][2]string{
		{"occupied.txt", "1"}, {"folder", "1"}, {"missing/entry.txt", "1"},
		{".filees/entry.txt", "1"}, {"../entry.txt", "1"}, {"foreign/entry.txt", "1"},
		{"entry.txt", "-1"}, {"entry.txt", "+1"}, {"entry.txt", "1x"},
		{"entry.txt", "999999999999999999999999999999"},
	} {
		if hr := p.restoreRelease(args[0], args[1]); hr == hresultOK {
			t.Fatalf("unsafe restore accepted: %v", args)
		}
	}
	if got, err := os.ReadFile(local); err != nil || !bytes.Equal(got, body) {
		t.Fatal("obstruction bytes changed")
	}
	if entries, err := os.ReadDir(external); err != nil || len(entries) != 0 {
		t.Fatal("created outside own point")
	}
	if hr := p.restoreRelease("folder/drawing.dwg", fmt.Sprint(len(body))); hr != hresultOK {
		t.Fatalf("nested restore: %s", hr)
	}
	before := p.state("folder/drawing.dwg")
	if !before.Placeholder || !before.Partial || before.Local {
		t.Fatalf("wrong seed: %+v", before)
	}
	if hr := p.restoreRelease("folder/drawing.dwg", fmt.Sprint(len(body))); hr == hresultOK {
		t.Fatal("replaced existing placeholder")
	}
	if after := p.state("folder/drawing.dwg"); after != before {
		t.Fatal("repeat changed seed")
	}
	select {
	case <-p.fetch:
		t.Fatal("restoration fetched bytes")
	default:
	}
	m := &anchorManager{helper: p.helper}
	if a, err := m.call(t.Context(), "", "hydrate", "--root", root, "--rel", "folder/drawing.dwg"); err != nil || !a.OK {
		t.Fatalf("nested hydrate: %+v %v", a, err)
	}
	select {
	case request := <-p.fetch:
		if len(request) < 5 || request[4] != "folder/drawing.dwg" {
			t.Fatalf("wrong restored identity: %v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("missing restored fetch")
	}
	if got, err := os.ReadFile(filepath.Join(root, "folder", "drawing.dwg")); err != nil || !bytes.Equal(got, body) {
		t.Fatal("nested bytes changed")
	}
}

func TestAnchorReleasePrerequisitesRoundTrip(t *testing.T) {
	root := t.TempDir()
	repo, source, point := filepath.Join(root, "repo"), filepath.Join(root, "source"), filepath.Join(root, "point")
	cli, admin, native := probeTool(t, "FILEES_PROBE_SVN"), probeTool(t, "FILEES_PROBE_SVNADMIN"), probeTool(t, "FILEES_SVN_PROBE")
	probeRun(t, root, admin, "create", repo)
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("owned synthetic drawing "), 6000)
	cache := filepath.Join(source, "drawing.dwg")
	if err := os.WriteFile(cache, body, 0600); err != nil {
		t.Fatal(err)
	}
	uri := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(repo)}).String()
	probeRun(t, root, cli, "import", "-q", "-m", "release fixture", source, uri)
	probeRun(t, root, cli, "checkout", "-q", "--depth", "empty", uri, point)
	if err := os.Mkdir(filepath.Join(point, ".filees"), 0700); err != nil {
		t.Fatal(err)
	}
	listing := fmt.Sprintf("f\t%d\tdrawing.dwg\tdrawing.dwg\n", len(body))
	p := newLocalStateProbe(t, point, cache, listing)
	local := filepath.Join(point, "drawing.dwg")
	svn := client.New(client.Options{SvnPath: filepath.Join(root, "absent-cli.exe"), NativeSVNPath: native, Timeout: time.Minute})
	adopt := svn.(interface {
		UpdateAdoptPath(context.Context, string, string, int64) (string, error)
	})
	inspection := svn.(interface {
		InspectPathExclusion(context.Context, string, string) (client.PathExclusion, error)
	})
	consumeExpectedHydration := func() {
		t.Helper()
		select {
		case request := <-p.fetch:
			if len(request) < 5 || request[4] != "drawing.dwg" {
				t.Fatalf("wrong explicit hydration: %v", request)
			}
		default:
			t.Fatal("explicit hydration did not reach the provider")
		}
	}
	var repositoryID string
	observeExclusion := func(excluded bool) {
		t.Helper()
		got, err := inspection.InspectPathExclusion(t.Context(), point, "drawing.dwg")
		if err != nil || got.Excluded != excluded || got.Revision != 1 || got.URL != uri+"/drawing.dwg" || got.RepositoryRootURL != uri {
			t.Fatalf("SVN effect inspection: %+v %v", got, err)
		}
		if repositoryID == "" {
			repositoryID = got.RepositoryUUID
		} else if got.RepositoryUUID != repositoryID {
			t.Fatal("SVN repository identity changed")
		}
		select {
		case <-p.fetch:
			t.Fatal("SVN metadata inspection requested remote hydration")
		default:
		}
	}
	if got, err := os.ReadFile(local); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("initial hydration: %v", err)
	}
	if _, err := adopt.UpdateAdoptPath(t.Context(), point, "drawing.dwg", 1); err != nil {
		t.Fatal(err)
	}
	p.accept("drawing.dwg", true)
	consumeExpectedHydration()
	observeExclusion(false)
	readLog := func() string {
		raw, err := exec.CommandContext(t.Context(), cli, "log", "--xml", uri).CombinedOutput()
		if err != nil {
			t.Fatalf("log: %s %v", raw, err)
		}
		return string(raw)
	}
	logBefore := readLog()
	// Persist byte evidence and requests before each external effect. Neither
	// the journal nor this probe retires the copy or replays a failed effect.
	store := filepath.Join(root, "release-evidence")
	if err := os.Mkdir(store, 0700); err != nil {
		t.Fatal(err)
	}
	m := &anchorManager{helper: p.helper}
	identity := anchorReleaseIdentity{ServerID: "fixture", RepoID: "fixture-repo", RepoURL: uri, WC: point, Rel: "drawing.dwg", Revision: 1}
	journal, err := prepareAnchorReleaseJournal(t.Context(), store, identity, func(ctx context.Context) (cfapiAnswer, error) {
		return m.call(ctx, "", "file-state", "--root", point, "--rel", "drawing.dwg")
	})
	if err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(store, journal.id, "data.bin")
	reload := func(phase string) {
		fresh := &anchorReleaseJournal{store: store, identity: identity, id: journal.id}
		if record, err := fresh.inspect(t.Context()); err != nil || record.Phase != phase {
			t.Fatalf("reload %s: %+v %v", phase, record, err)
		}
	}
	reload("retained")
	if err := journal.request(t.Context(), "prepare_requested"); err != nil {
		t.Fatal(err)
	}
	if hr := p.prepareRelease("drawing.dwg", p.state("drawing.dwg").USN); hr != hresultOK {
		t.Fatalf("prepare: %s", hr)
	}
	if s := p.state("drawing.dwg"); s.Placeholder || s.Partial {
		t.Fatalf("not ordinary/local: %+v", s)
	}
	reload("prepare_requested")
	observeExclusion(false)
	if err := journal.request(t.Context(), "exclude_requested"); err != nil {
		t.Fatal(err)
	}
	raw, err := exec.CommandContext(t.Context(), native, "exclude-retained", "--wc", point, "--retained", retained, "--", "drawing.dwg").CombinedOutput()
	var answer struct {
		OK       bool
		State    string
		Revision int64
	}
	if err != nil || json.Unmarshal(raw, &answer) != nil || !answer.OK || answer.State != "excluded" || answer.Revision != 1 {
		t.Fatalf("exclude: %s %v", raw, err)
	}
	if _, err := os.Lstat(local); !os.IsNotExist(err) {
		t.Fatalf("source remained: %v", err)
	}
	if rows, err := svn.Status(t.Context(), point, nil); err != nil {
		t.Fatal(err)
	} else {
		for _, row := range rows {
			if row.Path != ".filees" || row.Item != "unversioned" {
				t.Fatalf("pending change: %+v", row)
			}
		}
	}
	reload("exclude_requested")
	observeExclusion(true)
	if err := journal.request(t.Context(), "restore_requested"); err != nil {
		t.Fatal(err)
	}
	if hr := p.restoreRelease("drawing.dwg", fmt.Sprint(len(body))); hr != hresultOK {
		t.Fatalf("restore: %s", hr)
	}
	if s := p.state("drawing.dwg"); !s.Placeholder || !s.Partial || s.Local {
		t.Fatalf("not remote seed: %+v", s)
	}
	reload("restore_requested")
	observeExclusion(true)
	// The restored identity persists across provider restart without consuming
	// the retained copy. This is not transaction recovery after an interruption.
	p.stop()
	p = newLocalStateProbe(t, point, cache, "")
	if s := p.state("drawing.dwg"); !s.Placeholder || !s.Partial || s.Local {
		t.Fatalf("restored identity lost after reconnect: %+v", s)
	}
	// Ordinary updates must not materialize the excluded path.
	probeRun(t, root, cli, "update", "-q", point)
	if !p.state("drawing.dwg").Partial {
		t.Fatal("ordinary update hydrated exclusion")
	}
	if a, err := m.call(t.Context(), "", "hydrate", "--root", point, "--rel", "drawing.dwg"); err != nil || !a.OK {
		t.Fatalf("download: %+v %v", a, err)
	}
	if _, err := adopt.UpdateAdoptPath(t.Context(), point, "drawing.dwg", 1); err != nil {
		t.Fatal(err)
	}
	p.accept("drawing.dwg", true)
	consumeExpectedHydration()
	observeExclusion(false)
	if got, err := os.ReadFile(local); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("roundtrip lost bytes: %v", err)
	}
	if got, err := os.ReadFile(retained); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("retained copy changed: %v", err)
	}
	if got := readLog(); got != logBefore {
		t.Fatal("repository history changed")
	}
	if rows, err := svn.Status(t.Context(), point, []string{"drawing.dwg"}); err != nil || len(rows) != 1 || rows[0].Item != "normal" {
		t.Fatalf("readoption: %+v %v", rows, err)
	}
}
