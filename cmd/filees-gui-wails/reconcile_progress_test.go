package main

import (
	contract "filees/pkg/contract/v1"
	"testing"
)

func TestReconcileProjectionNeverAnimatesStaleState(t *testing.T) {
	p := &contract.ReconcileProgress{Phase: "adopting", Done: 16, Total: 40}
	if currentReconcileProgress(p, false) != nil {
		t.Fatal("stale progress")
	}
	got := currentReconcileProgress(p, true)
	if got == nil || got == p || *got != *p {
		t.Fatal(got)
	}
	p.Done = 41
	if currentReconcileProgress(p, true) != nil {
		t.Fatal("impossible progress")
	}
}
