package main

import (
	"os"

	"filees/internal/servertool"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--session-child" {
		os.Exit(servertool.RunClientSessionChild(os.Args[2:], os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "--whale-session-child" {
		os.Exit(servertool.RunClientWhaleSessionChild(os.Args[2:], os.Stderr))
	}
	os.Exit(servertool.RunClientEntry(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}
