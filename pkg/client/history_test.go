package client

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoryRequiresAHelper(t *testing.T) {
	c := New(Options{}).(HistoryReader)
	if c.HistoryEnabled() {
		t.Fatal("history enabled without a helper")
	}
	if _, err := c.HistoryList(t.Context(), "file:///lab", 1); !errors.Is(err, errHistoryUnavailable) {
		t.Fatalf("list without helper: %v", err)
	}
	if _, err := c.HistoryFetchFile(t.Context(), "file:///lab/a.txt", 1, filepath.Join(t.TempDir(), "a.txt")); !errors.Is(err, errHistoryUnavailable) {
		t.Fatalf("fetch without helper: %v", err)
	}
}

func TestHistoryListParsesEntriesAndNamesTheRevision(t *testing.T) {
	c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"revision":5,"entries":[`+
		`{"name":"01_EDITABLES","kind":"dir","size":null,"last_changed_revision":4,"last_changed_date":"2026-09-14T11:03:08.000000Z","last_author":"399c0801"},`+
		`{"name":"opis.docx","kind":"file","size":17,"last_changed_revision":2,"last_changed_date":null,"last_author":null}]}`)
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("FILEES_TEST_RA_TRACE", trace)

	entries, err := c.HistoryList(t.Context(), "file:///lab/5_BALUSTRADY", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	dir, file := entries[0], entries[1]
	if dir.Kind != "dir" || dir.Size != -1 || dir.LastChangedRevision != 4 || dir.LastAuthor != "399c0801" || dir.LastChangedDate == "" {
		t.Fatalf("dir = %+v", dir)
	}
	if file.Kind != "file" || file.Size != 17 || file.LastChangedRevision != 2 || file.LastAuthor != "" || file.LastChangedDate != "" {
		t.Fatalf("file = %+v", file)
	}
	argv, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), `"list","--url","file:///lab/5_BALUSTRADY","--revision","5"`) {
		t.Fatalf("list argv = %s", argv)
	}
}

// A daemon later joins these names onto a destination folder, so anything
// that could become a path escape or a silent overwrite is refused here.
func TestHistoryListRejectsUnsafeReceipts(t *testing.T) {
	entry := func(body string) string {
		return `{"schema":"filees.native-svn/v1","ok":true,"revision":5,"entries":[` + body + `]}`
	}
	for name, reply := range map[string]string{
		"other revision":        `{"schema":"filees.native-svn/v1","ok":true,"revision":4,"entries":[]}`,
		"no entries":            `{"schema":"filees.native-svn/v1","ok":true,"revision":5}`,
		"separator in name":     entry(`{"name":"a/b","kind":"file","size":1,"last_changed_revision":1}`),
		"backslash in name":     entry(`{"name":"a\\b","kind":"file","size":1,"last_changed_revision":1}`),
		"dot dot":               entry(`{"name":"..","kind":"dir","size":null,"last_changed_revision":1}`),
		"empty name":            entry(`{"name":"","kind":"file","size":1,"last_changed_revision":1}`),
		"duplicate name":        entry(`{"name":"x","kind":"file","size":1,"last_changed_revision":1},{"name":"x","kind":"file","size":1,"last_changed_revision":1}`),
		"directory with size":   entry(`{"name":"d","kind":"dir","size":3,"last_changed_revision":1}`),
		"unknown kind":          entry(`{"name":"l","kind":"symlink","size":1,"last_changed_revision":1}`),
		"changed after listing": entry(`{"name":"f","kind":"file","size":1,"last_changed_revision":6}`),
		"negative size":         entry(`{"name":"f","kind":"file","size":-2,"last_changed_revision":1}`),
		"author not a string":   entry(`{"name":"f","kind":"file","size":1,"last_changed_revision":1,"last_author":7}`),
	} {
		t.Run(name, func(t *testing.T) {
			c := raFake(t, reply)
			if entries, err := c.HistoryList(t.Context(), "file:///lab", 5); err == nil {
				t.Fatalf("accepted %s: %+v", name, entries)
			}
		})
	}
}

func TestHistoryRefusesBadArgumentsBeforeTheHelperRuns(t *testing.T) {
	c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"revision":1,"entries":[]}`)
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("FILEES_TEST_RA_TRACE", trace)
	taken := filepath.Join(t.TempDir(), "taken.txt")
	if err := os.WriteFile(taken, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := c.HistoryList(t.Context(), "file:///lab", -1); err == nil {
		t.Fatal("negative revision accepted")
	}
	if _, err := c.HistoryList(t.Context(), filepath.Join(t.TempDir(), "wc"), 1); err == nil {
		t.Fatal("local path accepted as URL")
	}
	if _, err := c.HistoryFetchFile(t.Context(), "file:///lab/a.txt", 1, "relative.txt"); err == nil {
		t.Fatal("relative output accepted")
	}
	if _, err := c.HistoryFetchFile(t.Context(), "file:///lab/a.txt", 1, taken); err == nil {
		t.Fatal("existing output accepted")
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatalf("helper ran for a refused request (err=%v)", err)
	}
	if body, _ := os.ReadFile(taken); string(body) != "keep\n" {
		t.Fatalf("existing output touched: %q", body)
	}
}

func TestHistoryRefusesAHelperWithoutTheFeatures(t *testing.T) {
	c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"revision":1,"entries":[]}`)
	t.Setenv("FILEES_TEST_RA_OLD_HELPER", "1")
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("FILEES_TEST_RA_TRACE", trace)
	if _, err := c.HistoryList(t.Context(), "file:///lab", 1); err == nil {
		t.Fatal("old helper accepted for list")
	}
	if _, err := c.HistoryFetchFile(t.Context(), "file:///lab/a.txt", 1, filepath.Join(t.TempDir(), "a.txt")); err == nil {
		t.Fatal("old helper accepted for fetch-file")
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatalf("history verb ran on an old helper (err=%v)", err)
	}
}

func TestHistoryFetchFileChecksTheReceiptAgainstTheFile(t *testing.T) {
	body := "repository bytes\n"
	t.Run("matching receipt", func(t *testing.T) {
		c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"bytes":17,"revision":3}`)
		t.Setenv("FILEES_TEST_RA_FILE", body)
		out := filepath.Join(t.TempDir(), "copy.txt")
		n, err := c.HistoryFetchFile(t.Context(), "file:///lab/a.txt", 3, out)
		if err != nil || n != int64(len(body)) {
			t.Fatalf("n=%d err=%v", n, err)
		}
	})
	for name, reply := range map[string]string{
		"size differs":     `{"schema":"filees.native-svn/v1","ok":true,"bytes":99,"revision":3}`,
		"revision differs": `{"schema":"filees.native-svn/v1","ok":true,"bytes":17,"revision":2}`,
		"no byte count":    `{"schema":"filees.native-svn/v1","ok":true,"revision":3}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := raFake(t, reply)
			t.Setenv("FILEES_TEST_RA_FILE", body)
			if _, err := c.HistoryFetchFile(t.Context(), "file:///lab/a.txt", 3, filepath.Join(t.TempDir(), "copy.txt")); err == nil {
				t.Fatalf("accepted a receipt where %s", name)
			}
		})
	}
	t.Run("helper wrote nothing", func(t *testing.T) {
		c := raFake(t, `{"schema":"filees.native-svn/v1","ok":true,"bytes":17,"revision":3}`)
		if _, err := c.HistoryFetchFile(t.Context(), "file:///lab/a.txt", 3, filepath.Join(t.TempDir(), "copy.txt")); err == nil {
			t.Fatal("accepted a receipt for a file that does not exist")
		}
	})
}
