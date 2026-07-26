package manifest

import (
	"path/filepath"
	"testing"
)

func TestParseChannelDefaultManifest(t *testing.T) {
	ch, err := ParseChannel([]byte(`{"schema_version":1,"release_id":"v0.9","sequence":7,"security_epoch":1}`))
	if err != nil {
		t.Fatal(err)
	}
	got := ExpandPlatform(ch.Manifest, "openbsd-amd64")
	want := "releases/v0.9/openbsd-amd64/manifest.json"
	if got != want {
		t.Fatalf("manifest path = %q, want %q", got, want)
	}
}

func TestParseSkipsUTF8BOM(t *testing.T) {
	withBOM := append([]byte{0xEF, 0xBB, 0xBF},
		[]byte(`{"schema_version":1,"release_id":"v0.9","platform":"openbsd-amd64","sequence":7,"security_epoch":1,"files":[{"source":"bin/filees-admin","target":"{sbin_dir}/filees-admin","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`)...)
	m, err := Parse(withBOM)
	if err != nil {
		t.Fatalf("Parse with BOM: %v", err)
	}
	if m.ReleaseID != "v0.9" {
		t.Fatalf("release_id = %q", m.ReleaseID)
	}
}

func TestParseRejectsUnknownFieldsSchemaAndBadDigest(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"schema_version":2,"release_id":"v1","platform":"openbsd-amd64","sequence":7,"security_epoch":1,"files":[{"source":"bin/x","target":"{sbin_dir}/x","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`),
		[]byte(`{"schema_version":1,"release_id":"v1","platform":"openbsd-amd64","sequence":7,"security_epoch":1,"unknown":true,"files":[{"source":"bin/x","target":"{sbin_dir}/x","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}`),
		[]byte(`{"schema_version":1,"release_id":"v1","platform":"openbsd-amd64","sequence":7,"security_epoch":1,"files":[{"source":"bin/x","target":"{sbin_dir}/x","sha256":"deadbeef"}]}`),
		[]byte(`{"schema_version":1,"release_id":"v1","platform":"openbsd-amd64","sequence":7,"security_epoch":1,"files":[{"source":"bin/x","target":"{sbin_dir}/x","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]} {}`),
	}
	for i, data := range cases {
		if _, err := Parse(data); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	if _, err := ParseChannel([]byte(`{"schema_version":1,"release_id":"v1","sequence":7,"security_epoch":1,"extra":true}`)); err == nil {
		t.Fatal("channel unknown field accepted")
	}
}

func TestResolveTarget(t *testing.T) {
	dirs := Dirs{SbinDir: "/usr/local/sbin", LibexecDir: "/usr/local/libexec"}
	cases := []struct{ in, want string }{
		{"{sbin_dir}/filees-admin", "/usr/local/sbin/filees-admin"},
		{"{libexec_dir}/filees/filees-onboard", "/usr/local/libexec/filees/filees-onboard"},
	}
	for _, c := range cases {
		got := ResolveTarget(dirs, c.in)
		if got != filepath.Clean(c.want) {
			t.Fatalf("ResolveTarget(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestManifestSourcePath(t *testing.T) {
	m := &Manifest{BasePath: "releases/v0.9/openbsd-amd64"}
	got := m.SourcePath("bin/filees-admin")
	want := "releases/v0.9/openbsd-amd64/bin/filees-admin"
	if got != want {
		t.Fatalf("source path = %q, want %q", got, want)
	}
}
