package main

import (
	"os"
)

// The BSD Authentication protocol always uses descriptor 3 for both input
// and decisions. Keeping it separate from stdin/stdout prevents accidental
// secret propagation through a shell pipeline.
func main() {
	auth := os.NewFile(3, "bsd-auth")
	if auth == nil {
		os.Exit(70)
	}
	os.Exit(run(os.Args[1:], auth, os.Stderr))
}
