package v1

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func validIdentity() Identity {
	return Identity{
		LogicalRepoID: uuid.NewString(), LogicalPath: "03_MEDIA/Łódź/video.mov",
		GenerationID: uuid.NewString(), ExpectedSize: 20 * 1024 * 1024 * 1024,
		SHA256: strings.Repeat("a", 64),
	}
}

func TestIdentityOwnsPhysicalMapping(t *testing.T) {
	i := validIdentity()
	if err := i.Validate(); err != nil {
		t.Fatal(err)
	}
	got, err := i.StoragePath()
	if err != nil || got != ".filees-whales/03_MEDIA/Łódź/video.mov" {
		t.Fatalf("StoragePath()=%q err=%v", got, err)
	}
}

func TestIdentityRejectsNamespaceAndNonCanonicalPaths(t *testing.T) {
	for _, value := range []string{"", "/absolute", "a/../b", "a//b", "a\\b", "a/", ".filees-whales/a", "a\x00b"} {
		i := validIdentity()
		i.LogicalPath = value
		if err := i.Validate(); err == nil {
			t.Errorf("path %q accepted", value)
		}
	}
}

func TestIdentityRejectsChangedGenerationTuple(t *testing.T) {
	for name, mutate := range map[string]func(*Identity){
		"noncanonical repo": func(i *Identity) { i.LogicalRepoID = strings.ToUpper(i.LogicalRepoID) },
		"oversize":          func(i *Identity) { i.ExpectedSize = MaxObjectBytes + 1 },
		"uppercase digest":  func(i *Identity) { i.SHA256 = strings.Repeat("A", 64) },
		"short digest":      func(i *Identity) { i.SHA256 = "abc" },
	} {
		t.Run(name, func(t *testing.T) {
			i := validIdentity()
			mutate(&i)
			if err := i.Validate(); err == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
}

func TestOffsetIsDurableByteCountAndNextByteIndex(t *testing.T) {
	const size = int64(1_203_036)
	for _, offset := range []int64{0, 1, 1_203_035, size} {
		if err := ValidateOffset(offset, size); err != nil {
			t.Fatalf("offset %d: %v", offset, err)
		}
	}
	for _, offset := range []int64{-1, size + 1} {
		if err := ValidateOffset(offset, size); err == nil {
			t.Fatalf("offset %d accepted", offset)
		}
	}
}

func TestStateMachinesDoNotCrossDirectionsOrLeaveTerminalState(t *testing.T) {
	if !CanTransition(DirectionPut, StateReceiving, StateValidating) || !CanTransition(DirectionPut, StateValidating, StateCommitting) || !CanTransition(DirectionPut, StateCommitting, StatePublished) {
		t.Fatal("valid PUT path rejected")
	}
	if CanTransition(DirectionPut, StateReceiving, StateMaterializing) || CanTransition(DirectionGet, StateMaterializing, StatePublished) {
		t.Fatal("direction state machines crossed")
	}
	if CanTransition(DirectionPut, StatePublished, StateReceiving) || CanTransition(DirectionGet, StateLocal, StateMaterializing) {
		t.Fatal("terminal state was left")
	}
}
