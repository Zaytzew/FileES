//go:build native_svn_probe

package nativesvnprobe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cat is the first verb with no working copy. Everything it touches is either a
// URL or the single absolute path it was told to write.
func TestRACatWritesTheRepositoryFile(t *testing.T) {
	f := newFixture(t, "old.txt")
	out := filepath.Join(f.root, "fetched.txt")

	got := f.jsonCall(t, true, "cat", "--url", f.repoURL+"/occupied.txt", "--out", out)
	if got["bytes"].(float64) != float64(len("another object\n")) {
		t.Fatalf("bytes = %v", got["bytes"])
	}
	content, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "another object\n" {
		t.Fatalf("content = %q", content)
	}
}

// A revision must be reachable, or release material can only ever be fetched
// from HEAD - which is not a pin at all.
func TestRACatHonoursARevision(t *testing.T) {
	f := newFixture(t, "old.txt")
	write(t, filepath.Join(f.wc, "occupied.txt"), "second version\n")
	f.svnRun(t, "commit", "--username", "editor", "-m", "second")

	first := filepath.Join(f.root, "r1.txt")
	f.jsonCall(t, true, "cat", "--url", f.repoURL+"/occupied.txt", "--out", first, "--revision", "1")
	content, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "another object\n" {
		t.Fatalf("revision 1 content = %q", content)
	}
}

// The guards. Each one is a way for a mistyped argument to become damage, and
// none of them can be checked by the .filees marker the working-copy verbs use.
func TestRACatRefusesUnsafeArguments(t *testing.T) {
	f := newFixture(t, "old.txt")
	occupied := filepath.Join(f.root, "taken.txt")
	write(t, occupied, "already here\n")

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"target exists", []string{"cat", "--url", f.repoURL + "/occupied.txt", "--out", occupied}},
		{"relative target", []string{"cat", "--url", f.repoURL + "/occupied.txt", "--out", "relative.txt"}},
		{"target is a local path, not a URL", []string{"cat", "--url", f.wc, "--out", filepath.Join(f.root, "x.txt")}},
		{"no url", []string{"cat", "--out", filepath.Join(f.root, "y.txt")}},
		{"negative revision", []string{"cat", "--url", f.repoURL + "/occupied.txt", "--out", filepath.Join(f.root, "z.txt"), "--revision", "-2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.jsonCall(t, false, tc.args...)
		})
	}
	if content, err := os.ReadFile(occupied); err != nil || string(content) != "already here\n" {
		t.Fatalf("refused fetch still touched the target: %q %v", content, err)
	}
}

// A failed fetch must leave nothing behind. Without cleanup the ".part" file
// survives and the next attempt fails on the exclusive open instead of on the
// real reason - a retry that reports the wrong problem.
func TestRACatLeavesNoPartialAfterFailure(t *testing.T) {
	f := newFixture(t, "old.txt")
	out := filepath.Join(f.root, "missing.txt")

	f.jsonCall(t, false, "cat", "--url", f.repoURL+"/no-such-file.txt", "--out", out)

	for _, path := range []string{out, out + ".part"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s survived a failed fetch (err=%v)", path, err)
		}
	}
	// And the retry reports the repository, not the leftover.
	f.jsonCall(t, false, "cat", "--url", f.repoURL+"/no-such-file.txt", "--out", out)
}

// log replaces three CLI invocations that were one question asked with
// different fields: the shout inbox wants revision and message, the commit
// receipt lookup wants a named revprop, and move-result recovery wants changed
// paths with copyfrom.
func TestRALogReadsRevisionsAndMessages(t *testing.T) {
	f := newFixture(t, "old.txt")
	write(t, filepath.Join(f.wc, "occupied.txt"), "second\n")
	f.svnRun(t, "commit", "--username", "editor", "-m", "second commit")

	got := f.jsonCall(t, true, "log", "--url", f.repoURL, "--revision", "HEAD:1")
	entries, _ := got["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("entries = %#v", got["entries"])
	}
	newest := entries[0].(map[string]any)
	if newest["revision"].(float64) != 2 || newest["message"] != "second commit" {
		t.Fatalf("newest entry = %#v", newest)
	}
	if newest["author"] != "editor" {
		t.Fatalf("author = %#v", newest["author"])
	}
	// Both entries must carry their own strings. The receiver's scratch pool is
	// cleared between entries, so a kept pointer reads freed memory - measured
	// 2026-09-08, when the newest entry came back with the tail of another
	// entry's date as its author.
	oldest := entries[1].(map[string]any)
	if oldest["message"] != "birth" || oldest["author"] != "creator" {
		t.Fatalf("oldest entry was corrupted by the newer one: %#v", oldest)
	}
}

func TestRALogReportsChangedPathsWithCopyfrom(t *testing.T) {
	f := newFixture(t, "old.txt")
	f.svnRun(t, "copy", "--", "occupied.txt", "copied.txt")
	f.svnRun(t, "commit", "--username", "copier", "-m", "copy with history")

	got := f.jsonCall(t, true, "log", "--url", f.repoURL, "--revision", "2", "--changed-paths")
	entries, _ := got["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", got["entries"])
	}
	paths, _ := entries[0].(map[string]any)["paths"].([]any)
	var found bool
	for _, raw := range paths {
		p := raw.(map[string]any)
		if p["path"] == "/copied.txt" {
			found = true
			if p["action"] != "A" || p["copyfrom_path"] != "/occupied.txt" || p["copyfrom_rev"].(float64) != 1 {
				t.Fatalf("copy not described: %#v", p)
			}
		}
	}
	if !found {
		t.Fatalf("copied path missing: %#v", paths)
	}
}

func TestRALogReturnsNamedRevprops(t *testing.T) {
	f := newFixture(t, "old.txt")
	write(t, filepath.Join(f.wc, "occupied.txt"), "receipted\n")
	f.svnRun(t, "commit", "--username", "editor", "--with-revprop", "filees:commit-id=RECEIPT-1", "-m", "with receipt")

	got := f.jsonCall(t, true, "log", "--url", f.repoURL, "--revision", "2", "--revprop", "filees:commit-id")
	entries, _ := got["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", got["entries"])
	}
	props, _ := entries[0].(map[string]any)["revprops"].(map[string]any)
	if props["filees:commit-id"] != "RECEIPT-1" {
		t.Fatalf("revprops = %#v", props)
	}
}

func TestRALogAcceptsAWorkingCopyTarget(t *testing.T) {
	f := newFixture(t, "old.txt")
	got := f.jsonCall(t, true, "log", "--disposable-wc", f.wc, "--revision", "1", "--", "occupied.txt")
	entries, _ := got["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["revision"].(float64) != 1 {
		t.Fatalf("entries = %#v", got["entries"])
	}
}

func TestRALogRefusesAmbiguousOrUnboundedRequests(t *testing.T) {
	f := newFixture(t, "old.txt")
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"both sources", []string{"log", "--url", f.repoURL, "--disposable-wc", f.wc, "--revision", "1"}},
		{"no source", []string{"log", "--revision", "1"}},
		{"no revision", []string{"log", "--url", f.repoURL}},
		{"working copy without a target", []string{"log", "--disposable-wc", f.wc, "--revision", "1"}},
		{"url with a separate target", []string{"log", "--url", f.repoURL, "--revision", "1", "--", "occupied.txt"}},
		{"nonsense revision", []string{"log", "--url", f.repoURL, "--revision", "yesterday"}},
	} {
		t.Run(tc.name, func(t *testing.T) { f.jsonCall(t, false, tc.args...) })
	}
}

// second returns a second working copy of the fixture repository, checked out
// by the helper itself and marked disposable so the other verbs accept it.
func (f fixture) second(t *testing.T, name string, args ...string) string {
	t.Helper()
	wc := filepath.Join(f.root, name)
	f.jsonCall(t, true, append([]string{"checkout", "--url", f.repoURL, "--wc", wc}, args...)...)
	write(t, filepath.Join(wc, ".filees-native-probe"), "Disposable fixture; never add to a real WC.\n")
	return wc
}

func TestRACheckoutCreatesAWorkingCopy(t *testing.T) {
	f := newFixture(t, "old.txt")
	wc := filepath.Join(f.root, "fresh")

	got := f.jsonCall(t, true, "checkout", "--url", f.repoURL, "--wc", wc)
	if got["revision"].(float64) != 1 {
		t.Fatalf("revision = %v", got["revision"])
	}
	if _, err := os.Stat(filepath.Join(wc, "occupied.txt")); err != nil {
		t.Fatalf("checkout produced no content: %v", err)
	}
}

func TestRASparseCheckoutFetchesOnlySelectedPath(t *testing.T) {
	f := newFixture(t, "old.txt")
	wc := f.second(t, "sparse", "--depth", "empty")
	if _, err := os.Stat(filepath.Join(wc, "occupied.txt")); !os.IsNotExist(err) {
		t.Fatalf("sparse checkout materialized repository content: %v", err)
	}
	f.jsonCall(t, true, "update", "--disposable-wc", wc, "--depth", "empty", "--", "occupied.txt")
	if _, err := os.Stat(filepath.Join(wc, "occupied.txt")); err != nil {
		t.Fatalf("selected file not fetched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wc, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("unselected file was fetched: %v", err)
	}
}

// A checkout over an existing working copy is a different operation with a
// different failure mode. The caller chooses between them rather than
// discovering which one it got.
func TestRACheckoutRefusesAnExistingWorkingCopy(t *testing.T) {
	f := newFixture(t, "old.txt")
	f.jsonCall(t, false, "checkout", "--url", f.repoURL, "--wc", f.wc)
}

// --force is not a convenience. Measured 2026-09-08 against an unversioned
// file colliding with a repository path - the shape FileES meets whenever the
// owner points it at a folder that already holds work:
//
//	without --force : checkout succeeds, the path becomes a TREE CONFLICT
//	                  (svn status "D     C"), i.e. a working copy broken from
//	                  its first second
//	with --force    : checkout succeeds, the path is a plain local
//	                  modification (svn status "M"), i.e. adopted
//
// Both keep the owner's bytes, so a test that only compares content cannot
// tell them apart - which is why this one asks the helper for the status. It
// also explains why pkg/client always passes --force, and why removing it
// would look harmless right up to the first import.
func TestRACheckoutForceAdoptsInsteadOfConflicting(t *testing.T) {
	f := newFixture(t, "old.txt")

	adopt := func(t *testing.T, name string, force bool) (string, []any) {
		t.Helper()
		wc := filepath.Join(f.root, name)
		if err := os.Mkdir(wc, 0o700); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(wc, "occupied.txt"), "local work\n")
		write(t, filepath.Join(wc, "tylko-lokalny.txt"), "never in the repository\n")
		args := []string{"checkout", "--url", f.repoURL, "--wc", wc}
		if force {
			args = append(args, "--force")
		}
		got := f.jsonCall(t, true, args...)
		write(t, filepath.Join(wc, ".filees-native-probe"), "Disposable fixture; never add to a real WC.\n")
		conflicts, _ := got["conflicts"].([]any)
		return wc, conflicts
	}

	plain, conflicts := adopt(t, "obstructed", false)
	if len(conflicts) != 1 || conflicts[0] != "occupied.txt" {
		t.Fatalf("without --force the obstruction must be reported: %#v", conflicts)
	}

	forced, conflicts := adopt(t, "adopted", true)
	if len(conflicts) != 0 {
		t.Fatalf("with --force there is nothing to conflict about: %#v", conflicts)
	}

	// The adopted copy is usable: the owner's bytes are there and waiting to be
	// published, not stuck in a conflict.
	status := f.jsonCall(t, true, "status", "--disposable-wc", forced, "--", "occupied.txt")
	entries, _ := status["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["item"] != "modified" {
		t.Fatalf("adopted file is not a plain local modification: %#v", status["entries"])
	}
	content, err := os.ReadFile(filepath.Join(forced, "occupied.txt"))
	if err != nil || string(content) != "local work\n" {
		t.Fatalf("adoption overwrote local work: %q %v", content, err)
	}
	for _, wc := range []string{plain, forced} {
		if _, err := os.Stat(filepath.Join(wc, "tylko-lokalny.txt")); err != nil {
			t.Fatalf("%s: unrelated local file was removed: %v", wc, err)
		}
	}
}

func TestRAUpdateBringsChangesForward(t *testing.T) {
	f := newFixture(t, "old.txt")
	other := f.second(t, "reader")

	write(t, filepath.Join(f.wc, "occupied.txt"), "moved on\n")
	f.svnRun(t, "commit", "--username", "editor", "-m", "second")

	got := f.jsonCall(t, true, "update", "--disposable-wc", other)
	if got["revision"].(float64) != 2 {
		t.Fatalf("revision = %v", got["revision"])
	}
	content, err := os.ReadFile(filepath.Join(other, "occupied.txt"))
	if err != nil || string(content) != "moved on\n" {
		t.Fatalf("content = %q err=%v", content, err)
	}
}

// Conflicts come from notifications, not from parsed output. The daemon reads
// them today by scanning the CLI's printed lines; a structured list removes
// that parser instead of moving it, so a reworded Subversion stops being able
// to make every conflict disappear silently.
func TestRAUpdateReportsConflictsStructurally(t *testing.T) {
	f := newFixture(t, "old.txt")
	other := f.second(t, "conflicting")

	write(t, filepath.Join(f.wc, "occupied.txt"), "server version\n")
	f.svnRun(t, "commit", "--username", "editor", "-m", "server change")
	write(t, filepath.Join(other, "occupied.txt"), "local version\n")

	got := f.jsonCall(t, true, "update", "--disposable-wc", other)
	conflicts, _ := got["conflicts"].([]any)
	if len(conflicts) != 1 || conflicts[0] != "occupied.txt" {
		t.Fatalf("conflicts = %#v", got["conflicts"])
	}
}

// --depth empty updates the named paths without deepening anything, which is
// what the daemon uses to refresh one file it cares about.
func TestRAUpdateDepthEmptyTargetsNamedPaths(t *testing.T) {
	f := newFixture(t, "old.txt")
	other := f.second(t, "targeted")

	write(t, filepath.Join(f.wc, "occupied.txt"), "only this one\n")
	f.svnRun(t, "commit", "--username", "editor", "-m", "one file")

	f.jsonCall(t, true, "update", "--disposable-wc", other, "--depth", "empty", "--", "occupied.txt")
	content, err := os.ReadFile(filepath.Join(other, "occupied.txt"))
	if err != nil || string(content) != "only this one\n" {
		t.Fatalf("targeted update did not arrive: %q %v", content, err)
	}
}

func TestRACheckoutAndUpdateRefuseBadArguments(t *testing.T) {
	f := newFixture(t, "old.txt")
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"checkout without url", []string{"checkout", "--wc", filepath.Join(f.root, "x")}},
		{"checkout without wc", []string{"checkout", "--url", f.repoURL}},
		{"checkout to a relative path", []string{"checkout", "--url", f.repoURL, "--wc", "relative"}},
		{"checkout from a local path", []string{"checkout", "--url", f.wc, "--wc", filepath.Join(f.root, "y")}},
		{"update without a working copy", []string{"update"}},
		{"update with a nonsense depth", []string{"update", "--disposable-wc", f.wc, "--depth", "sometimes"}},
	} {
		t.Run(tc.name, func(t *testing.T) { f.jsonCall(t, false, tc.args...) })
	}
}

func TestRACommitPublishesAndReportsItsRevision(t *testing.T) {
	f := newFixture(t, "old.txt")
	other := f.second(t, "publisher")
	write(t, filepath.Join(other, "occupied.txt"), "published natively\n")

	got := f.jsonCall(t, true, "commit", "--disposable-wc", other, "-m", "ogloszenie", "--", "occupied.txt")
	revision, ok := got["revision"].(float64)
	if !ok || revision != 2 {
		t.Fatalf("revision = %#v", got["revision"])
	}

	// The message must actually reach svn:log. It does not travel in the
	// revprop table - svn_client_commit6 refuses standard properties there
	// (E195011) - but through log_msg_func3. Measured 2026-09-08: the first
	// version committed happily with an empty message, which would have
	// silently emptied the Shouting Commit lane, since announcements ride in
	// exactly that property.
	log := f.jsonCall(t, true, "log", "--url", f.repoURL, "--revision", "2")
	entries, _ := log["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["message"] != "ogloszenie" {
		t.Fatalf("message did not reach svn:log: %#v", log["entries"])
	}
}

// The commit receipt marker the daemon uses to recognise its own revision after
// a lost acknowledgement.
func TestRACommitCarriesAReceiptRevprop(t *testing.T) {
	f := newFixture(t, "old.txt")
	other := f.second(t, "receipted")
	write(t, filepath.Join(other, "occupied.txt"), "with a receipt\n")

	f.jsonCall(t, true, "commit", "--disposable-wc", other, "-m", "z pokwitowaniem",
		"--revprop", "filees:commit-id=RECEIPT-7", "--", "occupied.txt")

	log := f.jsonCall(t, true, "log", "--url", f.repoURL, "--revision", "2", "--revprop", "filees:commit-id")
	entries, _ := log["entries"].([]any)
	props, _ := entries[0].(map[string]any)["revprops"].(map[string]any)
	if props["filees:commit-id"] != "RECEIPT-7" {
		t.Fatalf("revprops = %#v", props)
	}
}

func TestRACommitRefusesIncompleteRequests(t *testing.T) {
	f := newFixture(t, "old.txt")
	other := f.second(t, "refuser")
	write(t, filepath.Join(other, "occupied.txt"), "something\n")

	// No paths: a commit that decides its own scope would publish work the
	// caller never listed.
	f.jsonCall(t, false, "commit", "--disposable-wc", other, "-m", "bez sciezek")
	// No message.
	f.jsonCall(t, false, "commit", "--disposable-wc", other, "--", "occupied.txt")
	// One source for the message, so --revprop must not quietly override -m.
	f.jsonCall(t, false, "commit", "--disposable-wc", other, "-m", "a",
		"--revprop", "svn:log=b", "--", "occupied.txt")
}

// Per-path receipts are the point. Subversion does not fail the whole call when
// one path is refused - it notifies and carries on - so a verb that reported
// only its exit status would turn "somebody else holds this file" into silence,
// which is the one thing a reservation must never be.
func TestRALockReportsEachPathSeparately(t *testing.T) {
	f := newFixture(t, "old.txt")
	holder := f.second(t, "holder")
	rival := f.second(t, "rival")

	got := f.jsonCall(t, true, "lock", "--disposable-wc", holder, "-m", "rezerwacja", "--", "occupied.txt")
	locked, _ := got["locked"].([]any)
	if len(locked) != 1 || locked[0].(map[string]any)["ok"] != true {
		t.Fatalf("lock = %#v", got["locked"])
	}

	// The contested attempt: the process succeeds, the path does not.
	contested := f.jsonCall(t, true, "lock", "--disposable-wc", rival, "--", "occupied.txt")
	entries, _ := contested["locked"].([]any)
	if len(entries) != 1 {
		t.Fatalf("contested lock = %#v", contested["locked"])
	}
	row := entries[0].(map[string]any)
	if row["ok"] != false {
		t.Fatalf("a held path must not report success: %#v", row)
	}
	if reason, _ := row["error"].(string); !strings.Contains(reason, "locked") {
		t.Fatalf("refusal must say why: %#v", row["error"])
	}

	released := f.jsonCall(t, true, "unlock", "--disposable-wc", holder, "--", "occupied.txt")
	unlocked, _ := released["unlocked"].([]any)
	if len(unlocked) != 1 || unlocked[0].(map[string]any)["ok"] != true {
		t.Fatalf("unlock = %#v", released["unlocked"])
	}
}

// Subversion can steal and break locks; this helper cannot, and that is a
// product decision rather than an omission. Taking a reservation away from
// whoever holds it is not an accepted mechanism here, and a capability the
// product has not accepted has no business being one keystroke away.
func TestRALockCannotStealOrBreak(t *testing.T) {
	f := newFixture(t, "old.txt")
	wc := f.second(t, "thief")
	f.jsonCall(t, false, "lock", "--disposable-wc", wc, "--steal", "--", "occupied.txt")
	f.jsonCall(t, false, "unlock", "--disposable-wc", wc, "--break", "--", "occupied.txt")
}
