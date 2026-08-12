package main

import (
	"flag"
	"fmt"
	"net/http/fcgi"
	"os"

	"filees/internal/obsandbox"
	"filees/public-shares/linkservice"
)

const linksSandboxPromises = "stdio rpath wpath cpath fattr flock unix inet"

func main() {
	configPath := flag.String("config", "/etc/filees/public-links.json", "public links configuration")
	flag.Parse()
	if err := run(*configPath); err != nil {
		fmt.Fprintln(os.Stderr, "filees-links:", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	runtime, err := linkservice.Load(configPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	listener, cleanup, err := runtime.ListenFastCGI()
	if err != nil {
		return fmt.Errorf("fastcgi listener: %w", err)
	}
	defer cleanup()
	paths := []obsandbox.Path{}
	for i, path := range runtime.SandboxPaths() {
		paths = append(paths, obsandbox.Path{Label: fmt.Sprintf("runtime-%d", i), Name: path, Perms: "rwc"})
	}
	if err := obsandbox.Apply(obsandbox.Profile{Name: "filees-links", Promises: linksSandboxPromises, Paths: paths}); err != nil {
		return fmt.Errorf("sandbox: %w", err)
	}
	if err := fcgi.Serve(listener, runtime.Handler()); err != nil {
		return fmt.Errorf("fastcgi: %w", err)
	}
	return nil
}
