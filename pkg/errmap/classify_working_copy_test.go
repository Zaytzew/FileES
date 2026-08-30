package errmap

import (
	"errors"
	"testing"

	"filees/pkg/errcat"
)

func TestClassifyWorkingCopyAdministrativeLockIsNotForeignPassport(t *testing.T) {
	err := errors.New("svn: E155004: Run 'svn cleanup' to remove locks\nsvn: E155004: Working copy '/wc/project' locked.\nsvn: E155004: '/wc/project' is already locked.")
	got := Classify(err)
	if got.Key != errcat.KeyWorkingCopyBusy || got.Code != errcat.CodeWCBusy || got.Severity != SevWarn || got.Hint != HintRetryLocal {
		t.Fatalf("Classify(working copy lock) = %+v", got)
	}
}

func TestClassifyRepositoryLockStillMeansForeignPassport(t *testing.T) {
	got := Classify(errors.New("file 'plan.dwg' is already locked by another user"))
	if got.Key != errcat.KeyLockHeldByOther || got.Code != errcat.CodeLockHeld {
		t.Fatalf("Classify(repository lock) = %+v", got)
	}
}
