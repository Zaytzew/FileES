package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"filees/internal/serverinstall/config"
	"filees/internal/serverinstall/platform"
	"filees/internal/serverinstall/svnfetch"
	"filees/internal/serverinstall/updater"
)

func main() {
	cfgFlag := flag.String("c", "", "configuration file")
	checkConfig := flag.Bool("check-config", false, "print resolved configuration and exit")
	check := flag.Bool("check", false, "check the configured channel without installing")
	dryRun := flag.Bool("dry-run", false, "show the install plan without downloading payload files")
	apply := flag.Bool("apply", false, "download, verify, and install a release")
	adopt := flag.Bool("adopt", false, "verify and adopt an existing installation as the managed baseline")
	rollback := flag.Bool("rollback", false, "restore the previous locally installed release (binary rollback)")
	revertTo := flag.String("revert-to", "", "install a specific release ID, e.g. r7")
	purge := flag.Bool("purge", false, "remove all installed files and system configuration (stage 2 rollback)")
	wipeState := flag.Bool("wipe-state", false, "with --purge: also delete /var/filees and /etc/filees (irreversible)")
	yes := flag.Bool("yes", false, "answer yes to interactive prompts")
	talkative := flag.Bool("talkative", false, "print [SECURITY] unveil/pledge diagnostics")
	// A downgrade is a deliberate administrative act, never an ordinary
	// update: without this flag a release older than the one already installed
	// is refused outright (anti-rollback, audit Finding E).
	allowRollback := flag.Bool("allow-rollback", false, "permit installing a release older than the one recorded locally")
	flag.Parse()

	cfgPath, err := config.Find(*cfgFlag)
	if err != nil {
		die(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		die(err)
	}
	cfg.Talkative = cfg.Talkative || *talkative
	if key, ok := releasePubkey(); ok {
		cfg.SignifyPubkey = key
	}

	if *checkConfig {
		runCheckConfig(cfg, os.Stdout)
		os.Exit(0)
	}

	if cfg.VerifySignature && !pubkeyConfigured() {
		die(fmt.Errorf("policy.verify_signature is true but no release key is embedded in this build (see cmd/filees-install/assets/release.pub)"))
	}

	plat, err := platform.New()
	if err != nil {
		die(err)
	}

	action, releaseID, purgeOpts, err := selectAction(cfg, actionFlags{
		check:     *check,
		dryRun:    *dryRun,
		apply:     *apply,
		adopt:     *adopt,
		rollback:  *rollback,
		revertTo:  *revertTo,
		purge:     *purge,
		wipeState: *wipeState,
		yes:       *yes,
		args:      flag.Args(),
	})
	if err != nil {
		die(err)
	}

	r := updater.NewRunner(cfg, svnfetch.SVN{
		Program: cfg.SVNPath,
		RepoURL: cfg.RepoURL,
	}, plat, os.Stdout, os.Stdin)
	ctx := context.Background()

	switch action {
	case "check":
		if err := r.Check(ctx, updater.Options{ReleaseID: releaseID, AllowRollback: *allowRollback}); err != nil {
			die(err)
		}
	case "dry-run":
		if err := r.Apply(ctx, updater.Options{ReleaseID: releaseID, DryRun: true, Yes: *yes, AllowRollback: *allowRollback}); err != nil {
			die(err)
		}
	case "apply":
		if err := r.Apply(ctx, updater.Options{ReleaseID: releaseID, Yes: *yes, AllowRollback: *allowRollback}); err != nil {
			die(err)
		}
	case "adopt":
		if err := r.Adopt(ctx, updater.Options{ReleaseID: releaseID}); err != nil {
			die(err)
		}
	case "rollback":
		if err := r.Rollback(); err != nil {
			die(err)
		}
	case "purge":
		if err := r.Purge(purgeOpts); err != nil {
			die(err)
		}
	default:
		die(fmt.Errorf("unknown action %q", action))
	}
}

type actionFlags struct {
	check     bool
	dryRun    bool
	apply     bool
	adopt     bool
	rollback  bool
	revertTo  string
	purge     bool
	wipeState bool
	yes       bool
	args      []string
}

func selectAction(cfg *config.Config, f actionFlags) (action, releaseID string, purgeOpts updater.PurgeOptions, err error) {
	count := 0
	for _, on := range []bool{f.check, f.dryRun, f.apply, f.adopt, f.rollback, f.purge, strings.TrimSpace(f.revertTo) != ""} {
		if on {
			count++
		}
	}
	if count > 1 {
		return "", "", updater.PurgeOptions{}, fmt.Errorf("choose one action: --check, --dry-run, --apply, --adopt, --rollback, --purge or --revert-to")
	}
	if strings.TrimSpace(f.revertTo) != "" {
		return "apply", strings.TrimSpace(f.revertTo), updater.PurgeOptions{}, nil
	}
	if f.purge {
		return "purge", "", updater.PurgeOptions{WipeState: f.wipeState, Yes: f.yes}, nil
	}
	if f.rollback {
		return "rollback", "", updater.PurgeOptions{}, nil
	}
	if f.apply {
		return "apply", firstArg(f.args), updater.PurgeOptions{}, nil
	}
	if f.adopt {
		return "adopt", firstArg(f.args), updater.PurgeOptions{}, nil
	}
	if f.dryRun {
		return "dry-run", firstArg(f.args), updater.PurgeOptions{}, nil
	}
	if f.check {
		return "check", firstArg(f.args), updater.PurgeOptions{}, nil
	}
	return cfg.DefaultAction, firstArg(f.args), updater.PurgeOptions{}, nil
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return strings.TrimSpace(args[0])
}

func die(err any) {
	fmt.Fprintln(os.Stderr, "filees-install:", err)
	os.Exit(1)
}
