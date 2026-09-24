package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"filees/internal/nativeruntime"
)

// A release process pins its own embedded runtime, including after its EXE is
// renamed by the updater. Environment overrides remain explicit test/developer
// choices; missing payloads never silently select CLI in a bundled build.
var nativeRuntimeLease *nativeruntime.Lease

var bundledNativeSVN = sync.OnceValues(func() (string, error) {
	cache, err := nativeCacheRoot()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	nativeRuntimeLease, err = nativeruntime.Acquire(ctx, filepath.Join(cache, "FileES", "native-svn"), nativeruntime.Payload)
	if err != nil {
		return "", err
	}
	return nativeRuntimeLease.Path, nil
})

func prepareNativeSVN() error {
	if (runtime.GOOS != "windows" && runtime.GOOS != "linux") || len(nativeruntime.Payload) == 0 || os.Getenv("FILEES_NATIVE_SVN") != "" {
		return nil
	}
	_, err := bundledNativeSVN()
	return err
}

// Desktop bundle builds use all native WC/RA operations by default;
// untagged developer builds retain explicit opt-in.
func nativeSVNPath() string {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		return ""
	}
	if path := os.Getenv("FILEES_NATIVE_SVN"); path != "" {
		return path
	}
	if len(nativeruntime.Payload) != 0 {
		path, err := bundledNativeSVN()
		if err != nil {
			// main reports this before constructing clients. A caller bypassing
			// startup must fail closed too, never create a CLI-backed client.
			panic(fmt.Sprintf("native SVN runtime unavailable: %v", err))
		}
		return path
	}
	return ""
}

func maintainNativeSVN(ctx context.Context, report func(error)) {
	if nativeRuntimeLease == nil {
		return
	}
	cache := filepath.Dir(filepath.Dir(filepath.Dir(nativeRuntimeLease.Path)))
	sweep := func() {
		if _, err := nativeruntime.Sweep(ctx, cache); err != nil && ctx.Err() == nil {
			report(err)
		}
	}
	sweep()
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}

// Compatibility wrapper keeps the lease until the helper exits, including
// when invoked outside the daemon (packaged filees-svn entrypoint).
func runNativeHelper(args []string) int {
	path := nativeSVNPath()
	if !filepath.IsAbs(path) {
		fmt.Fprintln(os.Stderr, "native SVN runtime unavailable")
		return 1
	}
	cmd := exec.Command(path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	release, err := nativeruntime.PinCommand(cmd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer release()
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() > 0 {
			return exit.ExitCode()
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
