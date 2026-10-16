package reservationprojection

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"testing"

	reservationv1 "filees/pkg/reservation/v1"
)

func TestUnchangedRecoversInvalidArtifactMetadata(t *testing.T) {
	for _, fields := range []string{
		`"generation":0,"as_of":"2026-10-16T00:00:00Z"`,
		`"generation":-1,"as_of":"2026-10-16T00:00:00Z"`,
		`"generation":9223372036854775807,"as_of":"2026-10-16T00:00:00Z"`,
		`"generation":1`,
	} {
		t.Run(fields, func(t *testing.T) {
			s := NewStore(t.TempDir())
			path, _, _ := s.pathsFor("fixture")
			raw := fmt.Sprintf(`{"schema":%q,"repo_id":"fixture",%s,"reservations":null}`, Schema, fields)
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := s.Load("fixture"); ok || err == nil {
				t.Fatal("accepted incomplete cache")
			}
			fresh, err := s.RefreshIfChanged("fixture", func(_ Artifact, ok bool) ([]reservationv1.Reservation, error) {
				if ok {
					t.Fatal("corrupt cache passed as valid to source")
				}
				return nil, nil
			})
			if err != nil || fresh.Generation != 1 || fresh.AsOf.IsZero() {
				t.Fatalf("recovery: %+v %v", fresh, err)
			}
			if _, ok, err := s.Load("fixture"); !ok || err != nil {
				t.Fatalf("recovered cache: %v", err)
			}
		})
	}
}

func TestUnchangedEmptyListKeepsIdenticalWireShape(t *testing.T) {
	s := NewStore(t.TempDir())
	query := func(Artifact, bool) ([]reservationv1.Reservation, error) { return []reservationv1.Reservation{}, nil }
	first, err := s.RefreshIfChanged("empty", query)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RefreshIfChanged("empty", query)
	if err != nil {
		t.Fatal(err)
	}
	if first.Reservations != nil || second.Reservations != nil || first.Generation != second.Generation {
		t.Fatal("nil/empty changed delta token")
	}
}

func TestUnchangedStillReadsSourceButDoesNotRewriteOrPersistFreshness(t *testing.T) {
	s := NewStore(t.TempDir())
	const id = "unchanged-fixture"
	locks := []reservationv1.Reservation{{Path: "b", Token: "b"}, {Path: "a", Token: "a"}}
	queries := 0
	query := func(Artifact, bool) ([]reservationv1.Reservation, error) { queries++; return locks, nil }
	first, err := s.RefreshIfChanged(id, query)
	if err != nil {
		t.Fatal(err)
	}
	path, _, _ := s.pathsFor(id)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	locks[0], locks[1] = locks[1], locks[0]
	second, err := NewStore(s.dir).RefreshIfChanged(id, query)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if queries != 2 || !bytes.Equal(before, after) || second.Generation != first.Generation || second.AsOf.Before(first.AsOf) {
		t.Fatal("unchanged write/freshness contract violated")
	}
	loaded, _, err := s.Load(id)
	if err != nil || !loaded.AsOf.Equal(first.AsOf) {
		t.Fatal("freshness persisted without content change")
	}
	if _, err = s.RefreshIfChanged(id, func(Artifact, bool) ([]reservationv1.Reservation, error) { return nil, errors.New("source failed") }); err == nil {
		t.Fatal("failure treated as unchanged ACK")
	}
	locks[0].Token = "replacement-without-commit"
	third, err := s.RefreshIfChanged(id, query)
	if err != nil || third.Generation != first.Generation+1 {
		t.Fatalf("replacement missed: %v", err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshIfChanged(id, query); err != nil {
		t.Fatalf("corrupt cache recovery: %v", err)
	}
}
