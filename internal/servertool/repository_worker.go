package servertool

import (
	"context"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"fmt"
	"io"
	"path/filepath"
)

func RunRepositoryWorker(args []string, in io.Reader, out, stderr io.Writer) int {
	return runRepositoryWorker("/etc/filees/server.json", args, in, out, stderr)
}
func runRepositoryWorker(configPath string, args []string, in io.Reader, out, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "filees-worker repository-control: client ID required")
		return ExitUsage
	}
	config, err := serverconfig.LoadFor(configPath, serverconfig.SecretActivation)
	if err != nil {
		report(stderr, "repository worker config", err)
		return ExitConfig
	}
	r := config.Repositories
	if !filepath.IsAbs(r.Root) || !filepath.IsAbs(r.ResultsRoot) || !filepath.IsAbs(r.DataAuthzFile) || !filepath.IsAbs(r.SVNAdminBinary) || r.URLPrefix == "" {
		fmt.Fprintln(stderr, "repository worker: repository configuration is incomplete")
		return ExitConfig
	}
	publisher := repoworker.ServicePublisher{ServiceWC: config.Activation.ServiceWorkingCopy, DataAuthzFile: r.DataAuthzFile, Runner: repoworker.SVNPublishRunner{SVN: config.Activation.SVNBinary, WorkingCopy: config.Activation.ServiceWorkingCopy}}
	effects := repoworker.ServerEffects{SVNAdmin: r.SVNAdminBinary, RepositoriesRoot: r.Root, DataAuthzFile: r.DataAuthzFile, Authority: publisher}
	backend := &repoworker.DurableBackend{Root: filepath.Join(r.ResultsRoot, "backend"), URLPrefix: r.URLPrefix, Effects: effects}
	store, err := repoworker.NewFileStore(filepath.Join(r.ResultsRoot, "results"))
	if err != nil {
		report(stderr, "repository worker store", err)
		return ExitConfig
	}
	worker := &repoworker.Worker{Backend: backend, Activator: effects, Store: store}
	dispatcher := repoworker.Dispatcher{Worker: worker, Resolver: repoworker.ViewResolver{ServiceWC: config.Activation.ServiceWorkingCopy}}
	if err := repoworker.WithFileLock(filepath.Join(r.ResultsRoot, ".worker.lock"), func() error { return dispatcher.Serve(context.Background(), args[0], in, out) }); err != nil {
		report(stderr, "repository worker", err)
		return ExitData
	}
	return ExitOK
}
