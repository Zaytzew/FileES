package main

import (
	"context"
	"testing"
	"time"

	"filees/pkg/runtime"
)

func TestProvisionerAdmissionBlocksBeforeTouchingLifecycle(t *testing.T) {
	var admission runtime.Admission
	resume, err := admission.Quiesce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer resume()
	p := &daemonProvisioner{admission: &admission} // nil stores expose accidental work
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	p.runOne(ctx, "durable-operation")
	if s := admission.Snapshot(); s.Active != 0 || !s.Closed {
		t.Fatal(s)
	}
}
