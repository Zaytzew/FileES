//go:build windows

package main

import (
	"strings"
	"testing"

	"filees/internal/predecessor"
)

func TestParseReplaceOptions(t *testing.T) {
	opts, err := parseReplaceOptions([]string{"--from", "msi", "--settings", "keep", "--config", `C:\Users\u\.filees\store\config.json`})
	if err != nil {
		t.Fatal(err)
	}
	if opts.from != predecessor.KindMSI || !opts.keep || opts.target != `C:\Users\u\.filees\store\config.json` {
		t.Fatalf("opts = %+v", opts)
	}
	opts, err = parseReplaceOptions([]string{"--from", "store", "--settings", "default", "--config", `C:\x\config.json`})
	if err != nil || opts.keep || opts.from != predecessor.KindStore {
		t.Fatalf("opts = %+v, err = %v", opts, err)
	}
	for _, args := range [][]string{
		{},
		{"--from", "msi", "--settings", "keep"},                                  // no target
		{"--from", "msi", "--settings", "keep", "--config", "config.json"},       // relative
		{"--from", "winget", "--settings", "keep", "--config", `C:\c.json`},      // unknown variant
		{"--from", "msi", "--settings", "maybe", "--config", `C:\c.json`},        // unknown choice
		{"--from", "msi", "--settings", "keep", "--config", `C:\c.json`, "more"}, // stray argument
	} {
		if _, err := parseReplaceOptions(args); err == nil {
			t.Fatalf("%q accepted", args)
		}
	}
}

// Each variant may remove only the other one. The decision rests on the
// running binary - package identity and the Store build marker - so no
// combination of arguments makes a variant uninstall itself.
func TestReplacementAllowedPairsEachVariantWithTheOther(t *testing.T) {
	cases := []struct {
		from     predecessor.Kind
		packaged bool
		mode     string
		allowed  bool
	}{
		{predecessor.KindMSI, true, "store", true},    // Store replaces MSI
		{predecessor.KindStore, false, "", true},      // MSI replaces Store
		{predecessor.KindStore, true, "store", false}, // Store must not remove itself
		{predecessor.KindMSI, false, "", false},       // MSI must not remove itself
		{predecessor.KindMSI, false, "store", false},  // unpackaged Store lab build
		{predecessor.KindStore, false, "store", false},
		{predecessor.KindMSI, true, "", false}, // an MSI binary repackaged by accident
	}
	for _, c := range cases {
		err := replacementAllowed(c.from, c.packaged, c.mode)
		if (err == nil) != c.allowed {
			t.Fatalf("from=%s packaged=%v mode=%q: err=%v, want allowed=%v", c.from, c.packaged, c.mode, err, c.allowed)
		}
		if err != nil && !strings.Contains(err.Error(), "only the") {
			t.Fatalf("refusal does not say which variant may do it: %v", err)
		}
	}
}
