package commit

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"filees/pkg/errmap"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func TestOfflineJournalRetainsLatestCauseAndResetsAfterRecovery(t *testing.T) {
	var output lockedBuffer
	s := &Service{ErrSink: errmap.NewSink(&output, "commit:test"), connectivityJournalDelay: time.Hour}
	t.Cleanup(s.cancelConnectivityJournal)
	s.goOffline(errors.New("poll HEAD revision: E170013: SSH connection refused"))
	s.goOffline(errors.New("poll HEAD revision: connection timed out"))
	s.recordSustainedOffline(s.offlineJournalGeneration)
	var row struct {
		Details string `json:"details"`
	}
	if e := json.Unmarshal([]byte(output.String()), &row); e != nil {
		t.Fatal(e)
	}
	if row.Details != "poll HEAD revision: connection timed out" {
		t.Fatal(row)
	}
	s.goOnline()
	if s.offlineCause != "" {
		t.Fatal("cause survives recovery")
	}
	s.goOffline(errors.New(strings.Repeat("x", 10000)))
	if len(s.offlineCause) > 8220 || !strings.HasSuffix(s.offlineCause, " [truncated]") {
		t.Fatal("unbounded details")
	}
}

func (buffer *lockedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.b.Write(data)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.b.String()
}

func (buffer *lockedBuffer) Len() int {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.b.Len()
}

func TestTransientOfflineDoesNotEnterStructuredJournal(t *testing.T) {
	var output lockedBuffer
	service := &Service{
		ErrSink:                  errmap.NewSink(&output, "commit:test"),
		connectivityJournalDelay: 25 * time.Millisecond,
	}
	service.goOffline(nil)
	service.goOnline()
	t.Cleanup(service.cancelConnectivityJournal)
	time.Sleep(60 * time.Millisecond)
	if output.Len() != 0 {
		t.Fatalf("transient disconnect reached journal: %s", output.String())
	}
}

func TestSustainedOfflineEntersStructuredJournalOnce(t *testing.T) {
	var output lockedBuffer
	service := &Service{
		ErrSink:                  errmap.NewSink(&output, "commit:test"),
		connectivityJournalDelay: 15 * time.Millisecond,
	}
	t.Cleanup(service.cancelConnectivityJournal)
	service.goOffline(nil)
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(output.String(), "NET-4007") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := strings.Count(output.String(), "NET-4007"); got != 1 {
		t.Fatalf("structured connectivity entries=%d, journal=%q", got, output.String())
	}
	service.goOffline(nil)
	time.Sleep(30 * time.Millisecond)
	if got := strings.Count(output.String(), "NET-4007"); got != 1 {
		t.Fatalf("repeated offline transition entries=%d, journal=%q", got, output.String())
	}
}
