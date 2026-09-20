package svnrotate

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGenerationSwapProcessDeath(t *testing.T) {
	if !rotationSupported() {
		t.Skip("requires advisory lock")
	}
	requireSVNTools(t)
	if phase := os.Getenv("FILEES_TEST_SWAP_PHASE"); phase != "" {
		swapCheckpoint = func(at string) {
			if at == phase {
				os.Exit(75)
			}
		}
		repo, archive := os.Getenv("FILEES_TEST_SWAP_REPO"), os.Getenv("FILEES_TEST_SWAP_ARCHIVE")
		var err error
		switch os.Getenv("FILEES_TEST_SWAP_ACTION") {
		case "load":
			_, err = LoadGeneration(testLoadConfig(repo, archive), bytes.NewReader(testDump("restored\n")), "operation", io.Discard)
		case "rotate":
			cfg := testConfig(repo, archive)
			cfg.DumpDepth = 1
			err = Rotate(cfg, "operation", io.Discard)
		case "recover":
			_, _, err = Recover(repo, archive, "operation")
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Fatal("checkpoint not reached")
	}
	for _, action := range []string{"load", "rotate"} {
		for _, phase := range []string{"prepared", "archived", "installed", "artifacts", "metadata", "frozen", "cleaned"} {
			t.Run(action+"/"+phase, func(t *testing.T) {
				root := t.TempDir()
				repo := buildTestRepo(t, root, "original\n")
				archive := filepath.Join(root, "archive")
				old, _ := repoUUID(repo)
				killSwap(t, action, phase, repo, archive)
				if _, found, err := Recover(repo, archive, "another-operation"); !found || err == nil {
					t.Fatal("foreign operation resumed swap")
				}
				// Also die during recovery after the first archive rename.
				if phase == "archived" {
					killSwap(t, "recover", "installed", repo, archive)
				}
				meta, found, err := Recover(repo, archive, "operation")
				if err != nil || !found {
					t.Fatalf("recovery: %+v %v %v", meta, found, err)
				}
				newID, _ := repoUUID(repo)
				archivedID, _ := repoUUID(meta.ArchiveDir)
				if meta.OldUUID != old || archivedID != old || newID != meta.NewUUID || old == newID {
					t.Fatalf("UUIDs: %+v hot=%s archive=%s", meta, newID, archivedID)
				}
				want := "original\n"
				if action == "load" {
					want = "restored\n"
				}
				if got := svnlookOut(t, "cat", repo, "docs/a.bin"); got != want {
					t.Fatalf("content=%q", got)
				}
				if err := verify(repo); err != nil {
					t.Fatal(err)
				}
				if err := verify(meta.ArchiveDir); err != nil {
					t.Fatal(err)
				}
				if raw, err := os.ReadFile(filepath.Join(meta.ArchiveDir, "hooks", "pre-commit")); err != nil || string(raw) != blockHookBody {
					t.Fatal("archive is not fenced", err)
				}
				for _, file := range []string{meta.Tag + ".log.xml", meta.Tag + ".meta.json", meta.Tag + ".svn/FROZEN"} {
					if _, err := os.Stat(filepath.Join(archive, file)); err != nil {
						t.Fatal(err)
					}
				}
				if _, found, err := Recover(repo, archive, "operation"); err != nil || found {
					t.Fatal("finished swap replayed", err)
				}
				dirs, _ := filepath.Glob(filepath.Join(archive, "*.svn"))
				if len(dirs) != 1 {
					t.Fatalf("duplicate archives: %v", dirs)
				}
				work, _ := filepath.Glob(filepath.Join(archive, ".*-work-*"))
				if len(work) != 0 {
					t.Fatalf("staging leaked: %v", work)
				}
			})
		}
	}
}

func killSwap(t *testing.T, action, phase, repo, archive string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestGenerationSwapProcessDeath$")
	cmd.Env = append(os.Environ(), "FILEES_TEST_SWAP_ACTION="+action, "FILEES_TEST_SWAP_PHASE="+phase, "FILEES_TEST_SWAP_REPO="+repo, "FILEES_TEST_SWAP_ARCHIVE="+archive)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 75 {
		t.Fatalf("child: %v %s", err, out)
	}
}

func TestSwapRecoveryRefusesChangedEvidence(t *testing.T) {
	if !rotationSupported() {
		t.Skip("requires advisory lock")
	}
	requireSVNTools(t)
	for _, damage := range []string{"uuid", "artifact", "staging-symlink"} {
		t.Run(damage, func(t *testing.T) {
			root := t.TempDir()
			repo := buildTestRepo(t, root, "original\n")
			archive := filepath.Join(root, "archive")
			before, _ := repoUUID(repo)
			killSwap(t, "load", "prepared", repo, archive)
			stages, _ := filepath.Glob(filepath.Join(archive, ".load-work-*"))
			if len(stages) != 1 {
				t.Fatal(stages)
			}
			stage := stages[0]
			switch damage {
			case "uuid":
				if err := os.WriteFile(filepath.Join(stage, "new.svn", "db", "uuid"), []byte("foreign-generation\n"), 0644); err != nil {
					t.Fatal(err)
				}
			case "artifact":
				files, _ := filepath.Glob(filepath.Join(stage, "*.log.xml"))
				if err := os.WriteFile(files[0], []byte("changed"), 0644); err != nil {
					t.Fatal(err)
				}
			case "staging-symlink":
				moved := stage + "-moved"
				if err := os.Rename(stage, moved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(moved, stage); err != nil {
					t.Fatal(err)
				}
			}
			if _, found, err := Recover(repo, archive, "operation"); !found || err == nil {
				t.Fatal("changed evidence accepted")
			}
			after, _ := repoUUID(repo)
			if after != before {
				t.Fatal("refusal changed hot generation")
			}
			dirs, _ := filepath.Glob(filepath.Join(archive, "*.svn"))
			if len(dirs) != 0 {
				t.Fatal("refusal archived hot generation")
			}
		})
	}
}
