package client

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNativeOutputIsBoundedWithoutReaderFromBypass(t *testing.T) {
	var b nativeOutput
	if _, ok := any(&b).(io.ReaderFrom); ok {
		t.Fatal("promoted ReaderFrom bypasses bound")
	}
	n, err := io.Copy(&b, strings.NewReader(strings.Repeat("x", 100000)))
	if err != nil || n != 100000 || b.buffer.Len() != nativeReceiptLimit || !b.truncated {
		t.Fatalf("n=%d err=%v size=%d truncated=%v", n, err, b.buffer.Len(), b.truncated)
	}
	var listing nativeOutput
	listing.max = nativeListingLimit
	n, err = io.Copy(&listing, strings.NewReader(strings.Repeat("y", 200000)))
	if err != nil || n != 200000 || listing.buffer.Len() != 200000 || listing.truncated {
		t.Fatalf("listing n=%d err=%v size=%d truncated=%v", n, err, listing.buffer.Len(), listing.truncated)
	}
}

func TestMissingPathLockNeedsBothMatchingTokensAndComment(t *testing.T) {
	good := `<status><target><entry><wc-status item="missing"><lock><token>T</token><comment>request</comment></lock></wc-status><repos-status><lock><token>T</token><comment>request</comment></lock></repos-status></entry></target></status>`
	if !missingPathLockConfirmed(good, "request") {
		t.Fatal("matching possession rejected")
	}
	for _, bad := range []string{strings.Replace(good, "<token>T</token>", "<token>other</token>", 1), strings.Replace(good, "<comment>request</comment>", "<comment>foreign</comment>", 1), strings.Replace(good, `item="missing"`, `item="normal"`, 1), `<status><target><entry><wc-status item="missing"><lock><token>T</token><comment>request</comment></lock></wc-status></entry></target></status>`} {
		if missingPathLockConfirmed(bad, "request") {
			t.Fatalf("unproven lock accepted: %s", bad)
		}
	}
}

func TestNativeReceiptsAndFailureAreNotSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process fixture; not native Windows acceptance")
	}
	for _, tc := range []struct {
		name, payload string
		ok            bool
	}{
		{"scheduled", `{"schema":"filees.native-svn/v1","ok":true,"state":"scheduled"}`, true},
		{"replay", `{"schema":"filees.native-svn/v1","ok":true,"state":"already_scheduled"}`, true},
		{"wrong-schema", `{"schema":"filees.native-svn/v0","ok":true,"state":"scheduled"}`, false},
		{"missing-state", `{"schema":"filees.native-svn/v1","ok":true}`, false},
		{"refused", `{"schema":"filees.native-svn/v1","ok":false}`, false},
		{"raw-debug", "unknown native error", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wc := t.TempDir()
			binary := filepath.Join(wc, "native")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' '"+tc.payload+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			c := New(Options{NativeSVNPath: binary}).(MetadataMover)
			if !c.MetadataMovesEnabled() {
				t.Fatal("opt-in ignored")
			}
			_, err := c.RecordMove(context.Background(), wc, "old.txt", "new.txt")
			if (err == nil) != tc.ok {
				t.Fatalf("unexpected receipt result: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := c.RecordMove(ctx, wc, "old.txt", "new.txt"); err == nil {
				t.Fatal("cancelled operation succeeded")
			}
			if _, err := c.RecordMove(context.Background(), wc, "../old", "new"); err == nil {
				t.Fatal("escaped WC")
			}
		})
	}
	if New(Options{}).(MetadataMover).MetadataMovesEnabled() {
		t.Fatal("native runtime enabled implicitly")
	}
}
