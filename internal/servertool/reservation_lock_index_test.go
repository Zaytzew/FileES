package servertool

import (
	"strings"
	"testing"
)

func TestLockIndexStrictRecordsAndMultilineComments(t *testing.T) {
	const record = "Path: /Zdjęcia/100% @ ujęcie.jpg\nUUID Token: token-one\nOwner: client\nCreated: 2026-10-16 12:34:56 +0000 (Fri, 16 Oct 2026)\nExpires: \nComment (3 lines):\nPath: /not-a-record\n\nend\n\n"
	rows, err := parseLockIndex([]byte(record))
	if err != nil || len(rows) != 1 {
		t.Fatalf("parse = %v, %v", rows, err)
	}
	if rows[0].Path != "Zdjęcia/100% @ ujęcie.jpg" || rows[0].Comment != "Path: /not-a-record\n\nend" || rows[0].CreatedAt != "2026-10-16T12:34:56Z" {
		t.Fatal("record changed")
	}
	for name, bad := range map[string]string{
		"duplicate": record + record,
		"truncated": strings.TrimSuffix(record, "end\n\n"),
		"count":     strings.Replace(record, "3 lines", "300 lines", 1),
		"heading":   strings.Replace(record, "Owner:", "Właściciel:", 1),
		"date":      strings.Replace(record, "2026-10-16", "invalid-date", 1),
		"traversal": strings.Replace(record, "/Zdjęcia/", "/../", 1),
		"encoding":  record + string([]byte{0xff}),
		"suffix":    record + "incomplete next record",
		"cr-path":   strings.Replace(record, "ujęcie.jpg\n", "ujęcie.jpg\r\n", 1),
		"nul-path":  strings.Replace(record, "ujęcie.jpg", "ujęcie\x00.jpg", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseLockIndex([]byte(bad)); err == nil {
				t.Fatal("accepted invalid partial source")
			}
		})
	}
	if rows, err := parseLockIndex(nil); err != nil || len(rows) != 0 {
		t.Fatal("confirmed empty source refused")
	}
}

func TestLockIndexOutputBound(t *testing.T) {
	var out lockIndexOutput
	if _, err := out.Write(make([]byte, 8<<20)); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte{1}); err == nil {
		t.Fatal("bound bypassed")
	}
}
