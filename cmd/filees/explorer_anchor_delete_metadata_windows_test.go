//go:build windows && !nocfapi

package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestAnchorDeleteMetadataRequiresExplicitByteCounts(t *testing.T) {
	identity, _ := anchorRemoteIdentity("remote.bin", 19, 4)
	row := fmt.Sprintf(`{"ok":true,"directory":false,"placeholder":true,"partial":true,"in_sync":true,"local":false,"usn":"100","size":4,"on_disk":0,"modified":0,"identity":%q}`, identity)
	good := `{"schema":"filees.cfapi/v1","ok":true,"states":[` + row + `]}`
	s, err := decodeAnchorDeleteFileState([]byte(good))
	if err != nil || !anchorDeleteRemoteState(s, identity) {
		t.Fatal("fully remote partial placeholder refused", s, err)
	}
	for _, bad := range []string{
		strings.Replace(good, `"on_disk":0,`, "", 1), strings.Replace(good, `"modified":0,`, "", 1),
		strings.Replace(good, `"on_disk":0`, `"on_disk":null`, 1), strings.Replace(good, `"modified":0`, `"modified":-1`, 1),
		strings.Replace(good, `"states":[`+row+`]`, `"states":[]`, 1),
		strings.Replace(good, `"states":[`+row+`]`, `"states":[`+row+`,`+row+`]`, 1),
		strings.Replace(good, `"directory":false`, `"unknown":true,"directory":false`, 1), good + `{}`,
		strings.Repeat("x", 16*1024+1),
	} {
		if _, err := decodeAnchorDeleteFileState([]byte(bad)); err == nil {
			t.Fatal("accepted missing/ambiguous byte evidence")
		}
	}
	for _, bad := range []string{strings.Replace(good, `"on_disk":0`, `"on_disk":1`, 1), strings.Replace(good, `"modified":0`, `"modified":1`, 1), strings.Replace(good, `"directory":false`, `"directory":true`, 1)} {
		s, err := decodeAnchorDeleteFileState([]byte(bad))
		if err != nil {
			t.Fatal(err)
		}
		if anchorDeleteRemoteState(s, identity) {
			t.Fatal("resident/dirty/directory admitted")
		}
	}
}
