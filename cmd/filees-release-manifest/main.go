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
	flag.Parse()
	if *specPath == "" || *payloadRoot == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: filees-release-manifest -spec SPEC.json -payload DIR [-output manifest.json]")
		os.Exit(2)
	}
	spec, err := releasepublish.LoadSpec(*specPath)
	if err != nil {
		die(err)
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
