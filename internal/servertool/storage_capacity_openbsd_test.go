//go:build openbsd

package servertool

import (
	"filees/internal/obsandbox"
	"filees/internal/storagewatch"
	v1 "filees/pkg/reservation/v1"
	"filees/pkg/serverconfig"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorageCapacityReadUnderLockedUnveil(t *testing.T) {
	if isolateSandboxingTest(t, "TestStorageCapacityReadUnderLockedUnveil") {
		return
	}
	root := sandboxedTestRoot(t)
	c := serverconfig.Config{}
	c.Repositories.Root = filepath.Join(root, "repos")
	if err := os.MkdirAll(c.Repositories.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := obsandbox.Apply(obsandbox.Profile{Name: "capacity-read", Promises: reservationWorkerRuntimePromises, Paths: []obsandbox.Path{{Label: "repositories", Name: c.Repositories.Root, Perms: "r"}}}); err != nil {
		t.Fatal(err)
	}
	r := v1.Result{ServerID: "spot"}
	attachStorageCapacity(c, v1.Request{Schema: v1.StateSchema, IncludeStorageCapacity: true}, &r, time.Now(), storagewatch.Native{}.Sample)
	if r.StorageCapacity == nil || r.StorageCapacity.State != "observed" {
		t.Fatal("statfs under worker sandbox", r)
	}
}
