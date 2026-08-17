package client

import "testing"

func TestParseLogXML(t *testing.T) {
	const raw = `<?xml version="1.0" encoding="UTF-8"?>
<log>
<logentry revision="18">
<author>acme</author>
<date>2026-08-17T10:00:00.000000Z</date>
<msg>[!shout@#!] Ważna paka</msg>
</logentry>
<logentry revision="19">
<msg>Auto-commit by FileES client x: 2 paths</msg>
</logentry>
</log>`
	got, err := parseLogXML(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Revision != 18 || got[0].Message != "[!shout@#!] Ważna paka" || got[1].Revision != 19 {
		t.Fatalf("%#v", got)
	}
}
