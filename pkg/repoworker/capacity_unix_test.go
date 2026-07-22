//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package repoworker

import (
	"context"
	"testing"
)

func TestFilesystemCapacityIncludesTransactionAndSafetyMargin(t *testing.T) {
	available, required, err := (FilesystemCapacity{Root: t.TempDir()}).Check(context.Background(), 100<<20)
	if err != nil {
		t.Fatal(err)
	}
	if available <= 0 || required != (264<<20) {
		t.Fatalf("available=%d required=%d", available, required)
	}
}
