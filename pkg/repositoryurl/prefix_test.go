package repositoryurl

import "testing"

func TestValidatePrefixKeepsTransportPortOutOfCanonicalURL(t *testing.T) {
	for _, tc := range []struct {
		prefix string
		ok     bool
	}{
		{prefix: "svn+ssh://_filees-data@spot.example.net/", ok: true},
		{prefix: "svn+ssh://_filees-client@example.net/repos/", ok: true},
		{prefix: "svn+ssh://_filees-data@spot.example.net:2223/"},
		{prefix: "svn+ssh://root@example.net/"},
		{prefix: "https://example.net/"},
		{prefix: "svn+ssh://_filees-data@example.net"},
	} {
		if err := ValidatePrefix(tc.prefix); (err == nil) != tc.ok {
			t.Fatalf("ValidatePrefix(%q) error = %v, ok=%v", tc.prefix, err, tc.ok)
		}
	}
}

func TestBuildRejectsRepositoryIDSyntax(t *testing.T) {
	const prefix = "svn+ssh://_filees-data@example.net/"
	if got, err := Build(prefix, "repo-id"); err != nil || got != prefix+"repo-id" {
		t.Fatalf("Build() = %q, %v", got, err)
	}
	for _, bad := range []string{"", "../escape", "a/b", "a?b", "a#b"} {
		if _, err := Build(prefix, bad); err == nil {
			t.Fatalf("Build accepted %q", bad)
		}
	}
}
