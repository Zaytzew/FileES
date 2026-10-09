package client

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func remoteDeleteFixture() RemoteFileDelete {
	return RemoteFileDelete{RootURL: "svn+ssh://user@example.test/repo", UUID: "f81d4fae-7dec-11d0-a765-00a0c91e6bf6",
		BaseRevision: 19, OperationID: "b3e94b92-41fc-4b5f-914e-6f4a291fd501", Source: "Dokumenty źródłowe/plik 新.txt", Message: "Usunięcie pliku"}
}

func TestRemoteFileDeleteValidationBeforeExecution(t *testing.T) {
	valid := remoteDeleteFixture()
	if err := ValidateRemoteFileDelete(valid); err != nil {
		t.Fatal(err)
	}
	c := New(Options{NativeSVNPath: filepath.Join(t.TempDir(), "absent.exe")}).(RemoteFileDeleter)
	for _, bad := range []string{"", ".", "..", "../escape", "/root", "a//b", "a/../b", "a\\b", "a:b", "x\x00", "x\r", "x\t", "x\x1f", "x.", "x ", ".SVN/x", "a/.FiLeEs/x", ".filees-native-probe/x", strings.Repeat("a/", 64) + "b", "bad\xff"} {
		m := valid
		m.Source = bad
		if _, err := c.RemoteFileDelete(context.Background(), m); err == nil || !strings.Contains(err.Error(), "relative path") {
			t.Fatalf("path %q reached helper: %v", bad, err)
		}
		if _, err := c.InspectRemoteFileDelete(context.Background(), m); err == nil || !strings.Contains(err.Error(), "relative path") {
			t.Fatalf("inspection %q reached helper: %v", bad, err)
		}
	}
	for _, mutate := range []func(*RemoteFileDelete){
		func(m *RemoteFileDelete) { m.BaseRevision = -1 },
		func(m *RemoteFileDelete) { m.BaseRevision = math.MaxInt32 + 1 },
		func(m *RemoteFileDelete) { m.OperationID = "" },
		func(m *RemoteFileDelete) { m.OperationID = strings.ToUpper(m.OperationID) },
		func(m *RemoteFileDelete) { m.UUID = "00000000-0000-0000-0000-000000000000" },
		func(m *RemoteFileDelete) { m.RootURL = "relative/repo" },
		func(m *RemoteFileDelete) { m.RootURL += "?unrelated" },
		func(m *RemoteFileDelete) { m.RootURL += "#fragment" },
		func(m *RemoteFileDelete) { m.Message = "" },
		func(m *RemoteFileDelete) { m.Message = "bad\x00" },
		func(m *RemoteFileDelete) { m.Message = strings.Repeat("x", 4097) },
	} {
		m := valid
		mutate(&m)
		if err := ValidateRemoteFileDelete(m); err == nil {
			t.Fatal("invalid binding accepted", m)
		}
	}
	if New(Options{}).(RemoteFileDeleter).RemoteFileDeletesEnabled() {
		t.Fatal("URL deletion enabled without native helper")
	}
}

func TestRemoteFileDeleteReceiptExactBinding(t *testing.T) {
	m := remoteDeleteFixture()
	raw := map[string]any{"revision": float64(20), "base_revision": float64(19), "mutation": "delete-file", "root_url": m.RootURL,
		"uuid": m.UUID, "operation_id": m.OperationID, "source": m.Source, "message": m.Message}
	if r, err := parseRemoteFileDeleteReceipt(raw, m); err != nil || r.Revision != 20 {
		t.Fatal(r, err)
	}
	for key, bad := range map[string]any{"revision": float64(19), "base_revision": float64(18), "root_url": "file:///other", "uuid": "other", "operation_id": "other", "source": "other", "message": "other", "mutation": "move"} {
		saved := raw[key]
		raw[key] = bad
		if _, err := parseRemoteFileDeleteReceipt(raw, m); err == nil {
			t.Fatal("unrelated receipt accepted", key)
		}
		raw[key] = saved
	}
	for _, bad := range []any{float64(20.5), math.NaN(), math.Inf(1), "20", float64(math.MaxInt32 + 1)} {
		raw["revision"] = bad
		if _, err := parseRemoteFileDeleteReceipt(raw, m); err == nil {
			t.Fatal("invalid revision accepted", bad)
		}
	}
	raw["revision"] = float64(20)
	raw["destination"] = ""
	if _, err := parseRemoteFileDeleteReceipt(raw, m); err == nil {
		t.Fatal("move receipt treated as deletion")
	}
}

func TestRemoteFileDeleteRecoveryRequiresExactSingleDelete(t *testing.T) {
	m := remoteDeleteFixture()
	build := func() []nativeLogEntry {
		data := map[string]any{"revision": 20, "message": m.Message, "revprops": map[string]string{"filees:commit-id": m.OperationID},
			"paths": []any{map[string]any{"path": "/" + m.Source, "action": "D"}}}
		b, _ := json.Marshal([]any{data})
		var entries []nativeLogEntry
		if err := json.Unmarshal(b, &entries); err != nil {
			t.Fatal(err)
		}
		return entries
	}
	if r, err := remoteFileDeleteReceiptFromLog(build(), m, 21); err != nil || r.Revision != 20 {
		t.Fatal(r, err)
	}
	for _, mutate := range []func([]nativeLogEntry){
		func(e []nativeLogEntry) { e[0].Paths = nil },
		func(e []nativeLogEntry) { e[0].Paths = append(e[0].Paths, e[0].Paths[0]) },
		func(e []nativeLogEntry) { e[0].Paths[0].Action = "M" },
		func(e []nativeLogEntry) { e[0].Paths[0].Path = "/other" },
		func(e []nativeLogEntry) { v := "/other"; e[0].Paths[0].CopyfromPath = &v },
		func(e []nativeLogEntry) { v := int64(19); e[0].Paths[0].CopyfromRev = &v },
		func(e []nativeLogEntry) { e[0].Message = "other" },
		func(e []nativeLogEntry) { e[0].Revision = 22 },
		func(e []nativeLogEntry) { e[0].Revision = 19 },
	} {
		e := build()
		mutate(e)
		if _, err := remoteFileDeleteReceiptFromLog(e, m, 21); err == nil {
			t.Fatal("mismatched history accepted", e)
		}
	}
	e := build()
	e[0].Revprops["filees:commit-id"] = "other"
	if r, err := remoteFileDeleteReceiptFromLog(e, m, 21); r != nil || err != nil {
		t.Fatal(r, err)
	}
	if r, err := remoteFileDeleteReceiptFromLog(nil, m, 19); r != nil || err != nil {
		t.Fatal(r, err)
	}
	for _, head := range []int64{18, math.MaxInt32 + 1} {
		if _, err := remoteFileDeleteReceiptFromLog(nil, m, head); err == nil {
			t.Fatal("invalid empty history range accepted", head)
		}
	}
	e = build()
	duplicate := build()[0]
	duplicate.Revision = 21
	if _, err := remoteFileDeleteReceiptFromLog(append([]nativeLogEntry{duplicate}, e...), m, 21); err == nil {
		t.Fatal("duplicate operation accepted")
	}
	if _, err := remoteFileDeleteReceiptFromLog(make([]nativeLogEntry, 4097), m, 5000); err == nil {
		t.Fatal("truncated history accepted")
	}
}
