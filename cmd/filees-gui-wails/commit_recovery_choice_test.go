package main

import (
	"testing"

	"filees/internal/gui/actions"
	contract "filees/pkg/contract/v1"
)

// The controller repeats the daemon's recovery choice names because it may
// not import the contract; this pins both spellings together.
func TestCommitRecoveryChoicesMatchTheContract(t *testing.T) {
	if actions.CommitRecoveryChoiceRetryQueue != contract.CommitRecoveryRetryQueue || actions.CommitRecoveryChoiceServerCopy != contract.CommitRecoveryServerCopy {
		t.Fatal("controller commit recovery choices drifted from the daemon contract")
	}
}
