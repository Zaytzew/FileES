package main

import (
	"os"

	"filees/internal/servertool"
)

func main() {
	os.Exit(servertool.RunBootstrapEntry(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
