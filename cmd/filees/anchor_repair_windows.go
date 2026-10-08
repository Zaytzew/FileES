//go:build windows && !nocfapi

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"filees/internal/anchorrepair"
)

// Exit codes of `filees anchor-repair`. The GUI starts this command elevated
// and can read nothing but the code: an elevated process writes no file for
// an unelevated one to trust. What was removed is learned by looking at the
// folder again.
const (
	anchorRepairDone        = 0 // nothing unreadable is left, the filter is attached
	anchorRepairUsage       = 2
	anchorRepairNoRights    = 3 // nothing was changed
	anchorRepairRefused     = 4 // not a FileES virtual folder; nothing was changed
	anchorRepairFailed      = 5 // nothing was removed, the filter is attached
	anchorRepairIncomplete  = 6 // some entries stayed, the filter is attached
	anchorRepairNotAttached = 7 // the filter did not come back on the volume
)

func anchorRepairExit(result anchorrepair.Result) int {
	switch {
	case result.Detached && !result.Reattached:
		return anchorRepairNotAttached
	case result.Error == "":
		return anchorRepairDone
	case result.Error == "elevation_required":
		return anchorRepairNoRights
	case result.Error == "refused_path" || result.Error == "not_anchor":
		return anchorRepairRefused
	case result.Error == "remove":
		return anchorRepairIncomplete
	case result.Error == "attach":
		return anchorRepairNotAttached
	default:
		return anchorRepairFailed
	}
}

// cmdAnchorRepair removes the entries of one virtual folder whose cloud
// metadata Windows cannot read, by detaching the cloud files filter from the
// volume for the duration (internal/anchorrepair). It is given a folder and
// nothing else: which entries are damaged is its own finding.
func cmdAnchorRepair(args []string) int {
	return runAnchorRepair(args, anchorrepair.Windows{}, os.Stdout)
}

func runAnchorRepair(args []string, system anchorrepair.System, out io.Writer) int {
	flags := flag.NewFlagSet("anchor-repair", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", "", "virtual folder")
	reattach := flags.Bool("reattach", false, "only attach the cloud files filter to the folder's volume again")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *root == "" {
		fmt.Fprintln(os.Stderr, "usage: filees anchor-repair --root <virtual folder> [--reattach]")
		return anchorRepairUsage
	}
	var result anchorrepair.Result
	if *reattach {
		result = anchorrepair.Reattach(system, *root, "")
	} else {
		result = anchorrepair.Repair(system, *root, "")
	}
	raw, _ := json.Marshal(result)
	fmt.Fprintln(out, string(raw))
	return anchorRepairExit(result)
}
