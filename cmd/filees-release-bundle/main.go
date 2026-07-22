package main

import (
	"flag"
	"fmt"
	"os"

	"filees/internal/releasepublish"
)

func main() {
	source := flag.String("source", "", "client bundle directory")
	output := flag.String("output", "", "deterministic tar.gz output")
	flag.Parse()
	if *source == "" || *output == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: filees-release-bundle -source DIR -output FILE.tar.gz")
		os.Exit(2)
	}
	if err := releasepublish.BundleDirectory(*source, *output); err != nil {
		fmt.Fprintln(os.Stderr, "filees-release-bundle:", err)
		os.Exit(1)
	}
}
