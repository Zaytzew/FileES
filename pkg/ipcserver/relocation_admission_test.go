package ipcserver

import "testing"

func TestQueuedRelocationRevalidatesProjectedAuthority(t *testing.T) {
	for _, change := range []string{"none", "url", "revoked", "server-removed"} {
		t.Run(change, func(t *testing.T) {
			s, _ := anchorServer(t, nil)
			url := "svn://example/repo"
			if change == "url" {
				url = "svn://example/different"
			}
			access, state := "rw", "active"
			if change == "revoked" {
				access = ""
				state = "revoked"
			}
			s.RegisterProjectedRepoPolicy("repo-1", "Docs", url, "primary", access, state, "owner", "optional", false)
			if change == "server-removed" {
				s.RemoveServer("primary")
			}
			err := s.ValidateRelocationAdmission("primary", "repo-1", "svn://example/repo")
			if (err == nil) != (change == "none") {
				t.Fatalf("change=%s err=%v", change, err)
			}
		})
	}
}

