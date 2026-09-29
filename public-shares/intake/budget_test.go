package intake

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var testToken = strings.Repeat("a", 64)

func accept(t *testing.T, store Store, channelID string, body string) (Record, error) {
	t.Helper()
	return store.Accept(channelID, "atmprojekt", "oferta-a", testToken, "plik.pdf", bytes.NewReader([]byte(body)))
}

func TestIntakeLimitsAreOffUnlessSet(t *testing.T) {
	store := Store{Root: t.TempDir(), MaxBytes: 16}
	channelID := uuid.NewString()
	for range 20 {
		if _, err := accept(t, store, channelID, "payload"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIntakePendingUploadsPerChannel(t *testing.T) {
	store := Store{Root: t.TempDir(), MaxBytes: 16, MaxUploadsPerChannel: 2}
	full, other := uuid.NewString(), uuid.NewString()
	first, err := accept(t, store, full, "one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accept(t, store, full, "two"); err != nil {
		t.Fatal(err)
	}
	if _, err := accept(t, store, full, "three"); !errors.Is(err, ErrChannelFull) {
		t.Fatalf("third upload: %v", err)
	}
	if _, err := accept(t, store, other, "elsewhere"); err != nil {
		t.Fatalf("one full channel blocked another: %v", err)
	}
	if err := store.Claim(first.UploadID); err != nil { // the reaper still holds it
		t.Fatal(err)
	}
	if _, err := accept(t, store, full, "three"); !errors.Is(err, ErrChannelFull) {
		t.Fatalf("PROCESSING stopped counting: %v", err)
	}
	if err := store.Remove(first.UploadID); err != nil { // reaped
		t.Fatal(err)
	}
	if _, err := accept(t, store, full, "three"); err != nil {
		t.Fatalf("reaped upload still counted: %v", err)
	}
}

func TestIntakeQuarantineSizeCountsWorstCaseForArrivingUploads(t *testing.T) {
	store := Store{Root: t.TempDir(), MaxBytes: 10, MaxQuarantineBytes: 25}
	channelID := uuid.NewString()
	for i := range 4 { // held 0,5,10,15 + reservation 10 <= 25
		if _, err := accept(t, store, channelID, "12345"); err != nil {
			t.Fatalf("upload %d: %v", i+1, err)
		}
	}
	if _, err := accept(t, store, channelID, "12345"); !errors.Is(err, ErrQuarantineFull) {
		t.Fatalf("20 held + 10 reserved > 25 accepted: %v", err)
	}
}

// A parallel upload must see the one still streaming: admission and
// reservation happen together, before any byte is read.
func TestIntakeReservationCoversUploadInFlight(t *testing.T) {
	store := Store{Root: t.TempDir(), MaxBytes: 16, MaxUploadsPerChannel: 1}
	channelID := uuid.NewString()
	reader, writer := io.Pipe()
	t.Cleanup(func() { reader.Close(); writer.Close() })
	done := make(chan error, 1)
	go func() {
		_, err := store.Accept(channelID, "atmprojekt", "oferta-a", testToken, "slow.bin", reader)
		done <- err
	}()
	if _, err := writer.Write([]byte("partial")); err != nil { // the upload is now streaming
		t.Fatal(err)
	}
	if _, err := accept(t, store, channelID, "second"); !errors.Is(err, ErrChannelFull) {
		t.Fatalf("in-flight upload not reserved: %v", err)
	}
	writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestIntakeAbandonedReservationCountsUntilRemoval(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	store := Store{Root: t.TempDir(), MaxBytes: 16, MaxUploadsPerChannel: 1, Now: func() time.Time { return now }}
	channelID := uuid.NewString()
	// A crash left a reservation behind long ago.
	abandonedID := uuid.NewString()
	abandoned := filepath.Join(store.Root, abandonedID)
	if err := os.Mkdir(abandoned, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(reservation{ChannelID: channelID, Bytes: 16, At: now.Add(-7 * 24 * time.Hour)})
	if err := os.WriteFile(filepath.Join(abandoned, reservationName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(abandoned, ".payload.tmp"), []byte("abandoned"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := accept(t, store, channelID, "fresh"); !errors.Is(err, ErrChannelFull) {
		t.Fatalf("old reservation disappeared from usage: %v", err)
	}
	// A restart and clock advance do not release still-present bytes.
	now = now.Add(24 * time.Hour)
	other := store
	if _, err := accept(t, other, channelID, "fresh"); !errors.Is(err, ErrChannelFull) {
		t.Fatal(err)
	}
	if err := store.Remove(abandonedID); err != nil {
		t.Fatal(err)
	}
	record, err := accept(t, other, channelID, "fresh")
	if err != nil {
		t.Fatalf("removed reservation still counted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, record.UploadID, reservationName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reservation survived a completed upload: %v", err)
	}
}

func TestIntakeBudgetRejectsUnknownOrCorruptUsage(t *testing.T) {
	for _, kind := range []string{"missing", "json", "negative", "wrong-channel", "oversized", "size-mismatch", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			store := Store{Root: t.TempDir(), MaxBytes: 16}
			id := uuid.NewString()
			record, err := accept(t, store, id, "payload")
			if err != nil {
				t.Fatal(err)
			}
			meta := filepath.Join(store.Root, record.UploadID, metaName)
			switch kind {
			case "missing":
				err = os.Remove(meta)
			case "json":
				err = os.WriteFile(meta, []byte("{"), 0600)
			case "negative", "wrong-channel", "size-mismatch":
				if kind == "negative" {
					record.Size = -1
				} else if kind == "wrong-channel" {
					record.ChannelID = "bad"
				} else {
					record.Size++
				}
				raw, _ := json.Marshal(record)
				err = os.WriteFile(meta, raw, 0600)
			case "oversized":
				err = os.WriteFile(meta, []byte(strings.Repeat(" ", maxBudgetMetadata+1)), 0600)
			case "symlink":
				if runtime.GOOS == "windows" {
					t.Skip("Unix symlink fixture")
				}
				outside := filepath.Join(t.TempDir(), "outside")
				if err = os.WriteFile(outside, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(meta); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(outside, meta)
			}
			if err != nil {
				t.Fatal(err)
			}
			store.MaxQuarantineBytes = 100
			if _, err := accept(t, store, id, "next"); !errors.Is(err, ErrBudgetState) {
				t.Fatalf("unknown usage accepted: %v", err)
			}
		})
	}
}

func TestIntakeBudgetIncludesLegacyAndChecksOverflow(t *testing.T) {
	store := Store{Root: filepath.Join(t.TempDir(), "new-intake"), MaxBytes: 16}
	id := uuid.NewString()
	if _, err := accept(t, store, id, "legacy"); err != nil {
		t.Fatal(err)
	}
	store.MaxQuarantineBytes = 21 // 6 existing + 16 reserved cannot fit
	if _, err := accept(t, store, id, "new"); !errors.Is(err, ErrQuarantineFull) {
		t.Fatal(err)
	}
	for range 2 {
		dir := filepath.Join(store.Root, uuid.NewString())
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(reservation{ChannelID: id, Bytes: math.MaxInt64, At: time.Now()})
		if err := os.WriteFile(filepath.Join(dir, reservationName), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	store.MaxQuarantineBytes = math.MaxInt64
	if _, err := accept(t, store, id, "new"); !errors.Is(err, ErrBudgetState) {
		t.Fatalf("overflow accepted: %v", err)
	}
}

func TestIntakeSharedBudgetLockMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix modes")
	}
	store := Store{Root: t.TempDir(), MaxBytes: 16, MaxUploadsPerChannel: 1}
	record, err := accept(t, store, uuid.NewString(), "file")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(store.Root, budgetLockName))
	if err != nil || info.Mode().Perm() != jobFilePerm {
		t.Fatalf("lock mode: %v %v", info, err)
	}
	// Reaper instances have no admission limits but must share the same lock.
	reaper := Store{Root: store.Root}
	if err := reaper.Claim(record.UploadID); err != nil {
		t.Fatal(err)
	}
	if err := reaper.Release(record.UploadID); err != nil {
		t.Fatal(err)
	}
	if err := reaper.Remove(record.UploadID); err != nil {
		t.Fatal(err)
	}
}

func TestIntakeFailedUploadReleasesItsReservation(t *testing.T) {
	store := Store{Root: t.TempDir(), MaxBytes: 4, MaxUploadsPerChannel: 1}
	channelID := uuid.NewString()
	if _, err := accept(t, store, channelID, "too large"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize: %v", err)
	}
	if _, err := accept(t, store, channelID, "ok"); err != nil {
		t.Fatalf("failed upload kept its slot: %v", err)
	}
}

func TestIntakeBudgetAdmissionAcrossProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("production Unix flock")
	}
	if root := os.Getenv("FILEES_INTAKE_BUDGET_TEST_ROOT"); root != "" {
		store := Store{Root: root, MaxBytes: 16, MaxUploadsPerChannel: 3, MaxQuarantineBytes: 48}
		_, err := accept(t, store, os.Getenv("FILEES_INTAKE_BUDGET_TEST_CHANNEL"), "payload")
		if err != nil && !errors.Is(err, ErrChannelFull) && !errors.Is(err, ErrQuarantineFull) {
			t.Fatal(err)
		}
		return
	}
	root, channel := t.TempDir(), uuid.NewString()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 8)
	outputs := make([]bytes.Buffer, len(commands))
	for i := range commands {
		cmd := exec.Command(exe, "-test.run=^TestIntakeBudgetAdmissionAcrossProcesses$", "-test.timeout=30s")
		cmd.Env = append(os.Environ(), "FILEES_INTAKE_BUDGET_TEST_ROOT="+root, "FILEES_INTAKE_BUDGET_TEST_CHANNEL="+channel)
		cmd.Stdout, cmd.Stderr = &outputs[i], &outputs[i]
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands[i] = cmd
	}
	for i, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Errorf("process %d: %v: %s", i, err, &outputs[i])
		}
	}
	store := Store{Root: root}
	records, err := store.ListReady()
	if err != nil || len(records) != 3 {
		t.Fatalf("admitted %d instead of 3: %v", len(records), err)
	}
}

func TestIntakeBudgetRejectsNoncanonicalChannelWithoutReserving(t *testing.T) {
	store := Store{Root: t.TempDir(), MaxBytes: 16, MaxUploadsPerChannel: 1}
	if _, err := accept(t, store, "{"+uuid.NewString()+"}", "data"); !errors.Is(err, ErrIncomplete) {
		t.Fatal(err)
	}
	if _, err := accept(t, store, uuid.NewString(), "data"); err != nil {
		t.Fatal(err)
	}
	store.Root = filepath.Join(t.TempDir(), "missing")
	if err := store.Remove(uuid.NewString()); err != nil {
		t.Fatalf("removing absent job must remain idempotent: %v", err)
	}
}
