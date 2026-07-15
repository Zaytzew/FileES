package main

import (
	"os"

	"filees/internal/servertool"
)

func main() { os.Exit(servertool.RunAdmin(os.Args[1:], os.Stdout, os.Stderr)) }
