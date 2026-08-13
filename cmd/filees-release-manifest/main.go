package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"filees/internal/releasepublish"
)

func main() {
	specPath := flag.String("spec", "", "path to the release manifest specification")
	payloadRoot := flag.String("payload", "", "root containing files named by the specification")
	output := flag.String("output", "", "manifest output path (default: stdout)")
	releaseID := flag.String("release-id", "", "override release_id from the policy specification")
	platform := flag.String("platform", "", "override platform from the policy specification")
	svnRevision := flag.String("svn-revision", "", "override source svn_revision")
	sequence := flag.Uint64("sequence", 0, "override monotonic release sequence")
	securityEpoch := flag.Uint64("security-epoch", 0, "override security epoch")
	flag.Parse()
	if *specPath == "" || *payloadRoot == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: filees-release-manifest -spec SPEC.json -payload DIR [-output manifest.json]")
		os.Exit(2)
	}
	spec, err := releasepublish.LoadSpec(*specPath)
	if err != nil {
		die(err)
	}
	if *releaseID != "" {
		spec.ReleaseID = *releaseID
	}
	if *platform != "" {
		spec.Platform = *platform
	}
	if *svnRevision != "" {
		spec.SVNRevision = *svnRevision
	}
	if *sequence != 0 {
		spec.Sequence = *sequence
	}
	if *securityEpoch != 0 {
		spec.SecurityEpoch = *securityEpoch
	}
	raw, err := releasepublish.Generate(*payloadRoot, spec)
	if err != nil {
		die(err)
	}
	if *output == "" {
		if _, err := os.Stdout.Write(raw); err != nil {
			die(err)
		}
		return
	}
	out, err := filepath.Abs(*output)
	if err != nil {
		die(err)
	}
	if err := releasepublish.WriteAtomic(out, raw); err != nil {
		die(err)
	}
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "filees-release-manifest:", err)
	os.Exit(1)
}
