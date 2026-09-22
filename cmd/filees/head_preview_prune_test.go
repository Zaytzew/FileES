package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOldPreviewsGoAndFreshOnesStay(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	make := func(name string, age time.Duration, readOnly bool) string {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "brief.docx")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if readOnly {
			if err := os.Chmod(file, 0o444); err != nil {
				t.Fatal(err)
			}
		}
		stamp := now.Add(-age)
		if err := os.Chtimes(dir, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	old := make(previewPrefix+"old", 20*time.Hour, true)
	fresh := make(previewPrefix+"fresh", time.Minute, true)
	foreign := make("someone-elses-temp", 20*time.Hour, false)

	prunePreviews(root, previewLifetime, now)

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("yesterday's preview survived: %v", err)
	}
	for _, keep := range []string{fresh, foreign} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("%s was removed: %v", keep, err)
		}
	}
}

func TestPruningAMissingTempRootIsNotAFailure(t *testing.T) {
	prunePreviews(filepath.Join(t.TempDir(), "gone"), previewLifetime, time.Now())
}
