package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"filees/pkg/recoverykit"
)

func cmdRecovery(args []string) int {
	if len(args) < 1 || args[0] != "download" {
		fmt.Fprintln(os.Stderr, "usage: filees recovery download --kit FILE.fkr --output DIR")
		return 2
	}
	flags := flag.NewFlagSet("recovery download", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	kitPath := flags.String("kit", "", "absolute path to recovery kit")
	outputPath := flags.String("output", "", "absolute output directory")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return 2
	}
	if !filepath.IsAbs(*kitPath) || !filepath.IsAbs(*outputPath) {
		fmt.Fprintln(os.Stderr, "recovery kit and output paths must be absolute")
		return 2
	}
	now := time.Now().UTC()
	kit, err := recoverykit.Load(filepath.Clean(*kitPath), now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load recovery kit: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	paths, err := recoverykit.Download(ctx, kit, filepath.Clean(*outputPath), now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "download recovery archives: %v\n", err)
		return 1
	}
	for _, path := range paths {
		fmt.Println(path)
	}
	return 0
}
