//go:build linux || openbsd

package servertool

import (
	"context"
	"encoding/json"
	"errors"
	"filees/internal/mobileworker"
	"filees/internal/obsandbox"
	"filees/pkg/serverconfig"
	"flag"
	"io"
	"os"
	"syscall"
	"time"
)

func runAdminMobileRecover(path string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mobile recover", flag.ContinueOnError)
	flags.SetOutput(stderr)
	id := flags.String("request-id", "", "operation UUID")
	dir := flags.String("ledger", mobileLedgerDefaultDir, "mobile ledger directory")
	allow := flags.Bool("allow-retry", false, "permit retry only after confirming maintenance quiescence")
	stopped := flags.Bool("confirm-workers-stopped", false, "operator confirms mobile sessions disabled and all prior worker/SVN children stopped")
	reason := flags.String("reason", "", "operator reason for the audited decision")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *id == "" || (*allow && (!*stopped || *reason == "")) {
		return adminUsage(stderr, flags, "--request-id UUID [--allow-retry --confirm-workers-stopped --reason TEXT]")
	}
	info, err := os.Lstat(*dir)
	if err != nil {
		report(stderr, "mobile ledger", err)
		return ExitConfig
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm()&0077 != 0 || int(stat.Uid) != os.Geteuid() {
		report(stderr, "mobile ledger", errors.New("run as the owner of the private ledger directory (normally doas -u _filees-state); no symlink allowed"))
		return ExitConfig
	}
	if err := sandboxBegin(mobileEntryPromises); err != nil {
		report(stderr, "mobile recovery sandbox", err)
		return ExitSoftware
	}
	c, err := serverconfig.LoadFor(path, 0)
	if err != nil {
		report(stderr, "mobile recovery config", err)
		return ExitConfig
	}
	svn, look := c.Activation.SVNBinary, c.Repositories.EffectiveSVNLookBinary()
	profile := obsandbox.Profile{Name: "mobile-recovery", Promises: mobileEntryPromises, Paths: mobileUnveilPaths(c.Repositories.Root, c.Activation.ServiceWorkingCopy, c.Repositories.ResultsRoot, *dir, svn, look)}
	// Recovery only reads the repository and projection. It needs no upload
	// workspace or GUI exchange directory; only the ledger remains writable.
	paths := profile.Paths[:0]
	for _, entry := range profile.Paths {
		if entry.Label == "gui-blobs" || entry.Label == "mobile-temp" {
			continue
		}
		if entry.Label == "repository-root" {
			entry.Perms = "r"
		}
		paths = append(paths, entry)
	}
	profile.Paths = paths
	if err := obsandbox.Apply(profile); err != nil {
		report(stderr, "mobile recovery sandbox", err)
		return ExitSoftware
	}
	a := mobileworker.Appender{Authority: clientviewMobileAuthority{ServiceWorkingCopy: c.Activation.ServiceWorkingCopy, RepositoriesRoot: c.Repositories.Root}, Reader: mobileworker.SVNReader{SvnPath: svn, SvnlookPath: look}, Ledger: mobileworker.Ledger{Dir: *dir}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := a.Reconcile(ctx, *id, *allow, *reason)
	if err != nil {
		report(stderr, "mobile recovery", err)
		return ExitSoftware
	}
	if err = json.NewEncoder(stdout).Encode(result); err != nil {
		report(stderr, "mobile recovery result", err)
		return ExitSoftware
	}
	return ExitOK
}
