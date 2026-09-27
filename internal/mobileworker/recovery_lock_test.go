//go:build linux || openbsd

package mobileworker

import (
	"io"
	"os/exec"
	"testing"
)

func TestOperationFenceSurvivesWorkerDescriptorClose(t *testing.T) {
	ledger := Ledger{Dir: t.TempDir()}
	lock, err := ledger.lockOperation("operation")
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("/bin/sh", "-c", "printf ready; read finish")
	if err := inheritOperationLock(child, lock); err != nil {
		t.Fatal(err)
	}
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { input.Close(); child.Wait() }()
	if _, err := io.ReadFull(output, make([]byte, 5)); err != nil {
		t.Fatal(err)
	}
	lock.Close() // parent dies; SVN child still owns its inherited fd
	if another, err := ledger.lockOperation("operation"); err == nil {
		another.Close()
		t.Fatal("orphan child lost operation fence")
	}
	input.Close()
	_ = child.Wait()
	another, err := ledger.lockOperation("operation")
	if err != nil {
		t.Fatalf("fence not released on child exit: %v", err)
	}
	another.Close()
}
