package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
	helperOut, daemonIn := io.Pipe()
	daemonOut, helperIn := io.Pipe()
	bridge := &anchorBridge{materialize: materialize}
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
