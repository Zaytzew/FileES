//go:build !windows

package intake

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

type receiveProbe struct{ first bool }

func (p *receiveProbe) Read(b []byte) (int, error) {
	if !p.first {
		p.first = true
		return copy(b, "partial"), nil
	}
	fmt.Println("receiving") // previous bytes are already in the payload file
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0, io.EOF
}

func TestReceivingCrashProbe(t *testing.T) {
	base := os.Getenv("FILEES_RECEIVING_PROBE")
	if base == "" {
		return
	}
	s := Store{Root: base, MaxBytes: 16, MaxQuarantineBytes: 16}
	_, err := s.Accept(uuid.NewString(), "a", "s", testToken, "file", &receiveProbe{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReceivingKilledProcessReclaimsBudget(t *testing.T) {
	base := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestReceivingCrashProbe$")
	cmd.Env = append(os.Environ(), "FILEES_RECEIVING_PROBE="+base)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		in.Close()
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "receiving\n" {
		t.Fatalf("probe %q %v", line, err)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	var job string
	for _, e := range entries {
		if e.IsDir() {
			job = filepath.Join(base, e.Name())
		}
	}
	payload := filepath.Join(job, ".payload.tmp")
	old := time.Now().Add(-365 * 24 * time.Hour)
	if err := os.Chtimes(payload, old, old); err != nil {
		t.Fatal(err)
	}
	s := Store{Root: base, MaxBytes: 16, MaxQuarantineBytes: 16}
	if _, err := accept(t, s, uuid.NewString(), "next"); !errors.Is(err, ErrQuarantineFull) {
		t.Fatalf("active aged receiver lost reservation: %v", err)
	}
	if raw, err := os.ReadFile(payload); err != nil || string(raw) != "partial" {
		t.Fatalf("active payload %q %v", raw, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if _, err := accept(t, s, uuid.NewString(), "next"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(job); !os.IsNotExist(err) {
		t.Fatalf("orphan retained: %v", err)
	}
}

func TestReceivingKeepsPublishedAndUnknownJobs(t *testing.T) {
	for _, kind := range []string{"ready", "processing", "legacy", "unknown", "symlink", "partial"} {
		t.Run(kind, func(t *testing.T) {
			s := Store{Root: t.TempDir(), MaxBytes: 16}
			id := uuid.NewString()
			job := filepath.Join(s.Root, id)
			if kind == "ready" || kind == "processing" {
				r, err := accept(t, s, uuid.NewString(), "keep")
				if err != nil {
					t.Fatal(err)
				}
				id, job = r.UploadID, filepath.Join(s.Root, r.UploadID)
				if kind == "processing" {
					if err := s.Claim(id); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				if err := os.Mkdir(job, 0770); err != nil {
					t.Fatal(err)
				}
				if kind != "legacy" {
					lease, err := createReceivingLease(filepath.Join(job, receivingLockName))
					if err != nil {
						t.Fatal(err)
					}
					lease.Close()
				}
				if kind == "unknown" {
					if err := os.WriteFile(filepath.Join(job, "unknown"), []byte("keep"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "symlink" {
					outside := filepath.Join(t.TempDir(), "target")
					if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(outside, filepath.Join(job, payloadName)); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						raw, err := os.ReadFile(outside)
						if err != nil || string(raw) != "keep" {
							t.Errorf("symlink target changed")
						}
					})
				}
			}
			_, err := accept(t, s, uuid.NewString(), "next")
			unsafe := kind == "unknown" || kind == "symlink"
			if unsafe && !errors.Is(err, ErrBudgetState) {
				t.Fatalf("unsafe state: %v", err)
			}
			if !unsafe && err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(job)
			if kind == "partial" {
				if !os.IsNotExist(statErr) {
					t.Fatalf("partial not reaped: %v", statErr)
				}
			} else if statErr != nil {
				t.Fatalf("protected job lost: %v", statErr)
			}
		})
	}
}
