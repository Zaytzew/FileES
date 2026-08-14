package updater

import (
	"context"
	"strings"
	"testing"

	"filees/internal/serverinstall/config"
)

func TestRunLockRejectsConcurrentInstallerAndReleasesOnClose(t *testing.T) {
	cfg := &config.Config{StateDir: t.TempDir()}
	r := &Runner{Config: cfg}

	first, err := r.acquireRunLock()
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	second, err := r.acquireRunLock()
	if err == nil {
		second.Close()
		first.Close()
		t.Fatal("concurrent installer unexpectedly acquired lock")
	}
	if !strings.Contains(err.Error(), "already running") {
		first.Close()
		t.Fatalf("concurrent lock error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("release first lock: %v", err)
	}

	afterRelease, err := r.acquireRunLock()
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if err := afterRelease.Close(); err != nil {
		t.Fatalf("release final lock: %v", err)
	}
}

func TestEveryInstallerActionTakesRunLockFirst(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Runner) error
	}{
		{name: "check", run: func(r *Runner) error { return r.Check(context.Background(), Options{}) }},
		{name: "apply", run: func(r *Runner) error { return r.Apply(context.Background(), Options{}) }},
		{name: "adopt", run: func(r *Runner) error { return r.Adopt(context.Background(), Options{}) }},
		{name: "rollback", run: func(r *Runner) error { return r.Rollback() }},
		{name: "purge", run: func(r *Runner) error { return r.Purge(PurgeOptions{Yes: true}) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Runner{Config: &config.Config{StateDir: t.TempDir()}}
			held, err := r.acquireRunLock()
			if err != nil {
				t.Fatalf("hold lock: %v", err)
			}
			defer held.Close()

			err = tt.run(r)
			if err == nil || !strings.Contains(err.Error(), "already running") {
				t.Fatalf("action error = %v, want already-running refusal", err)
			}
		})
	}
}
