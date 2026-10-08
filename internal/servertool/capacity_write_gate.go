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

func capacityWriterRoot(onboardingRoot, activationRoot string) string {
	if filepath.IsAbs(activationRoot) {
		return activationRoot
	}
	return onboardingRoot // pre-activation v1 layout
}

// Admission creates this directory before the action locks its unveil table.
// Acknowledgement needs unlink and directory fsync, not activation authority,
// gate policy writes or creation of new files in the activation root.
func capacityWriterCompletionPath(root string) obsandbox.Path {
	return obsandbox.Path{Label: "capacity-writer-completion", Name: filepath.Join(root, "capacity-active-writers"), Perms: "rwc"}
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
	root := capacityWriterRoot(c.Root, c.Activation.Root)
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
