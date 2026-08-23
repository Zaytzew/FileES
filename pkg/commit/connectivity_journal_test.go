package commit

import (
	"bytes"
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
	service.goOffline()
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
	service.goOffline()
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(output.String(), "NET-4007") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := strings.Count(output.String(), "NET-4007"); got != 1 {
		t.Fatalf("structured connectivity entries=%d, journal=%q", got, output.String())
	}
	service.goOffline()
	time.Sleep(30 * time.Millisecond)
	if got := strings.Count(output.String(), "NET-4007"); got != 1 {
		t.Fatalf("repeated offline transition entries=%d, journal=%q", got, output.String())
	}
}
