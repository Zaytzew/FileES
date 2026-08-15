package reposupervisor

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
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

// The passport manager is constructed once, when the pipeline starts, so a
// repository whose owner turns locking on while its URL and access stay put
// must still be torn down and rebuilt. Without EditingPolicy in the restart
// comparison the projection would be accepted silently and the repository
// would keep running without passports until some unrelated change happened
// to restart it.
func TestSessionTimeoutChangeForcesRestartEvenWhenAccessAndURLAreUnchanged(t *testing.T) {
	var log []string
	supervisor, _ := New(fakeStarter{&log}, nil)
	key := Key{"office", "repo"}
	short := Desired{Key: key, Access: "rw", State: "active", URL: "one", SessionTimeout: 30 * time.Minute}
	long := Desired{Key: key, Access: "rw", State: "active", URL: "one", SessionTimeout: 90 * time.Minute}
	if err := supervisor.Apply(t.Context(), "office", 1, []Desired{short}); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Apply(t.Context(), "office", 2, []Desired{long}); err != nil {
		t.Fatal(err)
	}
	want := []string{"start:office/repo:rw", "stop:office/repo:rw", "start:office/repo:rw"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("timeout change did not restart folder work: log=%v want=%v", log, want)
	}
}

func TestEditingPolicyChangeForcesRestartEvenWhenAccessAndURLAreUnchanged(t *testing.T) {
	var log []string
	supervisor, _ := New(fakeStarter{&log}, nil)
	key := Key{"office", "repo"}
	free := Desired{Key: key, Access: "rw", State: "active", URL: "one"}
	locked := Desired{Key: key, Access: "rw", State: "active", URL: "one", EditingPolicy: "lock_required"}

	if err := supervisor.Apply(t.Context(), "office", 1, []Desired{free}); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Apply(t.Context(), "office", 2, []Desired{locked}); err != nil {
		t.Fatal(err)
	}
	want := []string{"start:office/repo:rw", "stop:office/repo:rw", "start:office/repo:rw"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("policy change did not restart the pipeline: log=%v want=%v", log, want)
	}

	// Turning it back off must restart too, otherwise a repository could
	// never leave lock_required.
	if err := supervisor.Apply(t.Context(), "office", 3, []Desired{free}); err != nil {
		t.Fatal(err)
	}
	if len(log) != 5 {
		t.Fatalf("policy removal did not restart the pipeline: log=%v", log)
	}

	// An identical projection must still be a no-op, or every poll would
	// bounce every repository.
	if err := supervisor.Apply(t.Context(), "office", 4, []Desired{free}); err != nil {
		t.Fatal(err)
	}
	if len(log) != 5 {
		t.Fatalf("unchanged projection caused a restart: log=%v", log)
	}
}
