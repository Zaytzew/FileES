package client

import "testing"

func TestParseListXMLAcceptsImmediateChildren(t *testing.T) {
	raw := `<?xml version="1.0"?>
<lists>
<list path="svn+ssh://example/repo">
<entry kind="dir"><name>2026</name><commit revision="4"><author>a</author><date>2026-09-01T00:00:00.000000Z</date></commit></entry>
<entry kind="file"><name>readme.txt</name><size>12</size><commit revision="5"><author>a</author><date>2026-09-02T00:00:00.000000Z</date></commit></entry>
</list>
</lists>`
	got, err := parseListXML(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Kind != "dir" || got[0].Name != "2026" || got[0].Revision != 4 || got[1].Kind != "file" || got[1].Size != 12 {
		t.Fatalf("%#v", got)
	}
	if _, err := parseListXML(`<lists><list><entry kind="file"><name>a/b</name></entry></list></lists>`); err == nil {
		t.Fatal("nested name was accepted")
	}
}
