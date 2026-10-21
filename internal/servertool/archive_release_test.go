package servertool

import (
	"context"
	"fmt"
	"os"
	"testing"

	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
)

func TestArchiveReleaseUsesRealLocalOSAuthority(t *testing.T) {
	administrator := localArchiveAdministrator(t.TempDir())
	actor, err := administrator(t.Context(), "", repoworker.RepositoryArchivePolicy{})
	if repoworker.FileLocksSupported() {
		if err != nil || actor != fmt.Sprintf("local-service-owner:uid:%d", os.Geteuid()) {
			t.Fatal(actor, err)
		}
	} else {
		if err == nil || actor != "" {
			t.Fatal("unsupported platform acquired administrative authority")
		}
		if _, err := releaseRepositoryArchive(t.Context(), serverconfig.Config{}, "", repoworker.ArchiveReleaseRequest{}); err == nil {
			t.Fatal("unsupported platform entered executor")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := administrator(ctx, "", repoworker.RepositoryArchivePolicy{}); err == nil {
		t.Fatal("canceled authority accepted")
	}
}
