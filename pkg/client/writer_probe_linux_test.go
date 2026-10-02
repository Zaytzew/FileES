//go:build linux

package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriterProbeRetriesOnlyTransientLaunchFailure(t *testing.T) {
	for _, tc := range []struct {
		name, probe string
		wantErr     bool
		probes      int
	}{
		{"transient launch", `if [ "$n" -lt 3 ]; then exit 126; fi`, false, 3},
		{"persistent launch", `exit 126`, true, 3},
		{"normal failure", `exit 1`, true, 1},
		{"SVN refusal", `printf '%s' '{"schema":"filees.native-svn/v1","ok":false,"errors":[{"code":200004,"message":"refused"}]}'; exit 126`, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			helper := filepath.Join(root, "helper")
			t.Setenv("FILEES_TEST_PROBE_DIR", root)
			body := `#!/bin/sh
set -eu
cd "$FILEES_TEST_PROBE_DIR"
printf '%s\n' "$1" >> trace
if [ "$1" = --version ]; then
 n=0; if [ -f count ]; then n=$(sed -n 1p count); fi
 n=$((n+1)); printf '%s\n' "$n" > count
 ` + tc.probe + `
 printf '%s' '{"schema":"filees.native-svn/v1","ok":true,"features":["writer_inspect_v1"]}'
elif [ "$1" = writer-inspect ]; then
 printf '%s' '{"schema":"filees.native-svn/v1","ok":true,"commit_id":""}'
else
 exit 99
fi
`
			if err := os.WriteFile(helper, []byte(body), 0755); err != nil {
				t.Fatal(err)
			}
			c := &execClient{nativeSVNPath: helper, timeout: time.Second}
			id, err := c.InspectCommitWriter(t.Context(), root)
			if (err != nil) != tc.wantErr || id != "" {
				t.Fatalf("id=%q err=%v", id, err)
			}
			trace, err := os.ReadFile(filepath.Join(root, "trace"))
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Repeat("--version\n", tc.probes)
			if !tc.wantErr {
				want += "writer-inspect\n"
			}
			if string(trace) != want {
				t.Fatalf("unexpected retry/mutation: %s", trace)
			}
		})
	}
}

func TestWriterProbeCancellationIsNeverRetried(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	c := &execClient{nativeSVNPath: filepath.Join(t.TempDir(), "absent"), timeout: time.Second}
	_, err := c.InspectCommitWriter(ctx, t.TempDir())
	if !errors.Is(err, context.Canceled) || retryWriterProbeLaunch(err) {
		t.Fatalf("cancellation lost or considered retryable: %v", err)
	}
}
