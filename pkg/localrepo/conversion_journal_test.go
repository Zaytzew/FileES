package localrepo

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func conversionFixture(t *testing.T) (*ConversionJournal, Record) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "lifecycle.json"))
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := s.EnsureConfiguredAttached("lab", uuid.NewString(), "svn+ssh://example/repo", "rw", filepath.Join(t.TempDir(), "Zażółć WC"), "Zażółć")
	if err != nil {
		t.Fatal(err)
	}
	j, err := CreateConversionJournal(filepath.Join(t.TempDir(), "conversion.json"), r)
	if err != nil {
		t.Fatal(err)
	}
	return j, r
}

func conversionPrepared(t *testing.T, j *ConversionJournal) ConversionRecord {
	t.Helper()
	r, err := j.Prepare(j.Get(), "volume:12/file:345", 0, filepath.Join(t.TempDir(), "inventory.ndjson"), strings.Repeat("ab", 32), 7)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestConversionJournalPersistsEveryBoundaryAndRejectsStaleReceipts(t *testing.T) {
	j, attachment := conversionFixture(t)
	waiting := j.Get()
	if waiting.AttachmentID != attachment.OperationID || waiting.SourcePath != attachment.LocalPath || filepath.Base(waiting.BackupPath) != ".Zażółć WC-converting" {
		t.Fatalf("wrong attachment: %+v", waiting)
	}
	r := conversionPrepared(t, j)
	if r.Revision != 0 || !r.Prepared || r.Phase != ConversionRetaining {
		t.Fatalf("r0 was not preserved: %+v", r)
	}
	for _, phase := range []ConversionPhase{ConversionRetained, ConversionCreating, ConversionPreserving, ConversionVerifying, ConversionCleanup, ConversionComplete} {
		var err error
		j, err = OpenConversionJournal(j.path)
		if err != nil || j.Get() != r {
			t.Fatalf("restart lost receipt: %v", err)
		}
		old := r
		switch phase {
		case ConversionRetained:
			r, err = j.MarkRetained(r)
		case ConversionCreating:
			r, err = j.BeginPoint(r)
		case ConversionPreserving:
			r, err = j.MarkPointReady(r, "volume:12/file:678", "FileES/repo")
		case ConversionVerifying:
			r, err = j.MarkPreserved(r)
		case ConversionCleanup:
			r, err = j.MarkVerified(r)
		case ConversionComplete:
			r, err = j.MarkComplete(r)
		}
		if err != nil || r.Phase != phase {
			t.Fatalf("advance %s: %+v %v", phase, r, err)
		}
		if _, err := j.Block(old, errors.New("stale failure")); err == nil || j.Get() != r {
			t.Fatal("old observer overwrote newer receipt")
		}
	}
	if _, err := os.Stat(attachment.LocalPath); !os.IsNotExist(err) {
		t.Fatalf("journal touched WC path: %v", err)
	}
	if _, err := os.Stat(waiting.BackupPath); !os.IsNotExist(err) {
		t.Fatalf("journal touched backup path: %v", err)
	}
}

func TestConversionJournalFailureSurvivesRestartWithoutAdvancing(t *testing.T) {
	j, _ := conversionFixture(t)
	r := conversionPrepared(t, j)
	blocked, err := j.Block(r, errors.New("destination occupied; retained source is safe"))
	if err != nil {
		t.Fatal(err)
	}
	j, err = OpenConversionJournal(j.path)
	if err != nil || j.Get() != blocked {
		t.Fatalf("lost blocked receipt: %v", err)
	}
	if _, err := j.MarkRetained(blocked); err == nil {
		t.Fatal("known failed operation replayed without retry")
	}
	r, err = j.Retry(blocked)
	if err != nil || r.Phase != ConversionRetaining || r.Blocked != "" || r.SourceFileID != blocked.SourceFileID {
		t.Fatalf("retry lost boundary: %+v %v", r, err)
	}
	if _, err := j.MarkRetained(r); err != nil {
		t.Fatal(err)
	}
}

func TestConversionJournalRefusesOverwriteSkippingAndUnboundEvidence(t *testing.T) {
	j, attachment := conversionFixture(t)
	if _, err := CreateConversionJournal(j.path, attachment); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwriting another intent: %v", err)
	}
	if _, err := j.MarkComplete(j.Get()); err == nil {
		t.Fatal("skipped entire transaction")
	}
	for _, mutate := range []func(*Record){
		func(r *Record) { r.Anchor, r.Sparse = true, true },
		func(r *Record) { r.Sparse = true },
		func(r *Record) { r.State = StateDetaching },
		func(r *Record) { r.RelocationQueued = true },
		func(r *Record) { r.LastError = "not ready" },
	} {
		copy := attachment
		mutate(&copy)
		if _, err := CreateConversionJournal(filepath.Join(t.TempDir(), "intent"), copy); err == nil {
			t.Fatalf("unsupported attachment accepted: %+v", copy)
		}
	}
	for _, args := range []struct {
		fileID, digest string
		revision       int64
		plan           string
	}{
		{"", strings.Repeat("ab", 32), 3, filepath.Join(t.TempDir(), "plan")},
		{"file:1", "xyz", 3, filepath.Join(t.TempDir(), "plan")},
		{"file:1", strings.Repeat("ab", 32), -1, filepath.Join(t.TempDir(), "plan")},
		{"file:1", strings.Repeat("ab", 32), 3, filepath.Join(attachment.LocalPath, "plan")},
		{"file:1", strings.Repeat("ab", 32), 3, filepath.Join(j.Get().BackupPath, "plan")},
	} {
		if _, err := j.Prepare(j.Get(), args.fileID, args.revision, args.plan, args.digest, 0); err == nil {
			t.Fatal("unbound source snapshot accepted")
		}
	}
	r := conversionPrepared(t, j)
	r, _ = j.MarkRetained(r)
	r, _ = j.BeginPoint(r)
	if _, err := j.MarkPointReady(r, r.SourceFileID, "FileES"); err == nil {
		t.Fatal("old retained WC accepted as the new point")
	}
	if _, err := j.MarkPointReady(r, "other-id", ""); err == nil {
		t.Fatal("unconfirmed provider accepted")
	}
}

func TestConversionJournalRollbackAndCorruptionFailClosed(t *testing.T) {
	j, attachment := conversionFixture(t)
	if _, err := CreateConversionJournal(filepath.Join(attachment.LocalPath, "journal.json"), attachment); err == nil {
		t.Fatal("journal would move with source")
	}
	r := conversionPrepared(t, j)
	if _, err := j.MarkRolledBack(r, r.SourceFileID); err == nil {
		t.Fatal("rollback effect lacked durable direction")
	}
	r, err := j.BeginRollback(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.MarkRolledBack(r, "wrong-id"); err == nil {
		t.Fatal("rollback over unknown root accepted")
	}
	r, err = j.MarkRolledBack(r, r.SourceFileID)
	if err != nil || r.Phase != ConversionRolledBack {
		t.Fatalf("rollback receipt=%+v %v", r, err)
	}
	if _, err := j.Retry(r); err == nil {
		t.Fatal("terminal receipt replayed")
	}
	raw, err := os.ReadFile(j.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, corrupt := range []string{string(raw[:len(raw)/2]), string(raw) + "{}", strings.Replace(string(raw), `"schema":`, `"future":true,"schema":`, 1), strings.Replace(string(raw), `"phase":"rolled_back"`, `"phase":"unknown"`, 1)} {
		if err := os.WriteFile(j.path, []byte(corrupt), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenConversionJournal(j.path); err == nil {
			t.Fatal("damaged journal accepted")
		}
	}
}

func TestConversionJournalCancelOnlyBeforePreparation(t *testing.T) {
	j, _ := conversionFixture(t)
	r, err := j.Cancel(j.Get())
	if err != nil || r.Phase != ConversionCanceled {
		t.Fatalf("cancel=%+v %v", r, err)
	}
	j, err = OpenConversionJournal(j.path)
	if err != nil || j.Get() != r {
		t.Fatalf("cancel restart: %v", err)
	}
	if _, err := j.Prepare(r, "id", 0, filepath.Join(t.TempDir(), "plan"), strings.Repeat("ab", 32), 0); err == nil {
		t.Fatal("canceled intent executed")
	}
	j, _ = conversionFixture(t)
	r = conversionPrepared(t, j)
	if _, err := j.Cancel(r); err == nil {
		t.Fatal("prepared operation canceled without filesystem rollback")
	}
}

func TestConversionJournalBoundsFailureAndRollbackOrigin(t *testing.T) {
	j, _ := conversionFixture(t)
	r, err := j.Block(j.Get(), errors.New(strings.Repeat("żółć", 5000)))
	if err != nil || len(r.Blocked) > 16384 || !strings.Contains(r.Blocked, "truncated") {
		t.Fatalf("large error=%d %v", len(r.Blocked), err)
	}
	j, err = OpenConversionJournal(j.path)
	if err != nil || j.Get() != r {
		t.Fatalf("bounded error restart: %v", err)
	}
	if _, err := j.Retry(r); err != nil {
		t.Fatal(err)
	}
	r = conversionPrepared(t, j)
	r, err = j.BeginRollback(r)
	if err != nil {
		t.Fatal(err)
	}
	bad := r
	bad.RollbackFrom = ConversionCleanup
	if err := validateConversion(bad); err == nil {
		t.Fatal("rollback from irreversible cleanup accepted")
	}
	bad = r
	bad.RollbackFrom = ""
	if err := validateConversion(bad); err == nil {
		t.Fatal("rollback with no durable origin accepted")
	}
}
