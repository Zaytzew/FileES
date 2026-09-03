package detachment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "detachments.json")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store, path
}

func TestARecordedDetachmentSurvivesReopening(t *testing.T) {
	store, path := openTemp(t)
	at := time.Date(2026, 9, 3, 17, 40, 0, 0, time.UTC)
	rec := Record{
		ServerID: "manual", DisplayName: "manual", Address: "manual.example",
		Cause: CauseSelf, At: at,
		WorkingCopies: []string{`C:\Projekty\Willa`, `C:\Projekty\Biurowiec`},
	}
	if err := store.Record(rec); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// The whole point of the store is that a rebuild of the desktop pair does
	// not reset a forty-eight hour lifetime.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := reopened.ListAt(at.Add(time.Hour))
	if len(got) != 1 {
		t.Fatalf("ListAt after reopen = %d records, want 1", len(got))
	}
	if got[0].Name() != "manual" || got[0].Cause != CauseSelf {
		t.Fatalf("record = %+v", got[0])
	}
	if len(got[0].WorkingCopies) != 2 || got[0].WorkingCopies[0] != `C:\Projekty\Willa` {
		t.Fatalf("working copies = %v", got[0].WorkingCopies)
	}
}

func TestARecordDisappearsAfterVisibility(t *testing.T) {
	store, _ := openTemp(t)
	at := time.Date(2026, 9, 3, 17, 40, 0, 0, time.UTC)
	if err := store.Record(Record{ServerID: "manual", Cause: CauseSelf, At: at}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got := store.ListAt(at.Add(Visibility - time.Minute)); len(got) != 1 {
		t.Fatalf("just inside visibility = %d records, want 1", len(got))
	}
	if got := store.ListAt(at.Add(Visibility)); len(got) != 0 {
		t.Fatalf("at visibility = %d records, want 0", len(got))
	}
}

func TestExpiryIsPersistedSoAStoppedDaemonDoesNotResurrectARow(t *testing.T) {
	store, path := openTemp(t)
	at := time.Date(2026, 9, 3, 17, 40, 0, 0, time.UTC)
	if err := store.Record(Record{ServerID: "manual", Cause: CauseSelf, At: at}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// A read past the lifetime prunes. Without persisting that, a daemon
	// restarted afterwards would load the expired record and, if its clock or
	// the record's were ever disagreed upon, show it again.
	if got := store.ListAt(at.Add(Visibility + time.Hour)); len(got) != 0 {
		t.Fatalf("expired list = %d records, want 0", len(got))
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := reopened.ListAt(at); len(got) != 0 {
		t.Fatalf("reopened at original moment = %d records, want 0 (pruned on disk)", len(got))
	}
}

func TestASecondDetachmentReplacesTheFirstForTheSameServer(t *testing.T) {
	store, _ := openTemp(t)
	first := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	second := first.Add(3 * time.Hour)
	if err := store.Record(Record{ServerID: "manual", Cause: CauseRevoked, At: first}); err != nil {
		t.Fatalf("Record first: %v", err)
	}
	if err := store.Record(Record{ServerID: "manual", Cause: CauseSelf, At: second}); err != nil {
		t.Fatalf("Record second: %v", err)
	}
	got := store.ListAt(second.Add(time.Minute))
	if len(got) != 1 {
		t.Fatalf("ListAt = %d records, want 1: a server cannot be detached from twice at once", len(got))
	}
	if got[0].Cause != CauseSelf || !got[0].At.Equal(second) {
		t.Fatalf("record = %+v, want the later detachment", got[0])
	}
}

// Re-activation must stop the panel showing the row without erasing what
// happened. The two readers want opposite things and a record that deleted
// itself would satisfy one by lying to the other.
func TestReattachingMarksTheRecordAndDoesNotDeleteIt(t *testing.T) {
	store, path := openTemp(t)
	at := time.Date(2026, 9, 3, 17, 40, 0, 0, time.UTC)
	back := at.Add(time.Hour)
	if err := store.Record(Record{ServerID: "manual", Cause: CauseRevoked, At: at}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	changed, err := store.Reattached("manual", back)
	if err != nil || !changed {
		t.Fatalf("Reattached = %v, %v; want true, nil", changed, err)
	}
	// It fires on every successful cycle, so it must be idempotent and must
	// not keep rewriting the file.
	if again, err := store.Reattached("manual", at.Add(2*time.Hour)); err != nil || again {
		t.Fatalf("second Reattached = %v, %v; want false, nil", again, err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := reopened.ListAt(back)
	if len(got) != 1 {
		t.Fatalf("after Reattached = %d records, want 1: the detachment still happened", len(got))
	}
	if got[0].Current() {
		t.Error("the record is still current although the client is back")
	}
	if got[0].ReattachedAt == nil || !got[0].ReattachedAt.Equal(back) {
		t.Errorf("ReattachedAt = %v, want %v", got[0].ReattachedAt, back)
	}
	if !got[0].At.Equal(at) {
		t.Errorf("At = %v; the moment it happened must not move", got[0].At)
	}
}

// A detachment after a re-activation is a new ending, not a continuation of
// the old one, so it comes back as current with its own moment.
func TestDetachingAgainAfterReattachingIsCurrentOnceMore(t *testing.T) {
	store, _ := openTemp(t)
	first := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	if err := store.Record(Record{ServerID: "manual", Cause: CauseSelf, At: first}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := store.Reattached("manual", first.Add(time.Hour)); err != nil {
		t.Fatalf("Reattached: %v", err)
	}
	second := first.Add(2 * time.Hour)
	if err := store.Record(Record{ServerID: "manual", Cause: CauseSelf, At: second}); err != nil {
		t.Fatalf("second Record: %v", err)
	}
	got := store.ListAt(second.Add(time.Minute))
	if len(got) != 1 || !got[0].Current() || !got[0].At.Equal(second) {
		t.Fatalf("record = %+v, want one current detachment at the later moment", got)
	}
}

// The file is the artefact somebody will eventually open to work out what
// happened, so it must not contain fields that read like answers and are not.
//
// Caught on the owner's live machine within a minute of the first real record:
// encoding/json ignores omitempty on a struct, so a plain time.Time wrote
// "reattached_at": "0001-01-01T00:00:00Z" into a record whose client had never
// been re-activated - a date that is not a date, in the one place that exists
// to be believed.
func TestANeverReattachedRecordWritesNoReattachmentDate(t *testing.T) {
	store, path := openTemp(t)
	at := time.Date(2026, 9, 3, 17, 40, 0, 0, time.UTC)
	if err := store.Record(Record{ServerID: "manual", Cause: CauseRevoked, At: at}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if strings.Contains(string(raw), "reattached_at") {
		t.Errorf("the record carries a reattachment date it never had:\n%s", raw)
	}
	if strings.Contains(string(raw), "0001-01-01") {
		t.Errorf("the record carries a zero time as though it were a moment:\n%s", raw)
	}
}

// A record written before ReattachedAt became a pointer is still current.
//
// The owner's live file already held one when this was found. Read literally,
// its zero date decodes to a valid non-nil time and the record would count as
// re-activated - quietly removing the only detachment he had, from the panel
// built to show it.
func TestAZeroReattachmentDateFromAnOlderRecordIsNotAReattachment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "detachments.json")
	legacy := `{"schema":"` + Schema + `","records":[{"server_id":"manual","display_name":"manual",` +
		`"cause":"revoked","at":"2026-09-03T19:54:51Z","reattached_at":"0001-01-01T00:00:00Z"}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := store.ListAt(time.Date(2026, 9, 3, 20, 0, 0, 0, time.UTC))
	if len(got) != 1 {
		t.Fatalf("ListAt = %d records, want 1", len(got))
	}
	if !got[0].Current() {
		t.Error("a zero date was read as a reattachment; the record would vanish from the panel")
	}
}

func TestNewestFirst(t *testing.T) {
	store, _ := openTemp(t)
	base := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	for i, id := range []string{"spot", "manual", "cloud"} {
		if err := store.Record(Record{ServerID: id, Cause: CauseSelf, At: base.Add(time.Duration(i) * time.Hour)}); err != nil {
			t.Fatalf("Record %s: %v", id, err)
		}
	}
	got := store.ListAt(base.Add(4 * time.Hour))
	if len(got) != 3 {
		t.Fatalf("ListAt = %d records, want 3", len(got))
	}
	if got[0].ServerID != "cloud" || got[2].ServerID != "spot" {
		t.Fatalf("order = %s, %s, %s; want newest first", got[0].ServerID, got[1].ServerID, got[2].ServerID)
	}
}

func TestARecordWithoutACauseIsRefused(t *testing.T) {
	store, _ := openTemp(t)
	if err := store.Record(Record{ServerID: "manual"}); err == nil {
		t.Fatal("Record without a cause succeeded; the two detachments need opposite wording")
	}
	if err := store.Record(Record{Cause: CauseSelf}); err == nil {
		t.Fatal("Record without a server id succeeded")
	}
}

func TestNameFallsBackToTheServerID(t *testing.T) {
	if got := (Record{ServerID: "atmprojekt:filees"}).Name(); got != "atmprojekt:filees" {
		t.Fatalf("Name() = %q", got)
	}
	if got := (Record{ServerID: "id", DisplayName: "  cloud "}).Name(); got != "cloud" {
		t.Fatalf("Name() = %q", got)
	}
}
