package storage

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestBudgetsCombineDifferentRootsOnOneFilesystem(t *testing.T) {
	root := t.TempDir()
	requests := []Budget{{filepath.Join(root, "a"), 6 << 30}, {filepath.Join(root, "b"), 6 << 30}}
	probe := func(string) (string, int64, error) { return "same-device", 10 << 30, nil }
	volumes, err := inspectBudgets(requests, probe)
	if err != nil || len(volumes) != 1 {
		t.Fatalf("volumes=%+v err=%v", volumes, err)
	}
	if volumes[0].Required != 12<<30 || volumes[0].Check() == nil {
		t.Fatalf("combined capacity accepted: %+v", volumes)
	}
	if _, err := os.Stat(requests[0].Path); !os.IsNotExist(err) {
		t.Fatal("inspection created a directory")
	}
}

func TestBudgetsSeparateFilesystemsAndKeepConservativeObservation(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, p := range []string{a, b} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	probe := func(p string) (string, int64, error) { return filepath.Base(p), 8 << 30, nil }
	volumes, err := inspectBudgets([]Budget{{a, 6 << 30}, {b, 6 << 30}}, probe)
	if err != nil || len(volumes) != 2 {
		t.Fatalf("%+v %v", volumes, err)
	}
	for _, v := range volumes {
		if err := v.Check(); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	probe = func(string) (string, int64, error) { calls++; return "same", int64(10-calls) << 30, nil }
	volumes, err = inspectBudgets([]Budget{{a, 1}, {b, 1}}, probe)
	if err != nil || volumes[0].Available != 8<<30 {
		t.Fatalf("%+v %v", volumes, err)
	}
}

func TestBudgetsRejectOverflowProbeFailureAndFiles(t *testing.T) {
	root := t.TempDir()
	probe := func(string) (string, int64, error) { return "same", math.MaxInt64, nil }
	if _, err := inspectBudgets([]Budget{{root, math.MaxInt64}, {root, 1}}, probe); err == nil {
		t.Fatal("overflow accepted")
	}
	if _, err := inspectBudgets([]Budget{{root, -1}}, probe); err == nil {
		t.Fatal("negative budget accepted")
	}
	probe = func(string) (string, int64, error) { return "", 0, errors.New("denied") }
	if _, err := inspectBudgets([]Budget{{root, 1}}, probe); err == nil {
		t.Fatal("failed probe accepted")
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := budgetPath(filepath.Join(file, "child")); err == nil {
		t.Fatal("file ancestor accepted")
	}
}
