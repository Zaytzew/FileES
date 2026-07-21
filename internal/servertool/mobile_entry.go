package servertool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"filees/internal/mobileworker"
	"filees/internal/obsandbox"
)

const (
	mobileEntryPromises = writePromises + " proc exec"
	// Deliberately outside /etc/filees and /var/filees: those trees are
	// owned and locked to _filees-state (0700), unreachable by the
	// _filees-mobile login this forced command runs as. Etap 4b's stub gets
	// its own small, separately-owned tree instead of loosening that
	// boundary.
	mobileRepoConfigDefaultPath = "/etc/filees-mobile/mobile-repos.json"
	mobileLedgerDefaultDir      = "/var/filees-mobile/ledger"
)

// MobileRepoGrant is one static client_id -> repo mapping.
//
// This is the Etap 4b stub authority: it exists only to prove that sshd, the
// filees-mobile-v1 forced command and mobileworker.Dispatcher work together
// over a real network connection. It is deliberately NOT the production
// grant model — pkg/repoworker's realm/grant system (built separately,
// desktop/server side) is meant to replace it outright. Do not grow this
// into a general authorization system; once real grants exist, swap the
// Authority passed to the Dispatcher below and delete this stub.
type MobileRepoGrant struct {
	ClientID   string `json:"client_id"`
	RepoID     string `json:"repo_id"`
	RepoPath   string `json:"repo_path"`
	Access     string `json:"access"` // "rw" or "r"
	Generation int64  `json:"generation"`
}

type staticMobileAuthority []MobileRepoGrant

func (grants staticMobileAuthority) Resolve(_ context.Context, clientID, repoID string) (mobileworker.View, error) {
	for _, g := range grants {
		if g.ClientID == clientID && g.RepoID == repoID {
			if g.Access != "rw" && g.Access != "r" {
				return mobileworker.View{}, fmt.Errorf("mobile: grant for %s/%s has invalid access %q", clientID, repoID, g.Access)
			}
			return mobileworker.View{RepoPath: g.RepoPath, Generation: g.Generation, Access: g.Access}, nil
		}
	}
	return mobileworker.View{}, errors.New("mobile: no grant for this client/repo")
}

// RunMobileEntry is the forced command behind the filees-mobile-v1 SSH client
// class (FILEES_ANDROID_CLIENT_CONCEPT_V2.md §4.3). args[0] is the client_id
// baked into the per-key command= option in the rendered authorized_keys
// entry (mirrors RunClientEntry's binding) — a connecting device can never
// claim a different one itself. Exactly one framed mobile operation is
// served per invocation, then the process exits (one op per SSH session).
func RunMobileEntry(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runMobileEntry(mobileRepoConfigDefaultPath, mobileLedgerDefaultDir, args, stdin, stdout, stderr)
}

func runMobileEntry(configPath, ledgerDir string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintln(stderr, "filees-mobile-v1: rejected command")
		return ExitUnavailable
	}
	clientID := args[0]

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

	raw, err := os.ReadFile(configPath)
	if err != nil {
		report(stderr, "filees-mobile-v1 config", err)
		return ExitConfig
	}
	var grants staticMobileAuthority
	if err := json.Unmarshal(raw, &grants); err != nil {
		report(stderr, "filees-mobile-v1 config", err)
		return ExitConfig
	}

	profile := obsandbox.Profile{Name: "filees-mobile-v1", Promises: mobileEntryPromises, Paths: mobileUnveilPaths(grants, ledgerDir, svnPath, svnlookPath)}
	if err := obsandbox.Apply(profile); err != nil {
		report(stderr, "filees-mobile-v1 sandbox", err)
		return ExitSoftware
	}

	dispatcher := mobileworker.Dispatcher{
		Browser: mobileworker.Browser{
			Authority: grants,
			Reader:    mobileworker.SVNReader{SvnPath: svnPath, SvnlookPath: svnlookPath},
		},
		Appender: mobileworker.Appender{
			Authority: grants,
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

func mobileUnveilPaths(grants staticMobileAuthority, ledgerDir, svnPath, svnlookPath string) []obsandbox.Path {
	paths := []obsandbox.Path{
		{Label: "svn", Name: svnPath, Perms: "rx"},
		{Label: "svnlook", Name: svnlookPath, Perms: "rx"},
		{Label: "ledger", Name: ledgerDir, Perms: "rwc"},
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
	seen := make(map[string]bool, len(grants))
	for _, g := range grants {
		if seen[g.RepoPath] {
			continue
		}
		seen[g.RepoPath] = true
		perms := "r"
		if g.Access == "rw" {
			perms = "rwc"
		}
		paths = append(paths, obsandbox.Path{Label: "repo:" + g.RepoID, Name: g.RepoPath, Perms: perms})
	}
	return paths
}
