package servertool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"filees/internal/obsandbox"
	"filees/internal/whaleworker"
	"filees/pkg/clientview"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"

	"github.com/google/uuid"
)

type clientviewWhaleAuthority struct {
	ServiceWC        string
	RepositoriesRoot string
}

// whaleStorageCapacity admits a reservation only when both the operational
// cache filesystem and the FSFS repository filesystem can carry it. They are
// often one volume, but a separate WhaleRoot must not make either check lie.
type whaleStorageCapacity struct{ Roots []string }

func (c whaleStorageCapacity) Check(ctx context.Context, contentBytes int64) (int64, int64, error) {
	var available, required int64
	for index, root := range c.Roots {
		currentAvailable, currentRequired, err := (repoworker.FilesystemCapacity{Root: root}).Check(ctx, contentBytes)
		if err != nil {
			return 0, 0, err
		}
		if index == 0 || currentAvailable < available {
			available = currentAvailable
		}
		if currentRequired > required {
			required = currentRequired
		}
	}
	if len(c.Roots) == 0 {
		return 0, 0, errors.New("Whale capacity roots are missing")
	}
	return available, required, nil
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
	path, rest, err := configPath(args)
	if err != nil {
		report(stderr, "Whale worker arguments", err)
		return ExitUsage
	}
	return runWhaleWorker(path, rest, in, out, stderr)
}

func runWhaleWorker(configPath string, args []string, in io.Reader, out, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "filees-worker whale-v1: client ID required")
		return ExitUsage
	}
	clientUUID, err := uuid.Parse(args[0])
	if err != nil || clientUUID.String() != args[0] {
		fmt.Fprintln(stderr, "filees-worker whale-v1: client ID must be a canonical UUID")
		return ExitUsage
	}
	if err := sandboxBegin(whaleExecPromises); err != nil {
		report(stderr, "Whale worker bootstrap sandbox", err)
		return ExitSoftware
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
	stateRoot := r.EffectiveWhaleRoot()
	if err := os.MkdirAll(stateRoot, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		report(stderr, "Whale worker state", err)
		return ExitConfig
	}
	if err := sandboxApplyForExec(whaleWorkerProfile(config, configPath, stateRoot), svnExecPromises); err != nil {
		report(stderr, "Whale worker sandbox", err)
		return ExitSoftware
	}
	reservations := &repoworker.FileReservationLedger{Root: filepath.Join(stateRoot, "reservations"), Capacity: whaleStorageCapacity{Roots: []string{r.Root, stateRoot}}}
	authority := clientviewWhaleAuthority{ServiceWC: config.Activation.ServiceWorkingCopy, RepositoriesRoot: r.Root}
	service := whaleworker.PutService{
		Journal:      whaleworker.Journal{Root: filepath.Join(stateRoot, "generations")},
		Queue:        whaleworker.PathQueue{Root: filepath.Join(stateRoot, "queues")},
		Authority:    authority,
		Reservations: reservations,
		Publisher:    whaleworker.SVNPublisher{SVNMucc: r.EffectiveSVNMuccBinary(), SVNLook: r.EffectiveSVNLookBinary(), SVNAdmin: r.SVNAdminBinary},
	}
	get := whaleworker.GetService{Root: filepath.Join(stateRoot, "get-cache"), Authority: authority, Reservations: reservations, Source: whaleworker.SVNGetSource{SVNLook: r.EffectiveSVNLookBinary()}}
	dispatcher := whaleworker.Dispatcher{Service: service, Get: get, ClientID: args[0]}
	if err := dispatcher.Serve(context.Background(), in, out); err != nil {
		report(stderr, "Whale worker", err)
		return ExitData
	}
	return ExitOK
}

func whaleWorkerProfile(config serverconfig.Config, configPath, stateRoot string) obsandbox.Profile {
	r := config.Repositories
	return obsandbox.Profile{Name: "filees-worker/whale-v1", Promises: whaleWorkerPromises, Paths: []obsandbox.Path{
		{Label: "server-config", Name: configPath, Perms: "r"},
		{Label: "service-working-copy-parent", Name: filepath.Dir(config.Activation.ServiceWorkingCopy), Perms: "r"},
		{Label: "service-working-copy", Name: config.Activation.ServiceWorkingCopy, Perms: "r"},
		{Label: "repository-root-parent", Name: filepath.Dir(r.Root), Perms: "r"},
		{Label: "repository-root", Name: r.Root, Perms: "rwc"},
		{Label: "whale-operational", Name: stateRoot, Perms: "rwc"},
		{Label: "svnmucc", Name: r.EffectiveSVNMuccBinary(), Perms: "rx"},
		{Label: "svnlook", Name: r.EffectiveSVNLookBinary(), Perms: "rx"},
		{Label: "svnadmin", Name: r.SVNAdminBinary, Perms: "rx"},
		{Label: "null-device", Name: "/dev/null", Perms: "rw"},
		{Label: "random", Name: "/dev/urandom", Perms: "r"},
		{Label: "loader", Name: "/usr/libexec/ld.so", Perms: "rx"},
		{Label: "loader-hints", Name: "/var/run/ld.so.hints", Perms: "r"},
		{Label: "system-libraries", Name: "/usr/lib", Perms: "r"},
		{Label: "local-libraries", Name: "/usr/local/lib", Perms: "r"},
		{Label: "svn-system-config", Name: "/etc/subversion", Perms: "r"},
	}}
}
