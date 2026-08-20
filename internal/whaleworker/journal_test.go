package whaleworker

import (
	"errors"
	"strings"
	"testing"
	"time"

	whale "filees/pkg/whale/v1"
	"github.com/google/uuid"
)

func identity() whale.Identity {
	return whale.Identity{LogicalRepoID: uuid.NewString(), LogicalPath: "media/a.bin", GenerationID: uuid.NewString(), ExpectedSize: 3, SHA256: strings.Repeat("1", 64)}
}

func TestJournalCreateIsIdempotentButGenerationTupleIsImmutable(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	j := Journal{Root: t.TempDir(), Now: func() time.Time { return now }}
	id := identity()
	first, err := j.Create(id, whale.DirectionPut)
	if err != nil {
		t.Fatal(err)
	}
	again, err := j.Create(id, whale.DirectionPut)
	if err != nil || again != first {
		t.Fatalf("idempotent create=%+v err=%v", again, err)
	}
	changed := id
	changed.ExpectedSize++
	if _, err := j.Create(changed, whale.DirectionPut); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("changed tuple err=%v", err)
	}
	if _, err := j.Create(id, whale.DirectionGet); !errors.Is(err, ErrGenerationConflict) {
		t.Fatalf("changed direction err=%v", err)
	}
}

func TestJournalPersistsExactOffsetAndPUTRecoveryPath(t *testing.T) {
	clock := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	j := Journal{Root: t.TempDir(), Now: func() time.Time { return clock }}
	record, err := j.Create(identity(), whale.DirectionPut)
	if err != nil {
		t.Fatal(err)
	}
	record.BytesHave = 2
	clock = clock.Add(time.Minute)
	if err := j.Save(record); err != nil {
		t.Fatal(err)
	}
	loaded, err := j.Load(record.Identity.GenerationID)
	if err != nil || loaded.BytesHave != 2 || loaded.UpdatedAt != clock.Format(time.RFC3339Nano) {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	loaded.BytesHave = 1
	if err := j.Save(*loaded); err == nil {
		t.Fatal("offset regression accepted")
	}
	loaded.BytesHave = 3
	loaded.State = whale.StateValidating
	if err := j.Save(*loaded); err != nil {
		t.Fatal(err)
	}
	loaded, _ = j.Load(record.Identity.GenerationID)
	loaded.State = whale.StateCommitting
	if err := j.Save(*loaded); err != nil {
		t.Fatal(err)
	}
	loaded, _ = j.Load(record.Identity.GenerationID)
	loaded.State, loaded.Revision, loaded.CommitBaseKnown = whale.StatePublished, 184, true
	if err := j.Save(*loaded); err != nil {
		t.Fatal(err)
	}
	loaded, _ = j.Load(record.Identity.GenerationID)
	loaded.State = whale.StateReceiving
	if err := j.Save(*loaded); err == nil {
		t.Fatal("terminal state regression accepted")
	}
}

func TestJournalRequiresCompletePayloadBeforeValidation(t *testing.T) {
	j := Journal{Root: t.TempDir()}
	record, err := j.Create(identity(), whale.DirectionPut)
	if err != nil {
		t.Fatal(err)
	}
	record.BytesHave = 2
	record.State = whale.StateValidating
	if err := j.Save(record); err == nil {
		t.Fatal("partial PUT entered validation")
	}
}

func TestJournalGETRequiresConfirmationAndCompleteMaterialization(t *testing.T) {
	j := Journal{Root: t.TempDir()}
	record, err := j.Create(identity(), whale.DirectionGet)
	if err != nil {
		t.Fatal(err)
	}
	record.State = whale.StateMaterializing
	if err := j.Save(record); err != nil {
		t.Fatal(err)
	}
	record, _ = value(j.Load(record.Identity.GenerationID))
	record.BytesHave = 2
	record.State = whale.StateVerifying
	if err := j.Save(record); err == nil {
		t.Fatal("partial GET entered verification")
	}
}

func value(record *Record, err error) (Record, error) {
	if record == nil {
		return Record{}, err
	}
	return *record, err
}
