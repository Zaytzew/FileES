// Command filees-client is a one-shot working-copy tool. It activates or
// joins a realm, checks out one repository, and later updates that copy and
// publishes local file changes. It is not a daemon and it does not know
// shelves, drawers or shares.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"filees/pkg/client"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var code int
	switch os.Args[1] {
	case "activate-begin":
		code = cmdActivateBegin(os.Args[2:])
	case "activate-finish":
		code = cmdActivateFinish(os.Args[2:])
	case "activate-resume":
		code = cmdActivateResume(os.Args[2:])
	case "checkout":
		code = cmdCheckout(os.Args[2:])
	case "sync":
		code = cmdSync(os.Args[2:])
	case "dump-load":
		code = cmdDumpLoad(os.Args[2:])
	case "whale-put", "whale-get-quote", "whale-get-confirm", "whale-status", "whale-retry", "whale-resume":
		code = cmdWhale(os.Args[1], os.Args[2:])
	case "help", "-h", "--help":
		usage()
		code = 0
	default:
		fmt.Fprintf(os.Stderr, "filees-client: unknown command %q\n", os.Args[1])
		usage()
		code = 2
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprintf(os.Stderr, `usage: filees-client <command> [flags]

commands:
  activate-begin    start onboarding (new realm, or a join ticket)
  activate-finish   read one OTP from stdin and finish activation
  activate-resume   resume an OTP-authorized activation
  checkout          create or resume one configured working copy
  sync              update WC and publish changes; -nwc publishes without WC
  dump-load         create a new repository from a SVN dump and new WC
  whale-put         send one large file through the Whale channel
  whale-get-quote   prepare a large-file download for confirmation
  whale-get-confirm confirm a quoted download by operation ID
  whale-status      inspect one durable Whale operation
  whale-retry       retry a failed or paused Whale operation
  whale-resume      resume pending Whale operations after a restart

checkout and sync read a config. Tool paths default to /usr/local/bin/svn
and /usr/bin/ssh. only=!wc-01 !wc-02 limits which realm copies may be named.
On OpenBSD the process and the svn/ssh it execs are pledged to those paths.
`)
}

type runFlags struct {
	config, wc, message                            string
	timeout                                        time.Duration
	noWorkingCopy, restoreSet, reverseReconcileSet bool
}

func addRunFlags(flags *flag.FlagSet, values *runFlags, checkout bool) {
	flags.StringVar(&values.config, "config", "", "absolute client config")
	flags.StringVar(&values.wc, "wc", "", "working-copy id from the config, not a path")
	flags.DurationVar(&values.timeout, "timeout", 30*time.Minute, "per-command SVN timeout")
	if !checkout {
		flags.StringVar(&values.message, "message", "FileES sync", "commit message when local files changed")
		flags.BoolVar(&values.noWorkingCopy, "nwc", false, "publish without a working copy")
		flags.BoolVar(&values.noWorkingCopy, "no-working-copy", false, "publish without a working copy")
		flags.BoolVar(&values.restoreSet, "restore-set", false, "restore source paths deleted in the repository (requires -nwc)")
		flags.BoolVar(&values.reverseReconcileSet, "reverse-reconcile-set", false, "make the local directory match the repository without a WC (requires -nwc)")
	}
}

func (values runFlags) open() (Config, workingCopySpec, error) {
	if !filepath.IsAbs(values.config) {
		return Config{}, workingCopySpec{}, errors.New("--config must be absolute")
	}
	cfg, err := loadConfig(values.config)
	if err != nil {
		return Config{}, workingCopySpec{}, err
	}
	spec, err := cfg.selectCopy(values.wc)
	if err != nil {
		return Config{}, workingCopySpec{}, err
	}
	return cfg, spec, nil
}

func (cfg Config) svnClient(timeout time.Duration) client.Client {
	return client.New(client.Options{
		SvnPath:         cfg.SVN,
		Timeout:         timeout,
		LogScope:        "svn:filees-client",
		SSHIdentityFile: cfg.Identity,
		SSHKnownHosts:   cfg.KnownHosts,
		SSHPort:         cfg.Port,
		SSHHostName:     cfg.Host,
	})
}

// pkg/client's SVN tunnel invokes the command name "ssh". Restrict process
// lookup to the configured binary's directory; unveil then permits only the
// exact configured file on OpenBSD.
func prepareSSHPath(cfg Config) error {
	base := filepath.Base(cfg.SSH)
	if base != "ssh" && base != "ssh.exe" {
		return fmt.Errorf("tools.ssh must name the ssh executable")
	}
	if err := os.Setenv("PATH", filepath.Dir(cfg.SSH)); err != nil {
		return err
	}
	if runtime.GOOS == "openbsd" {
		// SVN's user configuration must live inside the unveiled state root;
		// a home directory outside the profile is intentionally inaccessible.
		if err := os.Setenv("HOME", cfg.effectiveLogDir()); err != nil {
			return err
		}
	}
	return nil
}

func cmdCheckout(args []string) int {
	flags := flag.NewFlagSet("checkout", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var values runFlags
	addRunFlags(flags, &values, true)
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	cfg, spec, err := values.open()
	if err != nil {
		fmt.Fprintln(os.Stderr, "checkout:", err)
		return 2
	}
	// The configured destination may not exist yet. Create only that leaf
	// before unveil; SVN needs to write .svn inside it, while its parent stays
	// read-only in the exec profile.
	if err := os.Mkdir(spec.Path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		fmt.Fprintln(os.Stderr, "checkout:", err)
		return 1
	}
	if err := lockPublishSandbox(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "checkout:", err)
		return 1
	}
	if err := prepareSSHPath(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "checkout:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), values.timeout)
	defer cancel()
	out, err := cfg.svnClient(values.timeout).Checkout(ctx, spec.URL, spec.Path)
	if out != "" {
		fmt.Fprint(os.Stdout, out)
		if out[len(out)-1] != '\n' {
			fmt.Fprintln(os.Stdout)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "checkout:", err)
		return 1
	}
	return 0
}

func cmdSync(args []string) int {
	flags := flag.NewFlagSet("sync", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var values runFlags
	addRunFlags(flags, &values, false)
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	if (values.restoreSet || values.reverseReconcileSet) && !values.noWorkingCopy {
		fmt.Fprintln(os.Stderr, "sync: -restore-set and -reverse-reconcile-set require -nwc")
		return 2
	}
	if values.restoreSet && values.reverseReconcileSet {
		fmt.Fprintln(os.Stderr, "sync: -restore-set and -reverse-reconcile-set cannot be combined")
		return 2
	}
	cfg, spec, err := values.open()
	if err != nil {
		fmt.Fprintln(os.Stderr, "sync:", err)
		return 2
	}
	if values.noWorkingCopy {
		return cmdSyncNoWC(cfg, spec, values)
	}
	if err := lockPublishSandbox(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "sync:", err)
		return 1
	}
	if err := prepareSSHPath(cfg); err != nil {
		fmt.Fprintln(os.Stderr, "sync:", err)
		return 1
	}
	lock, err := acquireCopyLock(spec.Path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sync:", err)
		return 1
	}
	defer lock.Close()
	if err := appendRunLog(cfg.LogDir, spec.ID, "sync", "started", false); err != nil {
		fmt.Fprintln(os.Stderr, "sync journal:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), values.timeout)
	defer cancel()
	err = syncWorkingCopy(ctx, cfg.svnClient(values.timeout), spec.Path, values.message)
	stage := "complete"
	if err != nil {
		stage = "failed"
	}
	if logErr := appendRunLog(cfg.LogDir, spec.ID, "sync", stage, err == nil); logErr != nil {
		fmt.Fprintln(os.Stderr, "sync journal:", logErr)
		return 1
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sync:", err)
		return 1
	}
	return 0
}
