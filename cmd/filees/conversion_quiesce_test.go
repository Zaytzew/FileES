package main

import (
	"context"
	"errors"
	"filees/pkg/clientview"
	"filees/pkg/config"
	"filees/pkg/reposupervisor"
	"testing"
)

type conversionQuiesceStarter struct {
	stops   map[reposupervisor.Key]int
	stopErr error
}

func (s *conversionQuiesceStarter) Start(_ context.Context, d reposupervisor.Desired) (reposupervisor.Instance, error) {
	return conversionQuiesceChild{s, d.Key}, nil
}

type conversionQuiesceChild struct {
	s   *conversionQuiesceStarter
	key reposupervisor.Key
}

func (c conversionQuiesceChild) Stop(context.Context) error { c.s.stops[c.key]++; return c.s.stopErr }

func conversionQuiesceFixture(t *testing.T) (daemonConversionFixture, provisionedAttachment, clientview.View) {
	f := newDaemonConversionFixture(t)
	r := f.queue(t)
	r, e := f.p.local.StartConversion(r)
	if e != nil {
		t.Fatal(e)
	}
	request := provisionedAttachment{Repo: config.Repo{ServerID: r.ServerID, ID: r.RepoID}, Quiesce: true, Conversion: &conversionQuiesceIntent{r.OperationID, r.ConversionID, r.LocalPath, r.RepoURL}}
	view := clientview.View{Generation: 5, Repositories: []clientview.Repository{{RepoID: r.RepoID, URL: r.RepoURL, Access: r.Access, State: "active"}}}
	return f, request, view
}

func TestConversionQuiesceResumeStopsSurvivingChildAndIsIdempotent(t *testing.T) {
	f, request, view := conversionQuiesceFixture(t)
	key := reposupervisor.Key{ServerID: request.Repo.ServerID, RepoID: request.Repo.ID}
	other := reposupervisor.Key{ServerID: key.ServerID, RepoID: "other"}
	view.Repositories = append(view.Repositories, clientview.Repository{RepoID: other.RepoID, URL: "svn+ssh://fixture/other", Access: "rw", State: "active"})
	runtimes := map[reposupervisor.Key]repoRuntime{other: {config: config.Repo{ID: other.RepoID, ServerID: other.ServerID, RepoURL: "svn+ssh://fixture/other", Access: "rw"}}}
	starter := &conversionQuiesceStarter{stops: map[reposupervisor.Key]int{}}
	supervisor, _ := reposupervisor.New(starter, nil)
	initial := append(attachedProjection(key.ServerID, view, runtimes), reposupervisor.Desired{Key: key, Access: "rw", State: "active", URL: request.Conversion.URL})
	if err := supervisor.Apply(t.Context(), key.ServerID, view.Generation, initial); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := quiesceProjectedRuntime(t.Context(), supervisor, request, view, runtimes, f.p.local); err != nil {
			t.Fatal(err)
		}
	}
	if starter.stops[key] != 1 || starter.stops[other] != 0 || len(runtimes) != 1 {
		t.Fatal("incorrect stop isolation", starter.stops, runtimes)
	}
}

func TestConversionQuiesceResumeRefusesStaleOrUnownedRequests(t *testing.T) {
	for _, kind := range []string{"ordinary", "stale-intent", "stale-attachment", "wrong-source", "wrong-url", "disabled", "no-access", "removed", "duplicate", "not-relocating", "remote-deleted", "known-error", "missing-store"} {
		t.Run(kind, func(t *testing.T) {
			f, request, view := conversionQuiesceFixture(t)
			store := f.p.local
			switch kind {
			case "ordinary":
				request.Conversion = nil
			case "stale-intent":
				request.Conversion.ConversionID = "other"
			case "stale-attachment":
				request.Conversion.AttachmentID = "other"
			case "wrong-source":
				request.Conversion.SourcePath += "-other"
			case "wrong-url":
				request.Conversion.URL += "-other"
			case "disabled":
				view.Repositories[0].State = "disabled"
			case "no-access":
				view.Repositories[0].Access = ""
			case "removed":
				view.Repositories = nil
			case "duplicate":
				view.Repositories = append(view.Repositories, view.Repositories[0])
			case "not-relocating":
				r, _ := store.Get(request.Conversion.AttachmentID)
				j, _ := f.p.conversionJournal(r, false)
				if _, e := store.DeferConversion(r, j.Get()); e != nil {
					t.Fatal(e)
				}
			case "missing-store":
				store = nil
			case "remote-deleted":
				if err := store.ObserveRemoteDeletion(request.Repo.ServerID, request.Repo.ID); err != nil {
					t.Fatal(err)
				}
			case "known-error":
				if _, err := store.RecordConversionError(request.Conversion.AttachmentID, request.Conversion.ConversionID, errors.New("known blocked conversion")); err != nil {
					t.Fatal(err)
				}
			}
			if conversionQuiesceResumeAllowed(request, view, store) {
				t.Fatal("invalid request admitted")
			}
		})
	}
}

func TestConversionQuiesceResumeStillPropagatesStopAndGenerationErrors(t *testing.T) {
	for _, kind := range []string{"stop-error", "stale-generation"} {
		t.Run(kind, func(t *testing.T) {
			f, request, view := conversionQuiesceFixture(t)
			key := reposupervisor.Key{ServerID: request.Repo.ServerID, RepoID: request.Repo.ID}
			starter := &conversionQuiesceStarter{stops: map[reposupervisor.Key]int{}}
			supervisor, _ := reposupervisor.New(starter, nil)
			if err := supervisor.Apply(t.Context(), key.ServerID, view.Generation, []reposupervisor.Desired{{Key: key, State: "active", Access: "rw", URL: request.Conversion.URL}}); err != nil {
				t.Fatal(err)
			}
			if kind == "stop-error" {
				starter.stopErr = errors.New("fixture child remains alive")
			} else {
				view.Generation--
			}
			if err := quiesceProjectedRuntime(t.Context(), supervisor, request, view, map[reposupervisor.Key]repoRuntime{}, f.p.local); err == nil {
				t.Fatal("unsafe stop reported success")
			}
		})
	}
}
