package contracttests

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
)

// error.list carries the dictionary key so a reader sees the journal in their
// own language instead of the English log sentence.
//
// Lines written before the key was carried must stay readable: the field is
// optional on the wire and Msg remains their text. This is the step §7.4 of
// the error-catalogue contract describes, and it is an addition — the on-disk
// JSONL already had "key", it was simply dropped at the IPC boundary.
func TestErrorListCarriesTheMessageKeyAndStaysBackwardCompatible(t *testing.T) {
	wc := t.TempDir()
	logDir := filepath.Join(wc, ".filees", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	withKey := `{"ts":"2026-09-11T10:00:00Z","scope":"commit:projectA","code":"NET-4007","key":"net.unreachable","severity":"WARN","hint":"RETRY_BACKOFF","msg":"offline","details":"network"}`
	withoutKey := `{"ts":"2026-09-11T10:00:01Z","scope":"commit:projectA","code":"COMMIT-3100","severity":"ERROR","hint":"RETRY_LOCAL","msg":"Commit failed","details":"hook"}`
	if err := os.WriteFile(filepath.Join(logDir, "errors.jsonl"), []byte(withKey+"\n"+withoutKey+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sock := testSocketPath(t)
	server := ipcserver.New(sock)
	server.RegisterRepo("projectA", "svn://example/projectA", wc)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}

	cli := ipcclient.New(sock, "error-key-test")
	result, err := cli.ErrorList(context.Background(), contract.ErrorListPayload{RepoID: "projectA", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Errors) != 2 {
		t.Fatalf("errors = %#v", result.Errors)
	}
	byCode := map[string]contract.ErrorRecord{}
	for _, record := range result.Errors {
		byCode[record.Code] = record
	}
	if got := byCode["NET-4007"].MessageKey; got != "net.unreachable" {
		t.Errorf("message_key = %q, want the key the sink wrote", got)
	}
	if got := byCode["COMMIT-3100"].MessageKey; got != "" {
		t.Errorf("a line written without a key must not gain one: %q", got)
	}
	if got := byCode["COMMIT-3100"].Msg; got != "Commit failed" {
		t.Errorf("an older line lost its own text: %q", got)
	}
}
