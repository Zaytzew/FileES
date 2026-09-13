package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"filees/pkg/clientprofile"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
	"filees/pkg/runtime"
	"github.com/google/uuid"
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
	if p.restoreOperations(ctx) {
		t.Fatal("startup accepted after cancellation while closed")
	}
	p.retryPendingCleanup(ctx)
	if s := admission.Snapshot(); s.Active != 0 || !s.Closed {
		t.Fatal(s)
	}
}

func TestRestoreAdmissionHeldUntilAttachmentHandedOff(t *testing.T) {
	local, err := localrepo.Open(filepath.Join(t.TempDir(), "local.json"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := provisioning.NewStore(filepath.Join(t.TempDir(), "provisioning"))
	if err != nil {
		t.Fatal(err)
	}
	repoID := uuid.NewString()
	record, err := local.BeginAttach("test", repoID, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = local.ApproveAttach(record.OperationID, "test", repoID, "svn+ssh://example/"+repoID, "rw"); err != nil {
		t.Fatal(err)
	}
	if _, err = local.MarkAttached(record.OperationID, repoID); err != nil {
		t.Fatal(err)
	}
	p := newDaemonProvisioner(local, store, []clientprofile.Profile{{ServerID: "test"}})
	var admission runtime.Admission
	p.admission = &admission
	attachments := make(chan provisionedAttachment)
	p.attachments = attachments
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- p.restoreOperations(ctx) }()
	for admission.Snapshot().Active == 0 {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	drainCtx, drainCancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer drainCancel()
	if resume, err := admission.Quiesce(drainCtx); err == nil {
		resume()
		t.Fatal("drained before blocked attachment publication completed")
	}
	select {
	case attachment := <-attachments:
		if attachment.Repo.ID != repoID {
			t.Fatal(attachment.Repo.ID)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !<-done {
		t.Fatal("restore aborted")
	}
	resume, err := admission.Quiesce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resume()
}
