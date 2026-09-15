package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeFoldsDiacriticsAndRejectsPhrases(t *testing.T) {
	for in, want := range map[string]string{
		"Dupą":     "dupa",
		"merde":    "merde",
		"Scheiße":  "scheisse",
		"pu-ta":    "puta",
		"  NAZI  ": "nazi",
	} {
		got, ok := normalize(in)
		if !ok || got != want {
			t.Errorf("normalize(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := normalize("blow job"); ok {
		t.Error("a phrase must not become a fragment")
	}
}

func TestOnlySpellableEntriesSurvive(t *testing.T) {
	// kurwa (w), fuck (c) and shit (h) can never appear in a nick; listing
	// them would only make the file look complete.
	got := build([]string{"kurwa", "fuck", "shit", "merde", "puta"}, 3)
	want := []string{"merde", "puta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestShorterFragmentAbsorbsLongerEntries(t *testing.T) {
	got := build([]string{"dupa", "dupek", "dup", "porno", "porn", "ab"}, 3)
	want := []string{"dup", "porn"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestReadEntriesSkipsCommentsAndBlankLines(t *testing.T) {
	got, err := readEntries(strings.NewReader("# header\n\nmerde\n  # indented comment\nputa\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"merde", "puta"}) {
		t.Fatalf("got %v", got)
	}
}

func TestSampleNicksFollowTheConceptGrammar(t *testing.T) {
	for i := 0; i < 2000; i++ {
		nick := sampleNick()
		if len(nick) < 6 || len(nick) > 9 || !spellable(nick) {
			t.Fatalf("nick %q breaks the grammar", nick)
		}
	}
}

func TestRunWritesHeaderAndList(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "pl.txt")
	if err := os.WriteFile(src, []byte("dupa\nkurwa\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "nick-blocklist.txt")
	var stderr bytes.Buffer
	if err := run([]string{"-out", out, src}, &bytes.Buffer{}, &stderr); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := readEntries(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(entries, []string{"dupa"}) {
		t.Fatalf("list %v", entries)
	}
	if !strings.Contains(string(raw), "# Sources: pl.txt") {
		t.Fatalf("header missing sources:\n%s", raw)
	}
}

func TestFragmentsLongerThanANickAreDropped(t *testing.T) {
	// A nick has at most nine letters, so a ten-letter fragment could never
	// match and would only make the file look more thorough than it is.
	got := atMost([]string{"anal", "fellatio", "masturbate"}, 9)
	if !reflect.DeepEqual(got, []string{"anal", "fellatio"}) {
		t.Fatalf("got %v", got)
	}
}

func TestRunWritesAttributionAsASingleCommentLine(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "en.txt")
	if err := os.WriteFile(src, []byte("porn\nmasturbation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := run([]string{"-attribution", "Word lists: LDNOOBW\nCC-BY-4.0", src}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "# Attribution: Word lists: LDNOOBW CC-BY-4.0\n") {
		t.Fatalf("attribution line missing or split:\n%s", stdout.String())
	}
	entries, err := readEntries(&stdout)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(entries, []string{"porn"}) {
		t.Fatalf("list %v; attribution leaked or long fragment kept", entries)
	}
}
