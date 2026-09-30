// Command filees-release-notes drafts and checks the signed "what's new" list
// of a release (releases/<id>/notes.json, see internal/releasenotes).
//
//	svn log --xml -r FROM:TO SOURCE | filees-release-notes draft -component desktop \
//	    -release-id r1720 -sequence 1720 -svn-revision 1720 \
//	    -previous FILEES-BIN/releases/r1698/notes.json -out releases/r1720/notes.draft.json
//	filees-release-notes lint -release-id r1720 -sequence 1720 releases/r1720/notes.json
//
// The draft is only a starting point. A person reads it, completes the
// missing language, removes what a user would not notice, and saves it as
// notes.json before the release is signed.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filees/internal/releasenotes"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "filees-release-notes:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: filees-release-notes draft|lint ...")
	}
	switch args[0] {
	case "draft":
		return runDraft(args[1:], stdin, stdout, stderr)
	case "lint":
		return runLint(args[1:], stdout)
	default:
		return fmt.Errorf("unknown command %q; use draft or lint", args[0])
	}
}

func runDraft(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("draft", flag.ContinueOnError)
	component := flags.String("component", "", "server, desktop or android")
	releaseID := flags.String("release-id", "", "release being prepared")
	sequence := flags.Uint64("sequence", 0, "sequence of that release")
	svnRevision := flags.String("svn-revision", "", "source revision the release is built from")
	previousPath := flags.String("previous", "", "notes.json of the previous release of this component (optional)")
	logPath := flags.String("log", "-", "svn log --xml output, - for standard input")
	outPath := flags.String("out", "", "draft to write; must not exist")
	securityEpoch := flags.Uint64("security-epoch", 0, "security_epoch of this release (0: unknown)")
	previousEpoch := flags.Uint64("previous-security-epoch", 0, "security_epoch of the previous release (0: unknown)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *releaseID == "" || *sequence == 0 || *outPath == "" {
		return errors.New("draft needs -component, -release-id, -sequence and -out")
	}
	var previous *releasenotes.Notes
	if *previousPath != "" {
		data, err := os.ReadFile(*previousPath)
		if err != nil {
			return err
		}
		// The previous file was reviewed and signed; a draft is never carried.
		if previous, err = releasenotes.Parse(data); err != nil {
			return fmt.Errorf("%s: %w", *previousPath, err)
		}
	}
	var log io.Reader = stdin
	if *logPath != "-" {
		file, err := os.Open(*logPath)
		if err != nil {
			return err
		}
		defer file.Close()
		log = file
	}
	entries, err := releasenotes.ParseSVNLog(log)
	if err != nil {
		return err
	}
	notes, warnings, err := releasenotes.Draft(previous, entries, *component, *releaseID, *sequence, *svnRevision)
	for _, warning := range warnings {
		fmt.Fprintln(stderr, "warning:", warning)
	}
	if err != nil {
		return err
	}
	if notes.AddSecurityPlaceholder(*previousEpoch, *securityEpoch) {
		fmt.Fprintf(stderr, "warning: this release raises security_epoch from %d to %d; fill in the empty security item\n", *previousEpoch, *securityEpoch)
	}
	data, err := json.MarshalIndent(notes, "", "  ")
	if err != nil {
		return err
	}
	out, err := os.OpenFile(*outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := out.Write(append(data, '\n')); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	fresh := 0
	for _, item := range notes.Items {
		if item.Sequence == notes.Sequence {
			fresh++
		}
	}
	fmt.Fprintf(stdout, "drafted %s: %d new item(s), %d carried; review, then save as %s\n",
		*outPath, fresh, len(notes.Items)-fresh, filepath.Join(filepath.Dir(*outPath), releasenotes.FileName))
	return nil
}

func runLint(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("lint", flag.ContinueOnError)
	releaseID := flags.String("release-id", "", "expected release_id (optional)")
	sequence := flags.Uint64("sequence", 0, "expected sequence (optional)")
	component := flags.String("component", "", "expected component (optional)")
	securityEpoch := flags.Uint64("security-epoch", 0, "security_epoch of this release (0: unknown)")
	previousEpoch := flags.Uint64("previous-security-epoch", 0, "security_epoch of the previous release (0: unknown)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return errors.New("lint needs a notes.json path")
	}
	for _, path := range flags.Args() {
		if filepath.Base(path) == releasenotes.DraftName {
			return fmt.Errorf("%s is a draft; review it and save it as %s", path, releasenotes.FileName)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		notes, err := releasenotes.Parse(data)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if *releaseID != "" && notes.ReleaseID != *releaseID {
			return fmt.Errorf("%s: release_id %s, expected %s", path, notes.ReleaseID, *releaseID)
		}
		if *sequence != 0 && notes.Sequence != *sequence {
			return fmt.Errorf("%s: sequence %d, expected %d", path, notes.Sequence, *sequence)
		}
		if *component != "" && notes.Component != *component {
			return fmt.Errorf("%s: component %s, expected %s", path, notes.Component, *component)
		}
		if err := notes.CheckSecurityEpoch(*previousEpoch, *securityEpoch); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		fmt.Fprintf(stdout, "%s: ok (%s, %d item(s))\n", path, strings.TrimSpace(notes.ReleaseID), len(notes.Items))
	}
	return nil
}
