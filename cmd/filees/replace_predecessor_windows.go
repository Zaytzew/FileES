//go:build windows

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"filees/internal/gui/singleinstance"
	"filees/internal/predecessor"
	"filees/pkg/ipcclient"
)

// cmdReplacePredecessor is the work behind the GUI's "remove the previous
// version" question. The Store launcher and the MSI supervisor ask; this
// command does, in an order chosen so that the one irreversible step comes
// last: stop the other pair, carry its settings (validated by this variant's
// own config-check), and only then uninstall it.
func cmdReplacePredecessor(args []string) int {
	if err := runReplacePredecessor(context.Background(), args, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "replace-predecessor:", err)
		return 1
	}
	return 0
}

type replaceOptions struct {
	from   predecessor.Kind
	keep   bool
	target string
}

func parseReplaceOptions(args []string) (replaceOptions, error) {
	flags := flag.NewFlagSet("replace-predecessor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	from := flags.String("from", "", "msi or store")
	settings := flags.String("settings", "", "keep or default")
	target := flags.String("config", "", "this variant's configuration file")
	const usage = "usage: filees replace-predecessor --from msi|store --settings keep|default --config path"
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return replaceOptions{}, errors.New(usage)
	}
	kind, err := predecessor.ParseKind(*from)
	if err != nil {
		return replaceOptions{}, fmt.Errorf("%v; %s", err, usage)
	}
	if *settings != "keep" && *settings != "default" {
		return replaceOptions{}, errors.New(usage)
	}
	if !filepath.IsAbs(*target) {
		return replaceOptions{}, errors.New("--config must be an absolute path")
	}
	return replaceOptions{from: kind, keep: *settings == "keep", target: filepath.Clean(*target)}, nil
}

// replacementAllowed pairs each variant with the only predecessor it may
// remove. The check is on the running binary, not on the arguments, so no
// invocation can make a variant uninstall itself.
func replacementAllowed(from predecessor.Kind, packaged bool, mode string) error {
	store := packaged && mode == "store"
	msi := !packaged && mode == ""
	switch {
	case from == predecessor.KindMSI && store:
		return nil
	case from == predecessor.KindStore && msi:
		return nil
	case from == predecessor.KindMSI:
		return errors.New("only the Microsoft Store version replaces the MSI installation")
	default:
		return errors.New("only the MSI installation replaces the Microsoft Store version")
	}
}

func runReplacePredecessor(ctx context.Context, args []string, out io.Writer) error {
	opts, err := parseReplaceOptions(args)
	if err != nil {
		return err
	}
	packaged, err := windowsPackageIdentityPresent()
	if err != nil {
		return err
	}
	if err := replacementAllowed(opts.from, packaged, injectedClientUpdateMode); err != nil {
		return err
	}
	// Uninstalling takes long enough for a second click on the tile to start
	// another launcher, which would ask again and run this again. Two
	// uninstalls of the same product at once is not something to find out
	// about from msiexec.
	lock, err := singleinstance.Acquire("FileESDesktopReplacePredecessor")
	if errors.Is(err, singleinstance.ErrAlreadyRunning) {
		return errors.New("the previous version is already being removed")
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	var dir, sourceConfig string
	var productCodes []string
	switch opts.from {
	case predecessor.KindMSI:
		present, err := predecessor.MSIPresent(os.Getenv("LOCALAPPDATA"))
		if err != nil {
			return err
		}
		if !present {
			fmt.Fprintln(out, "no MSI installation to replace")
			return nil
		}
		dir, _ = predecessor.MSIInstallDir(os.Getenv("LOCALAPPDATA"))
		productCodes, err = predecessor.MSIProductCodes()
		if err != nil {
			return err
		}
		if len(productCodes) == 0 {
			// Files without an installer registration are somebody's manual
			// copy. Deleting a folder we did not install is not ours to do.
			return fmt.Errorf("FileES in %s is not registered with Windows Installer; remove that folder manually", dir)
		}
		sourceConfig = filepath.Join(dir, "config.json")
	case predecessor.KindStore:
		dir, err = predecessor.StorePackageLocation(ctx)
		if err != nil {
			return err
		}
		if dir == "" {
			fmt.Fprintln(out, "no Microsoft Store installation to replace")
			return nil
		}
		sourceConfig = filepath.Join(home, ".filees", "store", "config.json")
	}

	// The supervisor first: a supervisor still running after its daemon
	// stopped would restart it, or adopt ours once we start (sandbox,
	// 2026-09-25).
	if _, err := predecessor.TerminateScriptsFrom(dir); err != nil {
		return fmt.Errorf("close the previous supervisor: %w", err)
	}
	if err := stopPredecessorDaemon(ctx); err != nil {
		return err
	}
	if _, err := predecessor.TerminateUnder(dir); err != nil {
		return fmt.Errorf("close the previous version: %w", err)
	}

	if opts.keep {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		carried, err := predecessor.CarrySettings(sourceConfig, opts.target, filepath.Join(home, ".filees", "migration-backups"), time.Now(), func(path string) error {
			output, err := exec.CommandContext(ctx, self, "config-check", "--config", path).CombinedOutput()
			if err != nil {
				return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
			}
			return nil
		})
		if err != nil {
			return err
		}
		if carried.Carried {
			fmt.Fprintf(out, "settings carried from the previous version; copies in %s\n", carried.Backup)
		} else {
			fmt.Fprintln(out, "the previous version had no settings to carry")
		}
	}

	uninstall, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	switch opts.from {
	case predecessor.KindMSI:
		for _, code := range productCodes {
			if err := predecessor.UninstallMSI(uninstall, code); err != nil {
				return err
			}
		}
		if present, err := predecessor.MSIPresent(os.Getenv("LOCALAPPDATA")); err != nil || present {
			return fmt.Errorf("the MSI installation is still present after uninstalling (%v)", err)
		}
	case predecessor.KindStore:
		if err := predecessor.RemoveStorePackage(uninstall); err != nil {
			return err
		}
		if left, err := predecessor.StorePackageLocation(uninstall); err != nil || left != "" {
			return fmt.Errorf("the Microsoft Store version is still installed after removal (%v)", err)
		}
	}
	fmt.Fprintf(out, "previous %s version removed\n", opts.from)
	return nil
}

// stopPredecessorDaemon asks whichever daemon owns the user socket to stop and
// waits until it no longer answers. Before this runs the calling variant has
// not started its own daemon, so the one answering is the predecessor's. The
// wait is generous for the same reason `filees shutdown` is: a clean stop runs
// one last scan and writes the manifest before it exits.
func stopPredecessorDaemon(ctx context.Context) error {
	client := ipcclient.New(ipcclient.DefaultSocketPath(), "filees-replace-predecessor")
	answers := func() bool {
		probe, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
		defer cancel()
		_, err := client.RepoList(probe)
		return err == nil
	}
	if !answers() {
		return nil
	}
	stop, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := client.SystemShutdown(stop); err != nil {
		return fmt.Errorf("stop the previous version's daemon: %w", err)
	}
	for answers() {
		select {
		case <-stop.Done():
			return errors.New("the previous version's daemon did not stop within two minutes")
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil
}
