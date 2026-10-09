package client

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func treeMoveFixture() RemoteTreeMove {
	return RemoteTreeMove{RootURL: "svn+ssh://user@example.test/repo", UUID: "f81d4fae-7dec-11d0-a765-00a0c91e6bf6",
		BaseRevision: 19, OperationID: "b3e94b92-41fc-4b5f-914e-6f4a291fd501", Source: "Dokumenty źródłowe",
		Destination: "01_PROJEKT/Dokumenty źródłowe", Message: "Przeniesienie folderu"}
}

func TestRemoteTreeBindingValidationBeforeExecution(t *testing.T) {
	valid := treeMoveFixture()
	if err := validateRemoteTreeMove(valid); err != nil {
		t.Fatal(err)
	}
	client := New(Options{NativeSVNPath: filepath.Join(t.TempDir(), "absent.exe")}).(RemoteTreeMover)
	for _, field := range []string{"Source", "Destination"} {
		for _, bad := range []string{"", ".", "..", "../escape", "/root", "a//b", "a/../b", "a\\b", "a:b", "x\x00", "x\r", "x\t", "x\x1f", "x.", "x ", ".SVN/x", "a/.FiLeEs/x", ".filees-native-probe/x", strings.Repeat("a/", 64) + "b", "bad\xff"} {
			t.Run(field+"/"+bad, func(t *testing.T) {
				m := valid
				if field == "Source" {
					m.Source = bad
				} else {
					m.Destination = bad
				}
				if _, err := client.RemoteTreeMove(context.Background(), m); err == nil || !strings.Contains(err.Error(), "relative paths") {
					t.Fatalf("did not reject before absent helper: %v", err)
				}
			})
		}
	}
	for _, pair := range [][2]string{{"A", "a"}, {"a", "a/b"}, {"a/b", "A"}} {
		m := valid
		m.Source, m.Destination = pair[0], pair[1]
		if err := validateRemoteTreeMove(m); err == nil {
			t.Fatal("overlap accepted", pair)
		}
	}
	for _, mutate := range []func(*RemoteTreeMove){
		func(m *RemoteTreeMove) { m.BaseRevision = -1 },
		func(m *RemoteTreeMove) { m.BaseRevision = 9007199254740991 },
		func(m *RemoteTreeMove) { m.BaseRevision = 2147483648 },
		func(m *RemoteTreeMove) { m.OperationID = "" },
		func(m *RemoteTreeMove) { m.UUID = "00000000-0000-0000-0000-000000000000" },
		func(m *RemoteTreeMove) { m.RootURL += "?unrelated" },
		func(m *RemoteTreeMove) { m.RootURL += "#fragment" },
		func(m *RemoteTreeMove) { m.Message = "" },
		func(m *RemoteTreeMove) { m.Message = "bad\x00" },
	} {
		m := valid
		mutate(&m)
		if err := validateRemoteTreeMove(m); err == nil {
			t.Fatal("invalid binding accepted", m)
		}
	}
}

func TestRemoteTreeReceiptExactBinding(t *testing.T) {
	m := treeMoveFixture()
	raw := map[string]any{"revision": float64(20), "base_revision": float64(19), "root_url": m.RootURL,
		"uuid": m.UUID, "operation_id": m.OperationID, "source": m.Source, "destination": m.Destination}
	if r, err := parseRemoteTreeReceipt(raw, m); err != nil || r.Revision != 20 {
		t.Fatal(r, err)
	}
	for key, bad := range map[string]any{"revision": float64(19), "base_revision": float64(18), "root_url": "file:///other", "uuid": "other", "operation_id": "other", "source": "other", "destination": "other"} {
		saved := raw[key]
		raw[key] = bad
		if _, err := parseRemoteTreeReceipt(raw, m); err == nil {
			t.Fatal("receipt accepted unrelated field", key)
		}
		raw[key] = saved
	}
}

func TestRemoteTreeRecoveredReceiptNotJustMarker(t *testing.T) {
	m := treeMoveFixture()
	build := func() []nativeLogEntry {
		data := map[string]any{"revision": 20, "message": m.Message, "revprops": map[string]string{"filees:commit-id": m.OperationID},
			"paths": []any{map[string]any{"path": "/" + m.Source, "action": "D"},
				map[string]any{"path": "/" + m.Destination, "action": "A", "copyfrom_path": "/" + m.Source, "copyfrom_rev": 19}}}
		b, _ := json.Marshal([]any{data})
		var entries []nativeLogEntry
		if err := json.Unmarshal(b, &entries); err != nil {
			t.Fatal(err)
		}
		return entries
	}
	if receipt, err := remoteTreeReceiptFromLog(build(), m, 21); err != nil || receipt.Revision != 20 {
		t.Fatal(receipt, err)
	}
	for _, mutate := range []func([]nativeLogEntry){
		func(e []nativeLogEntry) { e[0].Paths = e[0].Paths[:1] },
		func(e []nativeLogEntry) { e[0].Paths[1].CopyfromRev = nil },
		func(e []nativeLogEntry) { n := int64(18); e[0].Paths[1].CopyfromRev = &n },
		func(e []nativeLogEntry) { e[0].Paths[1].Path = "/other" },
		func(e []nativeLogEntry) { e[0].Message = "other" },
		func(e []nativeLogEntry) { e[0].Revision = 22 },
	} {
		e := build()
		mutate(e)
		if _, err := remoteTreeReceiptFromLog(e, m, 21); err == nil {
			t.Fatal("mismatched history accepted", e)
		}
	}
	e := build()
	e[0].Revprops["filees:commit-id"] = "other"
	if r, err := remoteTreeReceiptFromLog(e, m, 21); r != nil || err != nil {
		t.Fatal(r, err)
	}
	e = build()
	duplicate := build()[0]
	duplicate.Revision = 21
	if _, err := remoteTreeReceiptFromLog(append([]nativeLogEntry{duplicate}, e...), m, 21); err == nil {
		t.Fatal("duplicate operation accepted")
	}
	if _, err := remoteTreeReceiptFromLog(make([]nativeLogEntry, 4097), m, 5000); err == nil {
		t.Fatal("truncated history accepted")
	}
}
