package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"filees/pkg/ipcclient"
)

// cmdShutdown asks the running daemon to stop the way it expects to be stopped.
//
// There was no way to do this from a script, and on Windows that is not a
// convenience but the difference between keeping state and losing it. Windows
// has no SIGTERM to deliver, so every stop was Stop-Process - a kill - while
// the watcher's manifest is written when the scan loop's context is cancelled.
// Measured on the owner's machine: a manifest seven hours old across many
// restarts, and a queue of work shown to him that did not exist.
//
// r807 made a published batch checkpoint the manifest, which covers work that
// reached the server. This covers the rest, and it is what a deploy or restart
// script should call before replacing the binary.
func cmdShutdown(args []string) int {
	_, _ = parseConfigFlag(args) // accepted for symmetry with the other subcommands

	cli := ipcclient.New(ipcclient.DefaultSocketPath(), "fileesctl")
	// Generous, because a clean stop is allowed to take its time: the scan loop
	// runs one final pass so changes made after the last scan still reach the
	// commit service, and only then writes the manifest. Cutting that short
	// would defeat the point of asking politely.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if _, err := cli.SystemShutdown(ctx); err != nil {
		// Reported rather than classified. There is no reliable way here to
		// tell "already stopped" from "refused": the socket file outlives a
		// killed daemon, so its presence proves nothing, and guessing from the
		// error text would be the kind of inference this project keeps paying
		// for. A caller that does not care can ignore the exit code.
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Fprintf(os.Stderr, "hint: demon może już nie działać; sprawdź `filees status`\n")
		return 1
	}
	fmt.Println("demon zatrzymuje się")
	return 0
}
