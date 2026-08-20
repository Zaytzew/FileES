package servertool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"filees/internal/whaleworker"
	"filees/pkg/clientview"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
)

type clientviewWhaleAuthority struct {
	ServiceWC        string
	RepositoriesRoot string
}

func (a clientviewWhaleAuthority) ResolveWhale(_ context.Context, clientID, repoID string) (whaleworker.RepositoryAccess, error) {
	view, err := clientview.Load(filepath.Join(a.ServiceWC, "clients", clientID, "view.json"))
	if err != nil || view.ClientID != clientID {
		return whaleworker.RepositoryAccess{}, whaleworker.ErrAccessDenied
	}
	for _, repository := range view.Repositories {
		if repository.RepoID == repoID && repository.State == "active" {
			return whaleworker.RepositoryAccess{RepositoryPath: filepath.Join(a.RepositoriesRoot, repoID), Access: repository.Access}, nil
		}
	}
	return whaleworker.RepositoryAccess{}, whaleworker.ErrAccessDenied
}

func RunWhaleWorker(args []string, in io.Reader, out, stderr io.Writer) int {
	return runWhaleWorker("/etc/filees/server.json", args, in, out, stderr)
}

func runWhaleWorker(configPath string, args []string, in io.Reader, out, stderr io.Writer) int {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintln(stderr, "filees-worker whale-v1: client ID required")
		return ExitUsage
	}
	config, err := serverconfig.LoadFor(configPath, serverconfig.SecretActivation)
	if err != nil {
		report(stderr, "Whale worker config", err)
		return ExitConfig
	}
	r := config.Repositories
	if !filepath.IsAbs(r.Root) || !filepath.IsAbs(r.ResultsRoot) || !filepath.IsAbs(config.Activation.ServiceWorkingCopy) || !filepath.IsAbs(r.EffectiveSVNLookBinary()) || !filepath.IsAbs(r.EffectiveSVNMuccBinary()) {
		fmt.Fprintln(stderr, "Whale worker: repository configuration is incomplete")
		return ExitConfig
	}
	stateRoot := filepath.Join(r.ResultsRoot, "whale")
	if err := os.MkdirAll(stateRoot, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		report(stderr, "Whale worker state", err)
		return ExitConfig
	}
	service := whaleworker.PutService{
		Journal:      whaleworker.Journal{Root: filepath.Join(stateRoot, "generations")},
		Queue:        whaleworker.PathQueue{Root: filepath.Join(stateRoot, "queues")},
		Authority:    clientviewWhaleAuthority{ServiceWC: config.Activation.ServiceWorkingCopy, RepositoriesRoot: r.Root},
		Reservations: &repoworker.FileReservationLedger{Root: filepath.Join(stateRoot, "reservations"), Capacity: repoworker.FilesystemCapacity{Root: r.Root}},
		Publisher:    whaleworker.SVNPublisher{SVNMucc: r.EffectiveSVNMuccBinary(), SVNLook: r.EffectiveSVNLookBinary()},
	}
	dispatcher := whaleworker.Dispatcher{Service: service, ClientID: args[0]}
	if err := dispatcher.Serve(context.Background(), in, out); err != nil {
		report(stderr, "Whale worker", err)
		return ExitData
	}
	return ExitOK
}
