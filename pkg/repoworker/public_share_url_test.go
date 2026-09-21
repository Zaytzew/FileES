package repoworker

import "testing"

func TestPublicShareRecipientURL(t *testing.T) {
	for _, base := range []string{"", "http://host", "file:///tmp", "https://user:password@host", "https://host?invite=secret", "https://host#secret", "https://host/subpath", "https://"} {
		if got := publicShareRecipientURL(base, "realm", "share"); got != "" {
			t.Errorf("unsafe base %q produced %q", base, got)
		}
	}
	if got := publicShareRecipientURL("https://download.example:8443/", "realm", "share"); got != "https://download.example:8443/realm/share" {
		t.Fatal(got)
	}
}
