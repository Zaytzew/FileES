package reposupervisor

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

type fakeStarter struct{ log *[]string }
type fakeInstance struct {
	key    Key
	access string
	log    *[]string
}

func (f fakeStarter) Start(_ context.Context, d Desired) (Instance, error) {
	*f.log = append(*f.log, "start:"+d.Key.String()+":"+d.Access)
	return fakeInstance{d.Key, d.Access, f.log}, nil
}
func (f fakeInstance) Stop(context.Context) error {
	*f.log = append(*f.log, "stop:"+f.key.String()+":"+f.access)
	return nil
}

func TestAccessTransitionStopsBeforeStartingReplacement(t *testing.T) {
	var log []string
	supervisor, _ := New(fakeStarter{&log}, nil)
	key := Key{"office", "repo"}
	if err := supervisor.Apply(t.Context(), "office", 1, []Desired{{Key: key, Access: "rw", State: "active", URL: "one"}}); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Apply(t.Context(), "office", 2, []Desired{{Key: key, Access: "r", State: "active", URL: "one"}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"start:office/repo:rw", "stop:office/repo:rw", "start:office/repo:r"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("log=%v want=%v", log, want)
	}
}

func TestTransitionBarrierRunsAfterStopBeforeReadOnlyStart(t *testing.T) {
	var log []string
	supervisor, _ := New(fakeStarter{&log}, nil)
	key := Key{"office", "repo"}
	if err := supervisor.Apply(t.Context(), "office", 1, []Desired{{Key: key, Access: "rw", State: "active", URL: "one"}}); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.ApplyWithTransition(t.Context(), "office", 2, []Desired{{Key: key, Access: "r", State: "active", URL: "one"}}, func(d Desired) {
		log = append(log, "publish:"+d.Key.String()+":"+d.Access)
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"start:office/repo:rw", "stop:office/repo:rw", "publish:office/repo:r", "start:office/repo:r"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("log=%v want=%v", log, want)
	}
}

func TestDisableRemovalAndOtherServerIsolation(t *testing.T) {
	var log []string
	supervisor, _ := New(fakeStarter{&log}, nil)
	for _, server := range []string{"a", "b"} {
		if err := supervisor.Apply(t.Context(), server, 1, []Desired{{Key: Key{server, "repo"}, Access: "rw", State: "active", URL: server}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := supervisor.Apply(t.Context(), "a", 2, []Desired{{Key: Key{"a", "repo"}, Access: "rw", State: "disabled", URL: "a"}}); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(log); got != "[start:a/repo:rw start:b/repo:rw stop:a/repo:rw]" {
		t.Fatalf("log=%s", got)
	}
	if err := supervisor.Apply(t.Context(), "a", 2, nil); err == nil {
		t.Fatal("same generation accepted")
	}
}

func TestInitializingRepositoryNeverStarts(t *testing.T) {
	var log []string
	supervisor, _ := New(fakeStarter{&log}, nil)
	key := Key{"office", "repo"}
	if err := supervisor.Apply(context.Background(), "office", 1, []Desired{{Key: key, Access: "rw", State: "initializing", URL: "one"}}); err != nil {
		t.Fatal(err)
	}
	if len(log) != 0 {
		t.Fatalf("initializing repository started: %v", log)
	}
}

func TestLocalAttachmentReappliesOnlyCurrentGeneration(t *testing.T) {
	var log []string
	supervisor, _ := New(fakeStarter{&log}, nil)
	if err := supervisor.Apply(context.Background(), "office", 4, nil); err != nil {
		t.Fatal(err)
	}
	item := Desired{Key: Key{"office", "repo"}, Access: "rw", State: "active", URL: "one"}
	if err := supervisor.ApplyLocalAttachment(context.Background(), "office", 3, []Desired{item}, nil); err == nil {
		t.Fatal("stale local attachment generation accepted")
	}
	if err := supervisor.ApplyLocalAttachment(context.Background(), "office", 4, []Desired{item}, nil); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(log); got != "[start:office/repo:rw]" {
		t.Fatalf("log=%s", got)
	}
}

func TestDetachLocalStopsWithoutAuthoritativeProjection(t *testing.T) {
	var log []string
	supervisor, _ := New(fakeStarter{&log}, nil)
	key := Key{"offline", "repo"}
	if err := supervisor.Apply(context.Background(), "offline", 1, []Desired{{Key: key, Access: "rw", State: "active", URL: "one"}}); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.DetachLocal(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.DetachLocal(context.Background(), key); err != nil {
		t.Fatalf("idempotent detach: %v", err)
	}
	if got := fmt.Sprint(log); got != "[start:offline/repo:rw stop:offline/repo:rw]" {
		t.Fatalf("log=%s", got)
	}
}
