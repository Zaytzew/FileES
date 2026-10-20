package commit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/errmap"
	"filees/pkg/talk"
	"filees/pkg/watcher"
)

func integrityFixture(t *testing.T, wc, rel string) AnchorIntegrityRecord {
	t.Helper()
	r := AnchorIntegrityRecord{Path: rel, URL: "svn://fixture/repo", Identity: rel, FileID: "1:2", Revision: 31, Size: 14, SHA256: strings.Repeat("a", 64)}
	if err := BeginAnchorIntegrity(wc, &r, true); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAnchorIntegrityFencesShutdownDrainAndPollerUpdate(t *testing.T) {
	wc := t.TempDir()
	r := integrityFixture(t, wc, "OPZ.pdf")
	abs := filepath.Join(wc, r.Path)
	if err := os.WriteFile(abs, []byte("preserve unknown bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	cli := &stagingClient{statuses: map[string]string{r.Path: "unversioned"}}
	events := make(chan watcher.Event, 1)
	events <- watcher.Event{Path: abs, Rel: r.Path, Type: watcher.EntryFile, Op: watcher.Added}
	close(events)
	s := &Service{Cli: cli, Rules: Rules{NewLatency: time.Hour, MaxBatchFiles: 10, ShutdownTimeout: time.Second}}
	s.Run(context.Background(), "repo", wc, events)
	if cli.commits != 0 || cli.adds != 0 || len(s.staging) != 1 {
		t.Fatalf("shutdown escaped: commits=%d adds=%d staged=%d", cli.commits, cli.adds, len(s.staging))
	}
	poll := &revisionClient{remote: 32, local: 31}
	p := &Service{Cli: poll, RepoURL: "svn://fixture/repo", Logger: talk.With("integrity-test")}
	p.pollOnce(t.Context(), wc, filepath.Join(wc, "head.rev"))
	if poll.update != 0 {
		t.Fatal("poller overwrote unresolved local bytes")
	}
}

func TestAnchorIntegrityNewDownloadInvalidatesOnlyMatchingVerifiedProof(t *testing.T) {
	wc := t.TempDir()
	r := integrityFixture(t, wc, "OPZ.pdf")
	if err := FinishAnchorIntegrity(wc, r, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := BeginAnchorIntegrity(wc, &r, true); err != nil {
		t.Fatal(err)
	}
	if err := CheckAnchorIntegrity(wc, []string{r.Path}); err == nil {
		t.Fatal("new partial fetch inherited stale verified receipt")
	}
	if err := FinishAnchorIntegrity(wc, r, false, "unexplained bytes"); err != nil {
		t.Fatal(err)
	}
	changed := r
	changed.FileID = "replaced object"
	if err := BeginAnchorIntegrity(wc, &changed, true); err == nil {
		t.Fatal("replacement erased an unresolved hold")
	}
}

func TestAnchorIntegrityPersistsAndScopesPublication(t *testing.T) {
	wc := t.TempDir()
	r := integrityFixture(t, wc, "folder/OPZ.pdf")
	for _, paths := range [][]string{{r.Path}, {"folder"}, {"folder/OPZ.pdf/child"}, {"FOLDER/OPZ.PDF"}, {"."}} {
		if err := CheckAnchorIntegrity(wc, paths); err == nil {
			t.Fatalf("pending bypass: %v", paths)
		}
	}
	if err := CheckAnchorIntegrity(wc, []string{"unrelated.txt", "folder-other"}); err != nil {
		t.Fatal("overbroad fence", err)
	}
	if err := FinishAnchorIntegrity(wc, r, false, "checksum mismatch"); err != nil {
		t.Fatal(err)
	}
	if err := BeginAnchorIntegrity(wc, &r, false); err != nil {
		t.Fatal(err)
	}
	after, err := ReadAnchorIntegrity(wc, r.Path)
	if err != nil || after.Phase != "held" {
		t.Fatal("retry erased hold", after, err)
	}
	// No in-memory state is involved: the next process/build reads this file.
	err = (&Service{}).checkDownloadIntegrity(wc, []string{r.Path})
	if err == nil || errmap.Classify(err).Key != "sync.download_integrity_held" {
		t.Fatal("restart/nocfapi bypass", err)
	}
	stale := r
	stale.FileID = "different"
	if err := FinishAnchorIntegrity(wc, stale, true, ""); err == nil {
		t.Fatal("stale proof accepted")
	}
	if err := CheckAnchorIntegrity(wc, []string{r.Path}); err == nil {
		t.Fatal("stale proof erased fence")
	}
	if err := FinishAnchorIntegrity(wc, r, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := CheckAnchorIntegrity(wc, []string{r.Path}); err != nil {
		t.Fatal("verified delivery held", err)
	}
}

func TestAnchorIntegrityUnknownReferenceAndMalformedStateFailClosed(t *testing.T) {
	wc := t.TempDir()
	r := AnchorIntegrityRecord{Path: "x.pdf", URL: "svn://fixture/repo", Identity: "x.pdf", FileID: "1:2", Revision: 31, Size: 14}
	if err := BeginAnchorIntegrity(wc, &r, false); err != nil {
		t.Fatal(err)
	}
	if err := CheckAnchorIntegrity(wc, []string{r.Path}); err == nil {
		t.Fatal("missing reference accepted")
	}
	if err := FinishAnchorIntegrity(wc, r, true, ""); err == nil {
		t.Fatal("empty hash verified")
	}
	dir, err := integrityDir(wc, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, integrityName(r.Path)), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckAnchorIntegrity(wc, []string{"unrelated.txt"}); err == nil {
		t.Fatal("malformed receipt ignored")
	}
}

func TestAnchorIntegrityLateCompletionCannotBlessAnotherDownload(t *testing.T) {
	wc := t.TempDir()
	old := integrityFixture(t, wc, "OPZ.pdf")
	if err := FinishAnchorIntegrity(wc, old, true, ""); err != nil {
		t.Fatal(err)
	}
	next := old
	if err := BeginAnchorIntegrity(wc, &next, true); err != nil {
		t.Fatal(err)
	}
	if next.Generation == old.Generation {
		t.Fatal("partial transfer reused the previous validation generation")
	}
	if err := FinishAnchorIntegrity(wc, old, true, ""); err == nil {
		t.Fatal("late verification erased the new transfer fence")
	}
	if err := CheckAnchorIntegrity(wc, []string{old.Path}); err == nil {
		t.Fatal("new unverified bytes became publishable")
	}
}

func TestAnchorIntegrityFencesAutomaticAndManualPublishWithoutDiscardingEdits(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic", true: "manual"}[manual], func(t *testing.T) {
			wc := t.TempDir()
			path := filepath.Join(wc, "OPZ.pdf")
			bytes := []byte("preserve local edits")
			if err := os.WriteFile(path, bytes, 0600); err != nil {
				t.Fatal(err)
			}
			r := integrityFixture(t, wc, "OPZ.pdf")
			cli := &stagingClient{statuses: map[string]string{"OPZ.pdf": "modified"}, revision: 32}
			s := &Service{Cli: cli, RepoURL: r.URL, Rules: Rules{ModifiedQuiet: time.Nanosecond, MaxBatchFiles: 10}, repoID: "fixture", staging: map[string]*stageItem{}, cachePath: filepath.Join(wc, ".filees", "commit_cache", "cache.json")}
			s.acceptEvent(watcher.Event{Path: path, Rel: "OPZ.pdf", Type: watcher.EntryFile, Op: watcher.Modified})
			publish := func() error {
				if manual {
					_, err := s.RequestPublish(t.Context(), wc, "preserve edits")
					return err
				}
				return s.tryCommit(t.Context(), wc)
			}
			if err := publish(); err == nil {
				t.Fatal("unverified bytes published")
			}
			if cli.commits != 0 || cli.adds != 0 || len(s.staging) != 1 {
				t.Fatalf("state mutated: commits=%d adds=%d staged=%d", cli.commits, cli.adds, len(s.staging))
			}
			got, _ := os.ReadFile(path)
			if string(got) != string(bytes) {
				t.Fatal("local bytes changed")
			}
			if err := FinishAnchorIntegrity(wc, r, true, ""); err != nil {
				t.Fatal(err)
			}
			if err := publish(); err != nil {
				t.Fatal("legal subsequent edit blocked", err)
			}
			if cli.commits != 1 {
				t.Fatalf("commits=%d", cli.commits)
			}
		})
	}
}
