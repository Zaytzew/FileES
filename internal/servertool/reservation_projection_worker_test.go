package servertool

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	reservationv1 "filees/pkg/reservation/v1"
	"filees/pkg/reservationprojection"
)

// realSVNInfoLockXML is svn info -r HEAD --xml --depth infinity output,
// captured empirically against a real local repository with two locked
// files (a.txt at the repo root, sub/b.txt) and one unlocked directory —
// see concepts/RESERVATION_SERVER_EMISSION_WORKPLAN.md progress note. Kept
// verbatim (including the unlocked "sub" dir entry) so the parser is
// exercised against the real shape, not a hand-simplified one.
const realSVNInfoLockXML = `<?xml version="1.0" encoding="UTF-8"?>
<info>
<entry
   path="repo"
   revision="1"
   kind="dir">
<url>file:///tmp/tmp.4LxAP5I7JV/repo</url>
<relative-url>^/</relative-url>
<repository>
<root>file:///tmp/tmp.4LxAP5I7JV/repo</root>
<uuid>c54b24c0-fb1b-4e38-a3b9-bf051b50f697</uuid>
</repository>
<commit
   revision="1">
<author>root</author>
<date>2026-09-01T13:04:04.112907Z</date>
</commit>
</entry>
<entry
   path="sub"
   revision="1"
   kind="dir">
<url>file:///tmp/tmp.4LxAP5I7JV/repo/sub</url>
<relative-url>^/sub</relative-url>
<repository>
<root>file:///tmp/tmp.4LxAP5I7JV/repo</root>
<uuid>c54b24c0-fb1b-4e38-a3b9-bf051b50f697</uuid>
</repository>
<commit
   revision="1">
<author>root</author>
<date>2026-09-01T13:04:04.112907Z</date>
</commit>
</entry>
<entry
   path="sub/b.txt"
   revision="1"
   kind="file"
   size="6">
<url>file:///tmp/tmp.4LxAP5I7JV/repo/sub/b.txt</url>
<relative-url>^/sub/b.txt</relative-url>
<repository>
<root>file:///tmp/tmp.4LxAP5I7JV/repo</root>
<uuid>c54b24c0-fb1b-4e38-a3b9-bf051b50f697</uuid>
</repository>
<commit
   revision="1">
<author>root</author>
<date>2026-09-01T13:04:04.112907Z</date>
</commit>
<lock>
<token>opaquelocktoken:33349f30-b7c4-43aa-a37a-5d678b4dfa31</token>
<owner>root</owner>
<comment>editing b</comment>
<created>2026-09-01T13:04:04.155981Z</created>
</lock>
</entry>
<entry
   size="6"
   path="a.txt"
   revision="1"
   kind="file">
<url>file:///tmp/tmp.4LxAP5I7JV/repo/a.txt</url>
<relative-url>^/a.txt</relative-url>
<repository>
<root>file:///tmp/tmp.4LxAP5I7JV/repo</root>
<uuid>c54b24c0-fb1b-4e38-a3b9-bf051b50f697</uuid>
</repository>
<commit
   revision="1">
<author>root</author>
<date>2026-09-01T13:04:04.112907Z</date>
</commit>
<lock>
<token>opaquelocktoken:5452b021-f6c8-4920-8364-d76b464719eb</token>
<owner>root</owner>
<comment>editing a</comment>
<created>2026-09-01T13:04:04.140107Z</created>
</lock>
</entry>
</info>
`

func TestParseLockXMLAgainstRealSVNOutput(t *testing.T) {
	reservations, err := parseLockXML(strings.NewReader(realSVNInfoLockXML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(reservations) != 2 {
		t.Fatalf("expected 2 locks, got %d: %+v", len(reservations), reservations)
	}
	byPath := map[string]reservationv1.Reservation{}
	for _, r := range reservations {
		byPath[r.Path] = r
	}
	b, ok := byPath["sub/b.txt"]
	if !ok || b.Token != "opaquelocktoken:33349f30-b7c4-43aa-a37a-5d678b4dfa31" || b.OwnerID != "root" || b.Comment != "editing b" || b.CreatedAt != "2026-09-01T13:04:04.155981Z" {
		t.Fatalf("sub/b.txt lock wrong: %+v", b)
	}
	a, ok := byPath["a.txt"]
	if !ok || a.Token != "opaquelocktoken:5452b021-f6c8-4920-8364-d76b464719eb" || a.OwnerID != "root" || a.Comment != "editing a" {
		t.Fatalf("a.txt lock wrong: %+v", a)
	}
	// The unlocked "repo" (root) and "sub" entries must not appear.
	if _, ok := byPath[""]; ok {
		t.Fatal("unlocked root entry leaked into results")
	}
	if _, ok := byPath["sub"]; ok {
		t.Fatal("unlocked sub directory leaked into results")
	}
}

func TestParseLockXMLNoLocksIsEmptyNotError(t *testing.T) {
	const noLocks = `<?xml version="1.0" encoding="UTF-8"?>
<info>
<entry path="repo" revision="1" kind="dir">
<relative-url>^/</relative-url>
</entry>
</info>
`
	reservations, err := parseLockXML(strings.NewReader(noLocks))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reservations) != 0 {
		t.Fatalf("expected zero locks, got %+v", reservations)
	}
}

func TestParseLockXMLRejectsMalformedInput(t *testing.T) {
	const truncatedMidLock = `<?xml version="1.0"?>
<info>
<entry path="a.txt" revision="1" kind="file">
<lock>
<token>opaquelocktoken:abc</token>
`
	if _, err := parseLockXML(strings.NewReader(truncatedMidLock)); err == nil {
		t.Fatal("expected truncated/malformed XML to be rejected")
	}
}

const repoID = "f5d5bfee-62f4-5b9c-b26f-8d4c424fb8f0"

func TestRefreshReservationProjectionFreshEmptyIsAValidZero(t *testing.T) {
	store := reservationprojection.NewStore(t.TempDir())
	result := refreshReservationProjectionWith(store, repoID, func() ([]reservationv1.Reservation, error) { return nil, nil })
	if result.Unknown || result.Stale || len(result.Reservations) != 0 || result.Generation != "1" {
		t.Fatalf("empty-but-confirmed refresh must be a plain fresh result: %+v", result)
	}
}

func TestRefreshReservationProjectionFallsBackToStaleOnFailureWithPriorArtifact(t *testing.T) {
	store := reservationprojection.NewStore(t.TempDir())
	fresh := refreshReservationProjectionWith(store, repoID, func() ([]reservationv1.Reservation, error) {
		return []reservationv1.Reservation{{Path: "a.txt", Token: "tok"}}, nil
	})
	if fresh.Stale || fresh.Unknown {
		t.Fatalf("first refresh must be fresh: %+v", fresh)
	}
	stale := refreshReservationProjectionWith(store, repoID, func() ([]reservationv1.Reservation, error) {
		return nil, errors.New("svn info: connection refused")
	})
	if !stale.Stale || stale.Unknown || len(stale.Reservations) != 1 || stale.Generation != "1" || stale.Detail == "" {
		t.Fatalf("failed refresh with a prior artifact must reply stale, not unknown: %+v", stale)
	}
}

func TestRefreshReservationProjectionReportsUnknownWithNoPriorArtifact(t *testing.T) {
	store := reservationprojection.NewStore(t.TempDir())
	result := refreshReservationProjectionWith(store, repoID, func() ([]reservationv1.Reservation, error) {
		return nil, errors.New("svn info: connection refused")
	})
	if !result.Unknown || result.Stale || len(result.Reservations) != 0 || result.Detail == "" {
		t.Fatalf("failed refresh with no prior artifact must reply unknown: %+v", result)
	}
}

func TestRefreshReservationProjectionResultRoundTripsThroughProtocolParser(t *testing.T) {
	store := reservationprojection.NewStore(t.TempDir())
	result := refreshReservationProjectionWith(store, repoID, func() ([]reservationv1.Reservation, error) {
		return []reservationv1.Reservation{{Path: "a.txt", Token: "tok", OwnerID: "acme"}}, nil
	})
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := reservationv1.ParseResult(raw)
	if err != nil {
		t.Fatalf("the worker's own result must parse under the protocol's own ParseResult: %v", err)
	}
	if parsed.RepoID != repoID || len(parsed.Reservations) != 1 {
		t.Fatalf("parsed=%+v", parsed)
	}
}
