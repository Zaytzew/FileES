package main

import (
	"os"

	"filees/internal/servertool"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "repository-control" {
		os.Exit(servertool.RunRepositoryWorker(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(servertool.RunWorker(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
