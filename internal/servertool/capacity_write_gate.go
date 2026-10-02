package servertool

import (
	"context"
	"io"
	"path/filepath"
	"time"

	"filees/internal/obsandbox"
	"filees/internal/storagewatch"
	"filees/pkg/serverconfig"
)

func capacityGatePaths(c serverconfig.Config) []obsandbox.Path {
	// Parent read/create is needed for the stable lock and atomic policy paths.
	// This is the already private activation root, never a client-selected path.
	return []obsandbox.Path{{Label: "capacity-write-gate", Name: c.Activation.Root, Perms: "rwc"}}
}

func runConfiguredCapacityWriter(path string, stderr io.Writer, run func() int) int {
	c, err := serverconfig.LoadFor(path, 0)
	if err != nil {
		report(stderr, "capacity gate config", err)
		return ExitConfig
	}
	return runCapacityWriter(c, stderr, run)
}

// The lease precedes all worker/WC locks and covers children and deferred
// cleanup. It must not be taken from inside withServiceWorkingCopy.
func runCapacityWriter(c serverconfig.Config, stderr io.Writer, run func() int) int {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	code := ExitTempFail
	root := c.Activation.Root
	// Pre-activation v1 onboarding still has its private initialized state root.
	if !filepath.IsAbs(root) {
		root = c.Root
	}
	err := (storagewatch.Gate{Root: root}).WithWriter(ctx, func() error {
		code = run()
		return nil
	})
	if err != nil {
		report(stderr, "server capacity write stop", err)
		return ExitTempFail
	}
	return code
}
