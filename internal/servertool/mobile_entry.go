package servertool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"filees/internal/mobileworker"
	"filees/internal/obsandbox"
	"filees/pkg/clientview"
	"filees/pkg/serverconfig"
)

const (
	mobileEntryPromises = writePromises + " proc exec"

	// mobileServerConfigDefaultPath is the same server.json every other
	// forced command reads (RunClientEntry, RunRepositoryWorker) - mobile
	// client_ids now go through the real activation/repoworker pipeline, so
	// there is no separate mobile-only configuration surface left.
	mobileServerConfigDefaultPath = "/etc/filees/server.json"
	mobileLedgerDefaultDir        = "/var/filees-mobile/ledger"

	// MobileOperationalCommand is what pkg/mobileclient/sshtransport.Do
	// sends for ongoing REFRESH_MANIFEST/UPLOAD_OBJECT traffic - the only
	// thing this forced command served before onboarding existed.
	MobileOperationalCommand = "filees-mobile-v1"
	// MobileProofCommand/MobileFinishCommand are the two onboarding-only
	// commands a freshly-staged mobile key uses once, right after
	// MobileOnboardCommand (internal/servertool/entry.go) staged it -
	// renderAccessLocked forces every mobile key's command= to this same
	// binary, so it must dispatch among all three itself, exactly like
	// filees-client-entry already dispatches desktop's svn/proof/control.
	MobileProofCommand  = "filees-mobile-proof/v1"
	MobileFinishCommand = "filees-mobile-finish/v1"
)

// clientviewMobileAuthority resolves mobile repository access from the same
// server-owned projection desktop clients already consume:
// <ServiceWorkingCopy>/clients/<clientID>/view.json, published by
// pkg/activation.Manager.Publish on activation and kept current by
// pkg/repoworker.ServicePublisher as repositories are granted. This retires
// the Etap 4b static-JSON stub (staticMobileAuthority) now that mobile
// client_ids actually go through pkg/activation instead of being
// hand-registered (concepts/FILEES_ANDROID_CLIENT_CONCEPT_V2.md SS3.1).
//
// Mirrors pkg/repoworker.ViewResolver: the view is knowledge, never local
// authority. repo.State is deliberately not consulted here - it matches how
// pkg/repoworker's rendered svnserve authz grants realm-wide access
// regardless of a repository's initializing/active state, so mobile stays
// consistent with the desktop access model rather than inventing a second,
// stricter one.
type clientviewMobileAuthority struct {
	ServiceWorkingCopy string
	RepositoriesRoot   string
}

func (a clientviewMobileAuthority) Resolve(_ context.Context, clientID, repoID string) (mobileworker.View, error) {
	view, err := a.loadView(clientID)
	if err != nil {
		return mobileworker.View{}, err
	}
	for _, repo := range view.Repositories {
		if repo.RepoID == repoID {
			return mobileworker.View{
				RepoPath:   filepath.Join(a.RepositoriesRoot, repo.RepoID),
				Generation: view.Generation,
				Access:     repo.Access,
			}, nil
		}
	}
	return mobileworker.View{}, mobileworker.ErrAccessDenied
}

func (a clientviewMobileAuthority) List(_ context.Context, clientID string) (mobileworker.Projection, error) {
	view, err := a.loadView(clientID)
	if err != nil {
		return mobileworker.Projection{}, err
	}
	repos := make([]mobileworker.RepositoryGrant, 0, len(view.Repositories))
	for _, repo := range view.Repositories {
		repos = append(repos, mobileworker.RepositoryGrant{
			RepoID:      repo.RepoID,
			DisplayName: repo.DisplayName,
			Access:      repo.Access,
			State:       repo.State,
			Purpose:     repo.Purpose,
		})
	}
	return mobileworker.Projection{
		RealmID:           view.RealmID,
		RealmAlias:        view.RealmAlias,
		ServerDisplayName: view.ServerDisplayName,
		Generation:        view.Generation,
		GeneratedAt:       view.GeneratedAt,
		Repositories:      repos,
	}, nil
}

func (a clientviewMobileAuthority) loadView(clientID string) (clientview.View, error) {
	view, err := clientview.Load(filepath.Join(a.ServiceWorkingCopy, "clients", clientID, "view.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return clientview.View{}, mobileworker.ErrAccessDenied
		}
		return clientview.View{}, err
	}
	if view.ClientID != clientID {
		return clientview.View{}, mobileworker.ErrAccessDenied
	}
	return view, nil
}

// RunMobileEntry is the forced command behind the filees-mobile-v1 SSH client
// class (FILEES_ANDROID_CLIENT_CONCEPT_V2.md §4.3). args are
// [operation_id, client_id], baked into the per-key command= option in the
// rendered authorized_keys entry (renderAccessLocked, mirrors
// RunClientEntry's binding) — a connecting device can never claim different
// ones itself. renderAccessLocked forces every mobile key's command= to this
// exact binary regardless of what the client asks for, so this is the one
// place a mobile key can ever land: it dispatches by SSH_ORIGINAL_COMMAND,
// exactly like filees-client-entry already dispatches desktop's
// svn/proof/control over one shared forced command. The onboarding-only
// proof/finish branches do nothing but validate and syscall.Exec into the
// already-audited filees-worker binary (mirrors execWorker in entry.go) -
// all activation-touching privilege stays there, never in this dispatcher.
// The operational branch (default, unchanged) serves exactly one framed
// mobile operation per invocation, then the process exits.
func RunMobileEntry(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	return runMobileEntry(mobileServerConfigDefaultPath, mobileLedgerDefaultDir, args, getenv, stdin, stdout, stderr, execMobileWorkerSubcommand)
}

func runMobileEntry(configPath, ledgerDir string, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer, execSubcommand func(subcommand, operationID, clientID string) error) int {
	if len(args) != 2 || args[0] == "" || args[1] == "" {
		fmt.Fprintln(stderr, "filees-mobile-v1: rejected command")
		return ExitUnavailable
	}
	operationID, clientID := args[0], args[1]
	originalCommand := getenv("SSH_ORIGINAL_COMMAND")
	if originalCommand == MobileProofCommand || originalCommand == MobileFinishCommand {
		subcommand := "mobile-proof"
		if originalCommand == MobileFinishCommand {
			subcommand = "mobile-finish"
		}
		if err := execSubcommand(subcommand, operationID, clientID); err != nil {
			report(stderr, "filees-mobile-v1 "+subcommand, err)
			return ExitSoftware
		}
		return ExitOK
	}
	if originalCommand != MobileOperationalCommand {
		fmt.Fprintln(stderr, "filees-mobile-v1: rejected command")
		return ExitUnavailable
	}

	// Resolved before the sandbox narrows: a locked-down forced command may
	// not keep enough of PATH unveiled to search it afterward, and the
	// resolved absolute path is exactly what gets unveiled below.
	svnPath, err := exec.LookPath("svn")
	if err != nil {
		report(stderr, "filees-mobile-v1 svn", err)
		return ExitConfig
	}
	svnlookPath, err := exec.LookPath("svnlook")
	if err != nil {
		report(stderr, "filees-mobile-v1 svnlook", err)
		return ExitConfig
	}

	if err := sandboxBegin(mobileEntryPromises); err != nil {
		report(stderr, "filees-mobile-v1 sandbox", err)
		return ExitSoftware
	}

	config, err := serverconfig.LoadFor(configPath, serverconfig.SecretActivation)
	if err != nil {
		report(stderr, "filees-mobile-v1 config", err)
		return ExitConfig
	}
	if !filepath.IsAbs(config.Repositories.Root) || !filepath.IsAbs(config.Activation.ServiceWorkingCopy) {
		fmt.Fprintln(stderr, "filees-mobile-v1: repository configuration is incomplete")
		return ExitConfig
	}
	authority := clientviewMobileAuthority{
		ServiceWorkingCopy: config.Activation.ServiceWorkingCopy,
		RepositoriesRoot:   config.Repositories.Root,
	}

	profile := obsandbox.Profile{
		Name:     "filees-mobile-v1",
		Promises: mobileEntryPromises,
		Paths:    mobileUnveilPaths(config.Repositories.Root, config.Activation.ServiceWorkingCopy, ledgerDir, svnPath, svnlookPath),
	}
	if err := obsandbox.Apply(profile); err != nil {
		report(stderr, "filees-mobile-v1 sandbox", err)
		return ExitSoftware
	}

	dispatcher := mobileworker.Dispatcher{
		Browser: mobileworker.Browser{
			Authority: authority,
			Reader:    mobileworker.SVNReader{SvnPath: svnPath, SvnlookPath: svnlookPath},
		},
		Appender: mobileworker.Appender{
			Authority: authority,
			Reader:    mobileworker.SVNReader{SvnPath: svnPath, SvnlookPath: svnlookPath},
			Committer: mobileworker.SVNAppender{SvnPath: svnPath, SvnlookPath: svnlookPath},
			Ledger:    mobileworker.Ledger{Dir: ledgerDir},
		},
		ClientID: clientID,
	}
	if err := dispatcher.Serve(context.Background(), stdin, stdout); err != nil {
		report(stderr, "filees-mobile-v1 dispatch", err)
		return ExitSoftware
	}
	return ExitOK
}

// execMobileWorkerSubcommand hands off to filees-worker for the two
// onboarding-only commands. Mirrors entry.go's execWorker exactly: this
// dispatcher never touches onboarding/activation state itself, it only
// pledges a fixed post-exec promise set and execs - the worker installs its
// own narrower unveil immediately after.
func execMobileWorkerSubcommand(subcommand, operationID, clientID string) error {
	if err := obsandbox.PledgeForExec(entryPromises, workerPromises+" unveil"); err != nil {
		return err
	}
	return syscall.Exec(workerPath, []string{"filees-worker", subcommand, operationID, clientID}, []string{})
}

func mobileUnveilPaths(repositoriesRoot, serviceWorkingCopy, ledgerDir, svnPath, svnlookPath string) []obsandbox.Path {
	return []obsandbox.Path{
		{Label: "svn", Name: svnPath, Perms: "rx"},
		{Label: "svnlook", Name: svnlookPath, Perms: "rx"},
		{Label: "ledger", Name: ledgerDir, Perms: "rwc"},
		// The whole repositories root is unveiled once, exactly like
		// filees-client-entry unveils it for _filees-data/svnserve - the
		// actual per-(client,repo) gate is clientviewMobileAuthority.Resolve
		// above, not the OS-level sandbox.
		{Label: "repository-root", Name: repositoriesRoot, Perms: "rwc"},
		// Read-only: this dispatcher only ever reads
		// clients/<clientID>/view.json, never writes to the service WC.
		{Label: "service-working-copy", Name: serviceWorkingCopy, Perms: "r"},
		{Label: "loader", Name: "/usr/libexec/ld.so", Perms: "rx"},
		{Label: "loader-hints", Name: "/var/run/ld.so.hints", Perms: "r"},
		{Label: "system-libraries", Name: "/usr/lib", Perms: "r"},
		{Label: "local-libraries", Name: "/usr/local/lib", Perms: "r"},
		{Label: "tmp", Name: os.TempDir(), Perms: "rwc"},
		// exec.Command opens /dev/null itself for any std stream left unset
		// (svn/svnlook invocations here never wire up Stdin), so the parent
		// process needs it unveiled even though it never execs into svn
		// directly the way filees-client-entry execs into svnserve.
		{Label: "devnull", Name: "/dev/null", Perms: "rw"},
	}
}
