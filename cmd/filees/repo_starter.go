package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/config"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcserver"
	"filees/pkg/reposupervisor"
	"filees/pkg/talk"
)

type repoRuntime struct {
	config config.Repo
	state  *ipcserver.RepoState
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
