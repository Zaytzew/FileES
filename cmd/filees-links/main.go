package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/fcgi"
	"os"
	"os/signal"
	"syscall"
	"time"

	"filees/internal/obsandbox"
	"filees/public-shares/linkservice"
	"filees/public-shares/storage"
)

const linksSandboxPromises = "stdio rpath wpath cpath fattr flock unix inet"

func main() {
	configPath := flag.String("config", "/etc/filees/public-links.json", "public links configuration")
	check := flag.Bool("check-maintenance", false, "check private cache maintenance status without starting the service")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	if *check {
		err = checkMaintenance(*configPath)
	} else {
		err = run(ctx, *configPath)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "filees-links:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configPath string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	runtime, err := linkservice.Load(configPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if runtime.Store != nil {
		owner, err := storage.Own(runtime.Config.Cache.Root)
		if err != nil {
			return err
		}
		defer owner.Close()
	}
	var active storage.Activity
	defer active.StopAndWait()
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
	if runtime.Store != nil {
		maintenance := &storage.Maintenance{Root: runtime.Config.Cache.Root, Interval: runtime.CleanupInterval, Sweep: runtime.Store.Sweep, Report: func(err error) { fmt.Fprintln(os.Stderr, "filees-links maintenance:", err) }}
		stop := maintenance.Start(ctx)
		defer stop()
	}
	handler := runtime.Handler()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- fcgi.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !active.Enter() {
				http.Error(w, "service stopping", http.StatusServiceUnavailable)
				return
			}
			defer active.Leave()
			handler.ServeHTTP(w, r.WithContext(ctx))
		}))
	}()
	select {
	case <-ctx.Done():
		_ = listener.Close()
		<-serveDone
		return nil
	case err := <-serveDone:
		cancel()
		return err
	}
}

func checkMaintenance(configPath string) error {
	runtime, err := linkservice.LoadForCheck(configPath)
	if err != nil {
		return err
	}
	if !runtime.Config.Cache.Enabled {
		return fmt.Errorf("cache is disabled")
	}
	if err := obsandbox.Apply(obsandbox.Profile{Name: "filees-links-maintenance-check", Promises: "stdio rpath", Paths: []obsandbox.Path{{Label: "maintenance-status", Name: runtime.Config.Cache.Root, Perms: "r"}}}); err != nil {
		return err
	}
	status, err := storage.CheckMaintenance(runtime.Config.Cache.Root, runtime.CleanupInterval, time.Now())
	if encodeErr := json.NewEncoder(os.Stdout).Encode(status); encodeErr != nil {
		return encodeErr
	}
	return err
}
