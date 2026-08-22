package intake

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAcceptWritesRandomIDAndKeepsNameInMetadata(t *testing.T) {
	store := Store{Root: t.TempDir(), MaxBytes: 1024, Now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }}
	channelID := uuid.NewString()
	record, err := store.Accept(channelID, "atmprojekt", "oferta-a", strings.Repeat("a", 64), "Opinia Łódź.pdf", bytes.NewReader([]byte("payload-bytes")))
	if err != nil {
		t.Fatal(err)
	}
	if record.State != StateReady || record.Size != 13 || record.OriginalName != "Opinia Łódź.pdf" || record.ChannelID != channelID {
		t.Fatalf("record=%+v", record)
	}
	dir := filepath.Join(store.Root, record.UploadID)
	if _, err := os.Stat(filepath.Join(dir, "READY")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, payloadName))
	if err != nil || string(raw) != "payload-bytes" {
		t.Fatalf("payload=%q err=%v", raw, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(strings.ToLower(entry.Name()), "opinia") || strings.Contains(entry.Name(), "Łódź") || strings.Contains(entry.Name(), ".pdf") {
			t.Fatalf("original name leaked onto disk as %q", entry.Name())
		}
	}
	var onDisk Record
	meta, err := os.ReadFile(filepath.Join(dir, metaName))
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(meta, &onDisk) != nil || onDisk.OriginalName != record.OriginalName || onDisk.SHA256 != record.SHA256 {
		t.Fatalf("meta=%s", meta)
	}
}

func TestAcceptRejectsEmptyOversizeAndPathName(t *testing.T) {
	store := Store{Root: t.TempDir(), MaxBytes: 4}
	channelID := uuid.NewString()
	hash := strings.Repeat("ab", 32)
	if _, err := store.Accept(channelID, "atmprojekt", "oferta-a", hash, "ok.bin", bytes.NewReader(nil)); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty=%v", err)
	}
	if _, err := store.Accept(channelID, "atmprojekt", "oferta-a", hash, "ok.bin", bytes.NewReader([]byte("12345"))); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize=%v", err)
	}
	if _, err := store.Accept(channelID, "atmprojekt", "oferta-a", hash, "../x.bin", bytes.NewReader([]byte("12"))); !errors.Is(err, ErrName) {
		t.Fatalf("path name=%v", err)
	}
}
