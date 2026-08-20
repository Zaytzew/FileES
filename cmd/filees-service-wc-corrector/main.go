package main

import (
	"os"

	"filees/internal/servertool"
)

func main() {
	os.Exit(servertool.RunServiceWCOwnershipCorrector(os.Args[1:], os.Stdout, os.Stderr))
}
