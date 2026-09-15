// Command nick-blocklist builds the activation nick blocklist described in
// concepts/ACTIVATION_NICK_CONCEPT.md §4.1 from local word lists.
//
// It never touches the network. Feed it curated profanity lists (one entry
// per line) for the five GUI languages; it keeps only the entries a nick
// could actually contain, and drops every entry already covered by a shorter
// one, because the server matches fragments as substrings.
//
//	go run ./tools/nick-blocklist -out nick-blocklist.txt -sample 100000 lists/*.txt
package main

import (
	"bufio"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The nick alphabet. Letters read differently across PL/EN/DE/FR/ES
// (c j w v h y q x) are absent, so most vulgarities cannot occur in a nick at
// all and do not need listing.
const (
	consonants = "bdfgklmnprstz"
	vowels     = "aeiou"
	alphabet   = consonants + vowels
)

var clusters = []string{"bl", "br", "dr", "fl", "fr", "gl", "gr", "kl", "kr", "pl", "pr", "tr"}

const codas = "lmnrs"

// foldTable maps the diacritics of the five GUI languages onto ASCII. ß and
// æ/œ expand to two letters; anything else outside ASCII is left in place and
// then rejected by the alphabet check.
var foldTable = map[rune]string{
	'ą': "a", 'ć': "c", 'ę': "e", 'ł': "l", 'ń': "n", 'ó': "o", 'ś': "s", 'ź': "z", 'ż': "z",
	'ä': "a", 'ö': "o", 'ü': "u", 'ß': "ss",
	'à': "a", 'â': "a", 'æ': "ae", 'ç': "c", 'é': "e", 'è': "e", 'ê': "e", 'ë': "e",
	'î': "i", 'ï': "i", 'ô': "o", 'œ': "oe", 'ù': "u", 'û': "u", 'ÿ': "y",
	'á': "a", 'í': "i", 'ñ': "n", 'ú': "u",
}

// normalize lowercases, folds diacritics and removes hyphens and apostrophes
// inside a single word. It reports false for phrases: their parts are usually
// harmless on their own, and blocking them would only thin the nick space.
func normalize(entry string) (string, bool) {
	entry = strings.TrimSpace(strings.ToLower(entry))
	if entry == "" || strings.ContainsAny(entry, " \t") {
		return "", false
	}
	var b strings.Builder
	for _, r := range entry {
		switch {
		case r == '-' || r == '\'' || r == '’':
			continue
		case foldTable[r] != "":
			b.WriteString(foldTable[r])
		default:
			b.WriteRune(r)
		}
	}
	return b.String(), true
}

// spellable reports whether every letter of word belongs to the nick alphabet.
func spellable(word string) bool {
	if word == "" {
		return false
	}
	for _, r := range word {
		if !strings.ContainsRune(alphabet, r) {
			return false
		}
	}
	return true
}

// readEntries returns the non-comment, non-blank lines of r.
func readEntries(r io.Reader) ([]string, error) {
	var out []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, scanner.Err()
}

// build turns raw entries into the minimal sorted blocklist.
func build(entries []string, minLen int) []string {
	candidates := map[string]bool{}
	for _, entry := range entries {
		word, ok := normalize(entry)
		if !ok || len(word) < minLen || !spellable(word) {
			continue
		}
		candidates[word] = true
	}
	words := make([]string, 0, len(candidates))
	for w := range candidates {
		words = append(words, w)
	}
	// Shortest first, so a kept fragment can absorb every longer entry that
	// contains it.
	sort.Slice(words, func(i, j int) bool {
		if len(words[i]) != len(words[j]) {
			return len(words[i]) < len(words[j])
		}
		return words[i] < words[j]
	})
	var kept []string
	for _, w := range words {
		covered := false
		for _, k := range kept {
			if strings.Contains(w, k) {
				covered = true
				break
			}
		}
		if !covered {
			kept = append(kept, w)
		}
	}
	sort.Strings(kept)
	return kept
}

func blocked(nick string, list []string) bool {
	for _, fragment := range list {
		if strings.Contains(nick, fragment) {
			return true
		}
	}
	return false
}

func randIndex(n int) int {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic(err)
	}
	return int(v.Int64())
}

// sampleNick draws one nick with the grammar of the concept: three syllables,
// at most nine letters.
func sampleNick() string {
	for {
		var b strings.Builder
		for i := 0; i < 3; i++ {
			if randIndex(6) == 0 {
				b.WriteString(clusters[randIndex(len(clusters))])
			} else {
				b.WriteByte(consonants[randIndex(len(consonants))])
			}
			b.WriteByte(vowels[randIndex(len(vowels))])
			if randIndex(3) == 0 {
				b.WriteByte(codas[randIndex(len(codas))])
			}
		}
		if b.Len() <= 9 {
			return b.String()
		}
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("nick-blocklist", flag.ContinueOnError)
	fs.SetOutput(stderr)
	outPath := fs.String("out", "", "write the blocklist here instead of standard output")
	minLen := fs.Int("min", 3, "shortest fragment kept")
	sample := fs.Int("sample", 0, "draw this many nicks and report the share the list would block")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: nick-blocklist [-out file] [-min 3] [-sample N] list.txt...")
	}
	var entries []string
	var sources []string
	for _, path := range fs.Args() {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		lines, err := readEntries(f)
		f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		entries = append(entries, lines...)
		sources = append(sources, filepath.Base(path))
	}
	list := build(entries, *minLen)

	var b strings.Builder
	b.WriteString("# FileES activation nick blocklist — generated by tools/nick-blocklist\n")
	b.WriteString("# One lowercase fragment per line, matched as a substring of a nick.\n")
	b.WriteString("# Alphabet: " + alphabet + "; shortest fragment: " + fmt.Sprint(*minLen) + "\n")
	b.WriteString("# Sources: " + strings.Join(sources, ", ") + "\n")
	for _, w := range list {
		b.WriteString(w + "\n")
	}
	if *outPath == "" {
		if _, err := io.WriteString(stdout, b.String()); err != nil {
			return err
		}
	} else if err := os.WriteFile(*outPath, []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "entries read: %d, fragments kept: %d\n", len(entries), len(list))

	if *sample > 0 {
		hits := 0
		for i := 0; i < *sample; i++ {
			if blocked(sampleNick(), list) {
				hits++
			}
		}
		fmt.Fprintf(stderr, "sample: %d nicks, %d blocked (%.2f%%)\n", *sample, hits, float64(hits)*100/float64(*sample))
	}
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
