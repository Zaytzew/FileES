package storagewatch

import (
	"context"
	"errors"
	"filees/pkg/alertchannel"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHysteresisTransitionsAndInodes(t *testing.T) {
	p := Policy{15, 5, 2, 100, 25, 10}
	cases := []struct {
		avail     uint64
		old, want string
	}{{150, "", ""}, {149, "", "warning"}, {160, "warning", "warning"}, {170, "warning", ""}, {49, "warning", "error"}, {69, "error", "error"}, {70, "error", "warning"}, {999, "error", ""}}
	for _, c := range cases {
		if got := p.level(Sample{Total: 1000, Available: c.avail}, c.old); got != c.want {
			t.Errorf("%+v got %s", c, got)
		}
	}
	if got := p.level(Sample{Total: 1000, Available: 900, Files: 1000, FreeFiles: 49}, ""); got != "error" {
		t.Fatal("inode exhaustion", got)
	}
}

type fakeReader struct {
	calls int
	fail  bool
}

func (f *fakeReader) Device(path string) (uint64, error) {
	if path == "bad" {
		return 0, errors.New("denied")
	}
	return 1, nil
}
func (f *fakeReader) Sample(string) (Sample, error) {
	f.calls++
	if f.fail {
		return Sample{}, errors.New("io failure")
	}
	return Sample{Total: 1000, Available: 800}, nil
}
func TestMeasureOnceAndNoPartialHealthyResult(t *testing.T) {
	r := &fakeReader{}
	v, e := Collect(context.Background(), []Path{{"a", "a"}, {"b", "b"}}, r)
	if e != nil || len(v) != 1 || r.calls != 1 || len(v[0].Labels) != 2 {
		t.Fatal(v, e, r.calls)
	}
	v, e = Collect(context.Background(), []Path{{"a", "a"}, {"b", "bad"}}, r)
	if e == nil || v != nil {
		t.Fatal("partial result", v, e)
	}
}
func TestLifecycleMailRetryEscalationRecoveryAndProjection(t *testing.T) {
	realm := uuid.NewString()
	p := Policy{15, 5, 2, 100, 25, 10}
	now := time.Now()
	file := filepath.Join(t.TempDir(), "state.json")
	s, e := Load(file, realm, "admin@example.test")
	if e != nil {
		t.Fatal(e)
	}
	sample := func(n uint64) []Volume {
		return []Volume{{Device: 1, Labels: []string{"data"}, Sample: Sample{Total: 1000, Available: n}}}
	}
	if e = s.Observe(sample(100), p, now); e != nil {
		t.Fatal(e)
	}
	if len(s.Pending) != 1 {
		t.Fatal(s)
	}
	if e = s.Save(file); e != nil {
		t.Fatal(e)
	}
	first := s.Pending[0].ID
	s, e = Load(file, realm, "admin@example.test")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Observe(sample(110), p, now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	if len(s.Pending) != 1 || s.Pending[0].ID != first || s.Snapshot.Incidents[0].Version != 1 {
		t.Fatal("duplicate", s)
	}
	s.Pending = nil // SMTP accepted; the persisted receipt suppresses repeats.
	if e = s.Save(file); e != nil {
		t.Fatal(e)
	}
	s, e = Load(file, realm, "admin@example.test")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Observe(sample(120), p, now.Add(2*time.Minute)); e != nil || len(s.Pending) != 0 {
		t.Fatal(e, s)
	}
	if e = s.Observe(sample(40), p, now.Add(3*time.Minute)); e != nil || len(s.Pending) != 1 || s.Pending[0].ID == first {
		t.Fatal("escalation", e, s)
	}
	remote, _, e := (alertchannel.Snapshot{}).Change(realm, "operator", "warning", "active", "unrelated", now)
	if e != nil {
		t.Fatal(e)
	}
	remote, did, e := s.Project(remote, now.Add(4*time.Minute))
	if e != nil || !did || len(remote.Incidents) != 2 {
		t.Fatal(remote, e)
	}
	_, did, e = s.Project(remote, now.Add(5*time.Minute))
	if e != nil || did {
		t.Fatal("duplicate publication", did, e)
	}
	if e = s.Observe(sample(800), p, now.Add(6*time.Minute)); e != nil || len(s.Pending) != 0 || s.Snapshot.Incidents[0].Status != "resolved" {
		t.Fatal("quiet recovery", e, s)
	}
	remote, did, e = s.Project(remote, now.Add(7*time.Minute))
	if e != nil || !did || remote.Incidents[0].Status != "active" {
		t.Fatal(remote, e)
	}
	if e = s.Observe(sample(100), p, now.Add(8*time.Minute)); e != nil || len(s.Pending) != 1 || s.Pending[0].ID == first {
		t.Fatal("recurrence", e, s)
	}
	if e = os.WriteFile(file, []byte("broken"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Load(file, realm, "admin@example.test"); e == nil {
		t.Fatal("corruption silently reset")
	}
}
func TestHealthyNoSnapshotAndPrepareFallback(t *testing.T) {
	root := t.TempDir()
	paths, e := Prepare([]Path{{"future", filepath.Join(root, "not-yet", "new")}})
	if e != nil || paths[0].Name != root {
		t.Fatal(paths, e)
	}
	s, did, e := Apply(alertchannel.Snapshot{}, uuid.NewString(), []Volume{{Device: 1, Sample: Sample{Total: 100 << 30, Available: 90 << 30}}}, DefaultPolicy(), time.Now())
	if e != nil || did || s.Schema != "" {
		t.Fatal(s, did, e)
	}
}
