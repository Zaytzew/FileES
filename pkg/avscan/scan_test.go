package avscan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestCommandTreatsEICARAsInfectedEvenWhenHelperExitsZero(t *testing.T) {
	helper := "/usr/bin/true"
	if _, err := os.Stat(helper); err != nil {
		t.Skip("no /usr/bin/true")
	}
	path := filepath.Join(t.TempDir(), "eicar.com")
	if err := os.WriteFile(path, []byte(EICAR), 0600); err != nil {
		t.Fatal(err)
	}
	verdict, detail, err := (Command{Path: helper}).Scan(context.Background(), path)
	if err != nil || verdict != Infected || detail != EICARSignature {
		t.Fatalf("verdict=%v detail=%q err=%v", verdict, detail, err)
	}
}

func TestCommandLeavesNonEICARToHelperExit(t *testing.T) {
	helper := "/usr/bin/true"
	if _, err := os.Stat(helper); err != nil {
		t.Skip("no /usr/bin/true")
	}
	path := filepath.Join(t.TempDir(), "ok.pdf")
	if err := os.WriteFile(path, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	verdict, _, err := (Command{Path: helper}).Scan(context.Background(), path)
	if err != nil || verdict != Clean {
		t.Fatalf("verdict=%v err=%v", verdict, err)
	}
}
