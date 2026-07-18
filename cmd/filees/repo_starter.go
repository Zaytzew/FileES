package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/reposupervisor"
	"filees/pkg/talk"
	"filees/pkg/watcher"
)

type repoRuntime struct {
	config config.Repo
	state  *ipcserver.RepoState
}

type repoEventSource interface {
	Start(context.Context) <-chan watcher.Event
}
type repoCommitRunner interface {
	Run(context.Context, string, string, <-chan watcher.Event)
}

func runReadWritePipeline(ctx context.Context, repo config.Repo, state *ipcserver.RepoState, source repoEventSource, committer repoCommitRunner) error {
	if state == nil || source == nil || committer == nil {
		return errors.New("read-write pipeline is incomplete")
	}
	state.SetState(contract.StateActive)
	events := source.Start(ctx)
	committer.Run(ctx, repo.ID, repo.LocalPath, events)
	state.SetState(contract.StateStopping)
	return nil
}

type passportRunner interface {
	Run(context.Context)
	ReleaseAll(context.Context) error
}

type passportSession struct {
	cancel   context.CancelFunc
	done     chan struct{}
	manager  passportRunner
	once     sync.Once
	stopDone chan struct{}
	err      error
}

func startPassportSession(parent context.Context, manager passportRunner) (*passportSession, error) {
	if manager == nil {
		return nil, errors.New("passport manager is required")
	}
	ctx, cancel := context.WithCancel(parent)
	s := &passportSession{cancel: cancel, done: make(chan struct{}), manager: manager, stopDone: make(chan struct{})}
	go func() { defer close(s.done); manager.Run(ctx) }()
	return s, nil
}

func (s *passportSession) Stop(ctx context.Context) error {
	s.once.Do(func() { go s.stop() })
	select {
	case <-s.stopDone:
		return s.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *passportSession) stop() {
	defer close(s.stopDone)
	s.cancel()
	<-s.done
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.err = s.manager.ReleaseAll(ctx)
}

func recoverReadWriteWorkingCopy(ctx context.Context, svn client.Client, wc string, service *commit.Service, logger talk.Logger) {
	if _, err := os.Stat(filepath.Join(wc, ".svn")); err != nil {
		return
	}
	if out, err := svn.Cleanup(ctx, wc); err != nil {
		logger.Warnf("svn cleanup failed: %v %s", err, out)
	}
	status, err := svn.Status(ctx, wc, nil)
	if err != nil {
		logger.Warnf("svn status before update failed: %v — update deferred", err)
		return
	}
	if client.HasMissingPaths(status) {
		logger.Infof("svn update deferred: working copy contains local removals")
		return
	}
	out, err := svn.Update(ctx, wc)
	service.ReconcileUpdateConflicts(ctx, wc, out)
	if err != nil {
		logger.Warnf("svn update failed: %v %s", err, out)
	}
}

type svnFactory func(config.Repo) client.Client
type readWriteFactory func(context.Context, repoRuntime, client.Client, reposupervisor.Desired) (reposupervisor.Instance, error)

// daemonRepoStarter binds generic supervisor lifecycle to concrete daemon
// pipelines. daemonCtx, not the reconcile context, owns running instances.
type daemonRepoStarter struct {
	daemonCtx      context.Context
	repos          map[reposupervisor.Key]repoRuntime
	newSVN         svnFactory
	startReadWrite readWriteFactory
}

func (s *daemonRepoStarter) Start(startCtx context.Context, desired reposupervisor.Desired) (reposupervisor.Instance, error) {
	if err := startCtx.Err(); err != nil {
		return nil, err
	}
	runtime, ok := s.repos[desired.Key]
	if !ok {
		return nil, fmt.Errorf("repository attachment %s is not configured", desired.Key)
	}
	if s.daemonCtx == nil || s.newSVN == nil || runtime.state == nil {
		return nil, errors.New("repository starter is incomplete")
	}
	svn := s.newSVN(runtime.config)
	if desired.Access == contract.AccessReadWrite {
		if s.startReadWrite == nil {
			return nil, errors.New("read-write pipeline factory is not connected yet")
		}
		return s.startReadWrite(s.daemonCtx, runtime, svn, desired)
	}
	if desired.Access != contract.AccessReadOnly {
		return nil, errors.New("repository access is invalid")
	}
	return reposupervisor.StartManaged(s.daemonCtx, func(ctx context.Context) error {
		runReadOnlyRepo(ctx, runtime.config, runtime.state, svn, talk.With("readonly:"+desired.Key.String()))
		return nil
	}, nil)
}
