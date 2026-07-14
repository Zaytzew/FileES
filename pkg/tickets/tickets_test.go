package tickets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateNoticeWritesPrivateTicketAndSanitizesTitle(t *testing.T) {
	wc := t.TempDir()
	path, err := New().CreateNotice(wc, "client-id", "line one\nline two", "body")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != filepath.Join(wc, ".filees", "tickets") {
		t.Fatalf("ticket path = %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"TYPE=NOTICE", "CLIENT=client-id", "TITLE=line oneline two", "body"} {
		if !strings.Contains(text, want) {
			t.Fatalf("ticket missing %q:\n%s", want, text)
		}
	}
}
