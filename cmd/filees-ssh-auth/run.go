package main

import (
	"io"

	"filees/internal/servertool"
)

func run(args []string, auth io.ReadWriter, stderr io.Writer) int {
	return servertool.RunSSHAuth(args, auth, stderr)
}
