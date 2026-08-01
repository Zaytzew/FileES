package repoworker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	control "filees/pkg/control/v1"

	"github.com/google/uuid"
)

func requireLoadDumpTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"svnadmin", "svnlook", "svn", "svndumpfilter"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
}

// buildCarrierRepo creates a repository under root/repos/<repoID>, commits
// carrierBytes as its sole r1 file via a real svn checkout+commit (so
// extractCarrier's svnlook cat sees a genuine committed blob, not a
// hand-assembled FSFS), and writes the matching canonical ownership record.
func buildCarrierRepo(t *testing.T, root, serviceWC, repoID, ownerRealm string, carrierBytes []byte, carrierName string) string {
	t.Helper()
	reposRoot := filepath.Join(root, "repos")
	if err := os.MkdirAll(reposRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	repoPath := filepath.Join(reposRoot, repoID)
	if out, err := exec.Command("svnadmin", "create", repoPath).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v: %s", err, out)
	}
	wc := filepath.Join(root, "wc-"+repoID)
	if out, err := exec.Command("svn", "checkout", "-q", "file://"+repoPath, wc).CombinedOutput(); err != nil {
		t.Fatalf("svn checkout: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(wc, carrierName), carrierBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("svn", "add", "-q", filepath.Join(wc, carrierName)).CombinedOutput(); err != nil {
		t.Fatalf("svn add: %v: %s", err, out)
	}
	if out, err := exec.Command("svn", "commit", "-q", "-m", "carrier", wc).CombinedOutput(); err != nil {
		t.Fatalf("svn commit: %v: %s", err, out)
	}

	record := repositoryRecord{
		Schema: RepositorySchema, RepoID: repoID, OwnerRealmID: ownerRealm,
		DisplayName: "test", URL: "svn+ssh://_filees-client@example/" + repoID,
		State: "active", CreatedAt: time.Now().UTC(),
	}
	recordPath, err := repositoryRecordPath(serviceWC, repoID)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(recordPath, record); err != nil {
		t.Fatal(err)
	}
	return reposRoot
}

// realDumpBytes produces a genuine SVN dump (via a throwaway source repo) so
// carrier content is a dump svnadmin will actually load, not a synthetic
// fixture with its own risk of not matching real tool behavior.
func realDumpBytes(t *testing.T, root string, files map[string]string) []byte {
	t.Helper()
	src := filepath.Join(root, "dumpsrc-"+uuid.NewString())
	if out, err := exec.Command("svnadmin", "create", src).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create dumpsrc: %v: %s", err, out)
	}
	wc := src + "-wc"
	if out, err := exec.Command("svn", "checkout", "-q", "file://"+src, wc).CombinedOutput(); err != nil {
		t.Fatalf("svn checkout dumpsrc: %v: %s", err, out)
	}
	for name, content := range files {
		full := filepath.Join(wc, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("svn", "add", "-q", "--parents", full).CombinedOutput(); err != nil {
			t.Fatalf("svn add: %v: %s", err, out)
		}
	}
	if out, err := exec.Command("svn", "commit", "-q", "-m", "seed", wc).CombinedOutput(); err != nil {
		t.Fatalf("svn commit dumpsrc: %v: %s", err, out)
	}
	out, err := exec.Command("svnadmin", "dump", "--quiet", src).CombinedOutput()
	if err != nil {
		t.Fatalf("svnadmin dump: %v: %s", err, out)
	}
	return out
}

func testDumpLoadService(root, serviceWC, reposRoot string) DumpLoadService {
	svnadmin, _ := exec.LookPath("svnadmin")
	svnlook, _ := exec.LookPath("svnlook")
	svndumpfilter, _ := exec.LookPath("svndumpfilter")
	return DumpLoadService{
		ServiceWC: serviceWC, RepositoriesRoot: reposRoot,
		ArchiveDir:    filepath.Join(root, "archive"),
		DataAuthzFile: filepath.Join(root, "data-authz.conf"),
		SVNAdmin:      svnadmin, SVNLook: svnlook, SVNDumpFilter: svndumpfilter,
	}
}

func TestDumpLoadServiceFullCycle(t *testing.T) {
	requireLoadDumpTools(t)
	root := t.TempDir()
	serviceWC := filepath.Join(root, "service")
	realm := uuid.NewString()
	repoID := uuid.NewString()
	dump := realDumpBytes(t, root, map[string]string{"docs/a.txt": "hello\n", "docs/b.tmp": "junk\n"})
	reposRoot := buildCarrierRepo(t, root, serviceWC, repoID, realm, dump, "carrier.dump")

	svc := testDumpLoadService(root, serviceWC, reposRoot)
	loaded, err := svc.Load(context.Background(), realm, repoID, uuid.NewString(), false, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.OldUUID == "" || loaded.NewUUID == "" || loaded.OldUUID == loaded.NewUUID {
		t.Fatalf("loaded = %+v, want distinct old/new UUIDs", loaded)
	}
	if loaded.SourceRevisionRange != "r1:r1" {
		t.Fatalf("SourceRevisionRange = %q, want r1:r1", loaded.SourceRevisionRange)
	}
	if loaded.ToolVersions["svnadmin"] == "" {
		t.Fatalf("loaded.ToolVersions missing svnadmin: %+v", loaded)
	}

	repoPath := filepath.Join(reposRoot, repoID)
	tree, err := exec.Command("svnlook", "tree", "--full-paths", "-r", "1", repoPath).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tree), "docs/a.txt") || !strings.Contains(string(tree), "docs/b.tmp") {
		t.Fatalf("new generation missing loaded (unfiltered) content: %s", tree)
	}
	confRaw, err := os.ReadFile(filepath.Join(repoPath, "conf", "svnserve.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(confRaw), "authz-db = "+svc.DataAuthzFile) {
		t.Fatalf("new generation conf/svnserve.conf does not point at the configured data authz file %q: %s", svc.DataAuthzFile, confRaw)
	}
}

func TestDumpLoadServiceAppliesIgnorePolicy(t *testing.T) {
	requireLoadDumpTools(t)
	root := t.TempDir()
	serviceWC := filepath.Join(root, "service")
	realm := uuid.NewString()
	repoID := uuid.NewString()
	dump := realDumpBytes(t, root, map[string]string{"docs/a.txt": "hello\n", "docs/b.tmp": "junk\n"})
	reposRoot := buildCarrierRepo(t, root, serviceWC, repoID, realm, dump, "carrier.dump")

	svc := testDumpLoadService(root, serviceWC, reposRoot)
	if _, err := svc.Load(context.Background(), realm, repoID, uuid.NewString(), true, nil); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tree, err := exec.Command("svnlook", "tree", "--full-paths", "-r", "1", filepath.Join(reposRoot, repoID)).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(tree), "b.tmp") {
		t.Fatalf("ignore policy did not filter b.tmp: %s", tree)
	}
	if !strings.Contains(string(tree), "a.txt") {
		t.Fatalf("ignore policy filtered a file it should not have: %s", tree)
	}
}

func TestDumpLoadServiceKeepLastRevisions(t *testing.T) {
	requireLoadDumpTools(t)
	root := t.TempDir()
	serviceWC := filepath.Join(root, "service")
	realm := uuid.NewString()
	repoID := uuid.NewString()

	// Build a five-revision dump directly (each revision changes the same
	// file), independent of the one-shot realDumpBytes helper.
	src := filepath.Join(root, "multirev")
	if out, err := exec.Command("svnadmin", "create", src).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v: %s", err, out)
	}
	wc := src + "-wc"
	if out, err := exec.Command("svn", "checkout", "-q", "file://"+src, wc).CombinedOutput(); err != nil {
		t.Fatalf("checkout: %v: %s", err, out)
	}
	for i := 1; i <= 5; i++ {
		f := filepath.Join(wc, "f.txt")
		if err := os.WriteFile(f, []byte(fmt.Sprintf("rev %d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			exec.Command("svn", "add", "-q", f).Run()
		}
		if out, err := exec.Command("svn", "commit", "-q", "-m", fmt.Sprintf("r%d", i), wc).CombinedOutput(); err != nil {
			t.Fatalf("commit %d: %v: %s", i, err, out)
		}
	}
	dump, err := exec.Command("svnadmin", "dump", "--quiet", src).CombinedOutput()
	if err != nil {
		t.Fatalf("dump: %v: %s", err, dump)
	}

	reposRoot := buildCarrierRepo(t, root, serviceWC, repoID, realm, dump, "carrier.dump")
	svc := testDumpLoadService(root, serviceWC, reposRoot)
	keep := 2
	loaded, err := svc.Load(context.Background(), realm, repoID, uuid.NewString(), false, &keep)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// SourceRevisionRange preserves traceability to the ORIGINAL source
	// numbering (r4:r5 of the 5-revision source).
	if loaded.SourceRevisionRange != "r4:r5" {
		t.Fatalf("SourceRevisionRange = %q, want r4:r5 (keep_last_revisions=2 of 5)", loaded.SourceRevisionRange)
	}
	// The new generation's OWN local numbering is unrelated to the source
	// numbers: svnadmin load into an empty target always renumbers
	// sequentially from 1, verified live (disambiguated from an earlier,
	// coincidentally ambiguous manual test — see LOAD_REPOSITORY_DUMP_CONCEPT.md
	// §5.4). Two revisions were loaded, so the new generation's head is 2.
	head, err := exec.Command("svnlook", "youngest", filepath.Join(reposRoot, repoID)).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(head)) != "2" {
		t.Fatalf("new generation head = %s, want 2 (sequential renumbering from 1, not source numbers preserved)", head)
	}
}

func TestDumpLoadServiceRejectsForeignRealm(t *testing.T) {
	requireLoadDumpTools(t)
	root := t.TempDir()
	serviceWC := filepath.Join(root, "service")
	owner, attacker := uuid.NewString(), uuid.NewString()
	repoID := uuid.NewString()
	dump := realDumpBytes(t, root, map[string]string{"a.txt": "hi\n"})
	reposRoot := buildCarrierRepo(t, root, serviceWC, repoID, owner, dump, "carrier.dump")

	svc := testDumpLoadService(root, serviceWC, reposRoot)
	if _, err := svc.Load(context.Background(), attacker, repoID, uuid.NewString(), false, nil); err == nil {
		t.Fatal("a realm that does not own the repository was allowed to load a dump into it")
	}
}

func TestDumpLoadServiceRejectsRepoWithMoreThanCarrier(t *testing.T) {
	requireLoadDumpTools(t)
	root := t.TempDir()
	serviceWC := filepath.Join(root, "service")
	realm := uuid.NewString()
	repoID := uuid.NewString()
	dump := realDumpBytes(t, root, map[string]string{"a.txt": "hi\n"})
	reposRoot := buildCarrierRepo(t, root, serviceWC, repoID, realm, dump, "carrier.dump")

	// A second commit on the "fresh" repo — no longer just a carrier.
	wc := filepath.Join(root, "extra-wc")
	if out, err := exec.Command("svn", "checkout", "-q", "file://"+filepath.Join(reposRoot, repoID), wc).CombinedOutput(); err != nil {
		t.Fatalf("checkout: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(wc, "extra.txt"), []byte("real content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("svn", "add", "-q", filepath.Join(wc, "extra.txt")).CombinedOutput(); err != nil {
		t.Fatalf("add: %v: %s", err, out)
	}
	if out, err := exec.Command("svn", "commit", "-q", "-m", "real work", wc).CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %s", err, out)
	}

	svc := testDumpLoadService(root, serviceWC, reposRoot)
	if _, err := svc.Load(context.Background(), realm, repoID, uuid.NewString(), false, nil); err == nil {
		t.Fatal("LOAD_REPOSITORY_DUMP proceeded on a repository with real content beyond the carrier")
	}
	// The repository must be completely untouched by the rejected attempt.
	head, err := exec.Command("svnlook", "youngest", filepath.Join(reposRoot, repoID)).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(head)) != "2" {
		t.Fatalf("repository head changed after a rejected load: %s", head)
	}
}

func TestDumpLoadServiceRejectsMultiFileCarrier(t *testing.T) {
	requireLoadDumpTools(t)
	root := t.TempDir()
	serviceWC := filepath.Join(root, "service")
	realm := uuid.NewString()
	repoID := uuid.NewString()

	reposRoot := filepath.Join(root, "repos")
	os.MkdirAll(reposRoot, 0o700)
	repoPath := filepath.Join(reposRoot, repoID)
	exec.Command("svnadmin", "create", repoPath).Run()
	wc := filepath.Join(root, "wc")
	exec.Command("svn", "checkout", "-q", "file://"+repoPath, wc).Run()
	os.WriteFile(filepath.Join(wc, "carrier.dump"), realDumpBytes(t, root, map[string]string{"x.txt": "x\n"}), 0o644)
	os.WriteFile(filepath.Join(wc, "sibling.txt"), []byte("not the carrier\n"), 0o644)
	exec.Command("svn", "add", "-q", filepath.Join(wc, "carrier.dump")).Run()
	exec.Command("svn", "add", "-q", filepath.Join(wc, "sibling.txt")).Run()
	if out, err := exec.Command("svn", "commit", "-q", "-m", "two files", wc).CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %s", err, out)
	}
	record := repositoryRecord{Schema: RepositorySchema, RepoID: repoID, OwnerRealmID: realm, DisplayName: "t", URL: "x", State: "active", CreatedAt: time.Now().UTC()}
	recordPath, _ := repositoryRecordPath(serviceWC, repoID)
	if err := atomicJSON(recordPath, record); err != nil {
		t.Fatal(err)
	}

	svc := testDumpLoadService(root, serviceWC, reposRoot)
	if _, err := svc.Load(context.Background(), realm, repoID, uuid.NewString(), false, nil); err == nil {
		t.Fatal("a two-file r1 was accepted as a valid carrier")
	}
}

func TestIgnorePatternArgsTranslatesBuiltinPatterns(t *testing.T) {
	args, err := ignorePatternArgs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		if strings.Contains(a, "**") {
			t.Fatalf("untranslated doublestar leaked into svndumpfilter args: %q", a)
		}
	}
	if len(args) == 0 {
		t.Fatal("no patterns translated")
	}
}

type fakeDumpLoader struct {
	calls                      int
	realm, repoID, operationID string
	applyIgnorePolicy          bool
	keepLastRevisions          *int
	result                     LoadedDump
	err                        error
}

func (f *fakeDumpLoader) Load(_ context.Context, realm, repoID, operationID string, applyIgnorePolicy bool, keepLastRevisions *int) (LoadedDump, error) {
	f.calls++
	f.realm, f.repoID, f.operationID, f.applyIgnorePolicy, f.keepLastRevisions = realm, repoID, operationID, applyIgnorePolicy, keepLastRevisions
	return f.result, f.err
}

func loadDumpTicket(t *testing.T, client, repoID string, applyIgnorePolicy bool) control.Ticket {
	t.Helper()
	tk, err := control.NewTicket(uuid.NewString(), uuid.NewString(), control.TicketLoadRepositoryDump,
		client, control.LoadRepositoryDumpPayload{RepoID: repoID, ApplyCurrentIgnorePolicy: applyIgnorePolicy}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestWorkerLoadRepositoryDumpDispatchesAndIsIdempotent(t *testing.T) {
	repoID := uuid.NewString()
	realm := uuid.NewString()
	loader := &fakeDumpLoader{result: LoadedDump{
		OldUUID: uuid.NewString(), NewUUID: uuid.NewString(),
		SourceRevisionRange: "r1:r5", ToolVersions: map[string]string{"svnadmin": "1.14.5"},
	}}
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{DumpLoader: loader, Store: store}
	session := Session{ClientID: "client-a", RealmID: realm, CanCreateRepositories: true}
	tk := loadDumpTicket(t, session.ClientID, repoID, true)

	first, err := w.Handle(context.Background(), session, tk)
	if err != nil || first.Status != control.ResultOK {
		t.Fatalf("result=%+v err=%v", first, err)
	}
	if loader.calls != 1 || loader.realm != realm || loader.repoID != repoID || !loader.applyIgnorePolicy {
		t.Fatalf("loader not invoked with session realm/payload: %+v", loader)
	}
	if loader.operationID != tk.OperationID {
		t.Fatalf("loader.operationID = %s, want ticket.OperationID %s (crash-recovery traceability)", loader.operationID, tk.OperationID)
	}
	var payload control.LoadRepositoryDumpResult
	if err := control.DecodeResultPayload(first.Result, &payload); err != nil || payload.RepoID != repoID {
		t.Fatalf("result payload=%+v err=%v", payload, err)
	}

	// Replay with the same operation/request must not call the loader again.
	replay, err := (&Worker{DumpLoader: loader, Store: store}).Handle(context.Background(), session, tk)
	if err != nil || replay.CompletedAt != first.CompletedAt || loader.calls != 1 {
		t.Fatalf("replay=%+v calls=%d err=%v", replay, loader.calls, err)
	}
}

func TestWorkerLoadRepositoryDumpRequiresCapability(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{DumpLoader: &fakeDumpLoader{}, Store: store}
	session := Session{ClientID: "client-a", RealmID: uuid.NewString(), CanCreateRepositories: false}
	tk := loadDumpTicket(t, session.ClientID, uuid.NewString(), false)
	result, err := w.Handle(context.Background(), session, tk)
	if err != nil || result.Status != control.ResultError || result.Error.Code != "LOAD_REPOSITORY_DUMP_FORBIDDEN" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestWorkerLoadRepositoryDumpFailureIsTerminal(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	loader := &fakeDumpLoader{err: errors.New("precondition failed")}
	w := &Worker{DumpLoader: loader, Store: store}
	session := Session{ClientID: "client-a", RealmID: uuid.NewString(), CanCreateRepositories: true}
	tk := loadDumpTicket(t, session.ClientID, uuid.NewString(), false)
	result, err := w.Handle(context.Background(), session, tk)
	if err != nil || result.Status != control.ResultError || result.Error.Code != "LOAD_REPOSITORY_DUMP_FAILED" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	// A retry of the same operation must return the same bound failure, not
	// call the loader a second time - w.failure binds the error result.
	replay, err := (&Worker{DumpLoader: loader, Store: store}).Handle(context.Background(), session, tk)
	if err != nil || loader.calls != 1 || replay.Error.Code != result.Error.Code {
		t.Fatalf("replay=%+v calls=%d err=%v", replay, loader.calls, err)
	}
}

func TestRevisionRangeScansDumpHeaders(t *testing.T) {
	var d bytes.Buffer
	d.WriteString("SVN-fs-dump-format-version: 2\n\n")
	d.WriteString("Revision-number: 5\n\n")
	d.WriteString("Revision-number: 7\n\n")
	d.WriteString("Revision-number: 6\n\n")
	low, high, err := revisionRange(d.Bytes())
	if err != nil || low != 5 || high != 7 {
		t.Fatalf("revisionRange = (%d,%d,%v), want (5,7,nil)", low, high, err)
	}
}
