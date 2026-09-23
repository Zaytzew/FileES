package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"filees/pkg/talk"
)

// The helper is played by two pipes: this side of the bridge has no idea
// whether a process is on the other end, which is exactly the point.
type fakeHelper struct {
	toDaemon   *io.PipeWriter
	fromDaemon *bufio.Scanner
	closeOut   func()
}

func runBridge(t *testing.T, materialize func(ctx context.Context, path string) (string, error)) (*fakeHelper, chan error) {
	t.Helper()
	return runAnchorBridge(t, &anchorBridge{materialize: func(ctx context.Context, path string) (string, int64, error) {
		file, err := materialize(ctx, path)
		return file, 7, err
	}})
}

func runAnchorBridge(t *testing.T, bridge *anchorBridge) (*fakeHelper, chan error) {
	t.Helper()
	helperOut, daemonIn := io.Pipe()
	daemonOut, helperIn := io.Pipe()
	done := make(chan error, 1)
	// stopped is separate from done so a test may read the error itself
	// without the cleanup then waiting for a second one that never comes.
	stopped := make(chan struct{})
	go func() {
		done <- bridge.serve(context.Background(), &anchorHelperStreams{stdout: helperOut, stdin: helperIn})
		close(stopped)
	}()
	t.Cleanup(func() {
		_ = daemonIn.Close()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("the bridge did not stop when the helper did")
		}
	})
	return &fakeHelper{toDaemon: daemonIn, fromDaemon: bufio.NewScanner(daemonOut), closeOut: func() { _ = daemonIn.Close() }}, done
}

func (helper *fakeHelper) fetch(t *testing.T, id, identity string) {
	t.Helper()
	if _, err := io.WriteString(helper.toDaemon, "fetch\t"+id+"\t0\t4096\t"+identity+"\n"); err != nil {
		t.Fatal(err)
	}
}

func (helper *fakeHelper) answer(t *testing.T) []string {
	t.Helper()
	if !helper.fromDaemon.Scan() {
		t.Fatal("the daemon answered nothing")
	}
	return strings.Split(helper.fromDaemon.Text(), "\t")
}

func TestOpeningAPlaceholderIsAnsweredWithTheMaterializedFile(t *testing.T) {
	wanted := filepath.Join(t.TempDir(), "sala.dwg")
	helper, _ := runBridge(t, func(_ context.Context, path string) (string, error) {
		if path != "projekt/sala.dwg" {
			return "", errors.New("unexpected path " + path)
		}
		return wanted, nil
	})
	// The helper's first line is its own; it must not be read as a request.
	if _, err := io.WriteString(helper.toDaemon, "{\"schema\":\"filees.cfapi/v1\",\"ok\":true}\n"); err != nil {
		t.Fatal(err)
	}
	helper.fetch(t, "7", "projekt/sala.dwg")

	answer := helper.answer(t)
	if len(answer) != 3 || answer[0] != "ok" || answer[1] != "7" || answer[2] != wanted {
		t.Fatalf("answer = %q", answer)
	}
}

func TestAFetchThatCannotBeMaterializedIsRefusedInOneLine(t *testing.T) {
	helper, _ := runBridge(t, func(context.Context, string) (string, error) {
		return "", errors.New("nie ma\ttakiej\nścieżki")
	})
	helper.fetch(t, "1", "projekt/brak.dwg")
	answer := helper.answer(t)
	if len(answer) != 3 || answer[0] != "err" || answer[1] != "1" {
		t.Fatalf("answer = %q", answer)
	}
	// Tabs and newlines in a reason would split into fields the helper reads
	// as another answer.
	if strings.ContainsAny(answer[2], "\t\n") {
		t.Fatalf("reason breaks the protocol: %q", answer[2])
	}
}

func TestARelativeAnswerIsNeverSentToTheShell(t *testing.T) {
	helper, _ := runBridge(t, func(context.Context, string) (string, error) {
		return "sala.dwg", nil
	})
	helper.fetch(t, "2", "projekt/sala.dwg")
	if answer := helper.answer(t); answer[0] != "err" {
		t.Fatalf("a relative path was handed to the shell: %q", answer)
	}
}

func TestASlowFetchDoesNotHoldUpTheNextOne(t *testing.T) {
	slow := make(chan struct{})
	helper, _ := runBridge(t, func(_ context.Context, path string) (string, error) {
		if path == "wolno" {
			<-slow
		}
		return filepath.Join(t.TempDir(), "plik"), nil
	})
	helper.fetch(t, "1", "wolno")
	helper.fetch(t, "2", "szybko")

	// The quick one must come back while the slow one is still working: a
	// blocked reading loop is what makes Explorer wait out the filter timeout.
	if answer := helper.answer(t); answer[1] != "2" {
		t.Fatalf("the slow fetch blocked the reading loop: %q", answer)
	}
	close(slow)
	if answer := helper.answer(t); answer[1] != "1" {
		t.Fatalf("second answer = %q", answer)
	}
}

func TestAMalformedRequestIsRefusedRatherThanGuessedAt(t *testing.T) {
	var asked sync.WaitGroup
	helper, _ := runBridge(t, func(context.Context, string) (string, error) {
		asked.Done()
		return filepath.Join(t.TempDir(), "plik"), nil
	})
	for _, line := range []string{
		"fetch\t3\tnie-liczba\t4096\tprojekt/a.dwg\n",
		"fetch\t4\t0\t4096\n",
		"fetch\t5\t-1\t4096\tprojekt/a.dwg\n",
	} {
		if _, err := io.WriteString(helper.toDaemon, line); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"3", "5"} {
		answer := helper.answer(t)
		if answer[0] != "err" || answer[1] != id {
			t.Fatalf("answer = %q", answer)
		}
	}
	// The line without an identity has no id field to answer either, so it is
	// dropped; the next good request must still work.
	asked.Add(1)
	helper.fetch(t, "6", "projekt/a.dwg")
	if answer := helper.answer(t); answer[0] != "ok" || answer[1] != "6" {
		t.Fatalf("answer = %q", answer)
	}
	asked.Wait()
}

func TestTheBridgeStopsWhenTheHelperDoes(t *testing.T) {
	helper, done := runBridge(t, func(context.Context, string) (string, error) {
		return filepath.Join(t.TempDir(), "plik"), nil
	})
	helper.closeOut()
	select {
	case err := <-done:
		if !errors.Is(err, errAnchorHelperGone) {
			t.Fatalf("serve() = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve() kept running after the helper ended")
	}
}

func TestAWholeFileIsAdoptedAtItsRevisionAndThenReverted(t *testing.T) {
	root := t.TempDir()
	adopted := make(chan string, 1)
	helper, _ := runAnchorBridge(t, &anchorBridge{
		root: root,
		materialize: func(context.Context, string) (string, int64, error) {
			return filepath.Join(t.TempDir(), "sala.dwg"), 41, nil
		},
		adopt: func(_ context.Context, identity string, revision int64) error {
			adopted <- identity + "@" + strconv.FormatInt(revision, 10)
			return nil
		},
	})
	helper.fetch(t, "3", "projekt/sala.dwg")
	if answer := helper.answer(t); answer[0] != "ok" {
		t.Fatalf("fetch = %q", answer)
	}
	// Nothing is adopted before the helper says the file is whole: Subversion
	// reading a partial placeholder would ask for it again.
	select {
	case got := <-adopted:
		t.Fatalf("adopted before hydration: %s", got)
	case <-time.After(50 * time.Millisecond):
	}
	io.WriteString(helper.toDaemon, "hydrated\t3\tprojekt/sala.dwg\n")
	if got := <-adopted; got != "projekt/sala.dwg@41" {
		t.Fatalf("adopted %s, want the revision the bytes were read at", got)
	}
	answer := helper.answer(t)
	want := filepath.Join(root, "projekt", "sala.dwg")
	if len(answer) != 3 || answer[0] != "revert" || answer[1] != "3" || answer[2] != want {
		t.Fatalf("revert = %q, want %s", answer, want)
	}
}

func TestAFileHeldByAnApplicationIsRevertedLater(t *testing.T) {
	helper, _ := runAnchorBridge(t, &anchorBridge{
		root:        t.TempDir(),
		revertRetry: 10 * time.Millisecond,
		materialize: func(context.Context, string) (string, int64, error) {
			return filepath.Join(t.TempDir(), "sala.dwg"), 1, nil
		},
		adopt: func(context.Context, string, int64) error { return nil },
	})
	helper.fetch(t, "1", "sala.dwg")
	helper.answer(t)
	io.WriteString(helper.toDaemon, "hydrated\t1\tsala.dwg\n")
	if answer := helper.answer(t); answer[0] != "revert" {
		t.Fatalf("first revert = %q", answer)
	}
	io.WriteString(helper.toDaemon, "reverted\t1\t"+hresultInUse+"\n")
	if answer := helper.answer(t); answer[0] != "revert" || answer[1] != "1" {
		t.Fatalf("a file in use must be asked for again, got %q", answer)
	}
	// Done ends the retries: no third revert.
	io.WriteString(helper.toDaemon, "reverted\t1\t"+hresultOK+"\n")
	io.WriteString(helper.toDaemon, "fetch\t2\t0\t10\tinny.dwg\tacad.exe\n")
	if answer := helper.answer(t); answer[0] != "ok" || answer[1] != "2" {
		t.Fatalf("after a finished revert the next line must be the next answer, got %q", answer)
	}
}

func TestOneProgramCannotDownloadAWholeAnchor(t *testing.T) {
	guard := newAnchorGuard(talk.Logger{})
	now := time.Now()
	scanner := `C:\Program Files\Skaner\scan.exe`
	for i := 0; i < guard.limit; i++ {
		if err := guard.allow(scanner, "plik-"+strconv.Itoa(i), now); err != nil {
			t.Fatalf("file %d refused below the limit: %v", i, err)
		}
	}
	if err := guard.allow(scanner, "jeszcze-jeden", now); err == nil {
		t.Fatal("past the limit the program must be refused")
	}
	if err := guard.allow(scanner, "plik-0", now.Add(2*guard.window)); err == nil {
		t.Fatal("a refused program stays refused for the anchor's session")
	}
	// Another program - the person's CAD - is unaffected.
	if err := guard.allow(`C:\CAD\acad.exe`, "sala.dwg", now); err != nil {
		t.Fatalf("another program was refused: %v", err)
	}
	// Opening the same drawing again and again is one file, not many.
	cad := newAnchorGuard(talk.Logger{})
	for i := 0; i < 100; i++ {
		if err := cad.allow(`C:\CAD\acad.exe`, "sala.dwg", now); err != nil {
			t.Fatalf("reopening one file tripped the guard: %v", err)
		}
	}
}

func TestTheGuardAnswersInsteadOfFetching(t *testing.T) {
	asked := 0
	bridge := &anchorBridge{
		guard: &anchorGuard{limit: 1, window: time.Minute},
		materialize: func(context.Context, string) (string, int64, error) {
			asked++
			return filepath.Join(t.TempDir(), "plik"), 1, nil
		},
	}
	helper, _ := runAnchorBridge(t, bridge)
	io.WriteString(helper.toDaemon, "fetch\t1\t0\t10\ta.dwg\tscan.exe\n")
	helper.answer(t)
	io.WriteString(helper.toDaemon, "fetch\t2\t0\t10\tb.dwg\tscan.exe\n")
	if answer := helper.answer(t); answer[0] != "err" || answer[1] != "2" {
		t.Fatalf("the guard must refuse the second file, got %q", answer)
	}
	if asked != 1 {
		t.Fatalf("a refused request must not reach Subversion, materialized %d times", asked)
	}
}
