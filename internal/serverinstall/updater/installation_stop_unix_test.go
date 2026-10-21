//go:build linux || openbsd

package updater

import (
	"errors"
	"strings"
	"testing"

	"filees/internal/storagewatch"
	"github.com/google/uuid"
)

func TestInstallationStopRefusesPublicInstallerActions(t *testing.T) {
	r, p := archiveInstallerFixture(t)
	g := storagewatch.Gate{Root: p.Activation.Root}
	b := storagewatch.InstallationBinding{OperationID: uuid.NewString(), PreviousManifest: strings.Repeat("a", 64), CandidateManifest: strings.Repeat("b", 64)}
	if err := g.RequestInstallationStop(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	for name, action := range map[string]func() error{
		"check":    func() error { return r.Check(t.Context(), Options{AllowRollback: true}) },
		"adopt":    func() error { return r.Adopt(t.Context(), Options{}) },
		"apply":    func() error { return r.Apply(t.Context(), Options{AllowRollback: true}) },
		"rollback": r.Rollback,
		"purge":    func() error { return r.Purge(PurgeOptions{Yes: true, WipeState: true}) },
		"recover":  r.recoverInterrupted,
	} {
		t.Run(name, func(t *testing.T) {
			if err := action(); !errors.Is(err, storagewatch.ErrInstallationStopped) {
				t.Fatal(err)
			}
		})
	}
	if err := g.CheckInstallationAdmission(); !errors.Is(err, storagewatch.ErrInstallationStopped) {
		t.Fatal("installer opened stop", err)
	}
}
