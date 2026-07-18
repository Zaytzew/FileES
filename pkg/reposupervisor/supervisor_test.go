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
