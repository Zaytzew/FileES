package avscan

import (
	"context"
	"errors"
	"testing"
)

func TestStaticReportsCleanInfectedAndUnavailable(t *testing.T) {
	clean, _, err := (Static{Verdict: Clean}).Scan(context.Background(), "/tmp/x")
	if err != nil || clean != Clean {
		t.Fatalf("clean=%v err=%v", clean, err)
	}
	infected, detail, err := (Static{Verdict: Infected, Detail: "eicar"}).Scan(context.Background(), "/tmp/x")
	if err != nil || infected != Infected || detail != "eicar" {
		t.Fatalf("infected=%v detail=%q err=%v", infected, detail, err)
	}
	_, _, err = (Static{Verdict: Unavailable}).Scan(context.Background(), "/tmp/x")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable err=%v", err)
	}
}

func TestCommandWithoutAbsolutePathIsUnavailable(t *testing.T) {
	_, _, err := (Command{Path: "clamscan"}).Scan(context.Background(), "/tmp/x")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
}
