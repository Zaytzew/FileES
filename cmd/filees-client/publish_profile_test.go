package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filees/pkg/clientprofile"
	"filees/pkg/clientview"
	"filees/pkg/deploy"
	"filees/pkg/privatefile"
)

func publishFixture() (clientprofile.Profile, clientview.View) {
	p := clientprofile.Profile{ClientID: "00000000-0000-0000-0000-000000000001", Address: "example.net:2222"}
	v := clientview.View{Schema: clientview.Schema, ServerDisplayName: "Lab", ClientID: p.ClientID, RealmID: "00000000-0000-0000-0000-000000000002", Generation: 1, GeneratedAt: time.Now().UTC(), ClientRole: "normal", Repositories: []clientview.Repository{{RepoID: "00000000-0000-0000-0000-000000000003", DisplayName: "Faktury 2026", URL: "svn+ssh://_filees-data@example.net/00000000-0000-0000-0000-000000000003", Access: "rw", State: "active"}}}
	return p, v
}

func TestPublishRepositorySelectionAndTransport(t *testing.T) {
	p, base := publishFixture()
	for _, tc := range []struct {
		name   string
		change func(*clientview.View)
	}{
		{"missing", func(v *clientview.View) { v.Repositories = nil }},
		{"ambiguous", func(v *clientview.View) {
			r := v.Repositories[0]
			r.RepoID = "00000000-0000-0000-0000-000000000004"
			v.Repositories = append(v.Repositories, r)
		}},
		{"read-only", func(v *clientview.View) { v.Repositories[0].Access = "r" }},
		{"role", func(v *clientview.View) { v.ClientRole = "ro" }},
		{"revoked", func(v *clientview.View) { v.Repositories[0].State = "revoked" }},
		{"initializing", func(v *clientview.View) { v.Repositories[0].State = "initializing" }},
		{"lock", func(v *clientview.View) { v.Repositories[0].EditingPolicy = clientview.EditingLockRequired }},
		{"shelf", func(v *clientview.View) { v.Repositories[0].Purpose = clientview.PurposeUploadShelf }},
		{"different-host", func(v *clientview.View) { v.Repositories[0].URL = "svn+ssh://_filees-data@other.net/repo" }},
		{"service-account", func(v *clientview.View) { v.Repositories[0].URL = "svn+ssh://_filees-client@example.net/repo" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := base
			v.Repositories = append([]clientview.Repository(nil), base.Repositories...)
			tc.change(&v)
			if _, err := selectPublishRepository(v, "Faktury 2026", "", p); err == nil {
				t.Fatal("unsafe selection accepted")
			}
		})
	}
	if _, err := selectPublishRepository(base, "Faktury 2026", "", p); err != nil {
		t.Fatal(err)
	}
	// A rename keeps the immutable repository binding, even if an unrelated
	// repository later acquires the old display name.
	id := base.Repositories[0].RepoID
	base.Repositories[0].DisplayName = "Renamed"
	if _, err := selectPublishRepository(base, "", id, p); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"svn+ssh://_filees-data:password@example.net/repo", "svn+ssh://_filees-data@example.net:22/repo", "file:///repo", "svn+ssh://_filees-data@example.net/repo?query=1", "svn+ssh://_filees-data@example.net/repo#fragment"} {
		if _, err := restrictedPublishURL(raw, "_filees-data", "example.net"); err == nil {
			t.Fatalf("unsafe URL accepted: %s", raw)
		}
	}
}

func TestPublishSavedBindingIdempotenceRollbackAndPrivacy(t *testing.T) {
	dir := t.TempDir()
	_, v := publishFixture()
	j := publishJob{Schema: publishJobSchema, Name: "faktury-2026", Source: filepath.Join(dir, "source"), ClientID: v.ClientID, RealmID: v.RealmID, RepoID: v.Repositories[0].RepoID, RepoURL: v.Repositories[0].URL, MinimumGeneration: 1}
	if err := storePublishJob(dir, j, v); err != nil {
		t.Fatal(err)
	}
	if err := privatefile.Verify(filepath.Join(dir, "job.json")); err != nil {
		t.Fatal(err)
	}
	v.Generation = 2
	j.MinimumGeneration = 2
	if err := storePublishJob(dir, j, v); err != nil {
		t.Fatal(err)
	}
	j.Source = filepath.Join(dir, "another-source")
	if err := storePublishJob(dir, j, v); err == nil {
		t.Fatal("silently rebound source")
	}
	j.Source = filepath.Join(dir, "source")
	v.Generation = 1
	if err := storePublishJob(dir, j, v); err == nil {
		t.Fatal("rolled back accepted projection")
	}
	var saved publishJob
	if err := readPublishJSON(filepath.Join(dir, "job.json"), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Source != j.Source || saved.MinimumGeneration != 1 {
		t.Fatalf("changed immutable binding: %+v", saved)
	}
	for _, content := range []string{"{} {}", `{"Unknown":true}`} {
		path := filepath.Join(dir, "bad.json")
		os.WriteFile(path, []byte(content), 0o600)
		if err := readPublishJSON(path, new(publishJob)); err == nil {
			t.Fatal("accepted malformed state")
		}
	}
}

func TestPublishSourceCannotContainState(t *testing.T) {
	source := t.TempDir()
	target := filepath.Join(source, "must-not-create", "jobs")
	if _, err := preparePublishDirectory(target, "faktury", source); err == nil {
		t.Fatal("self-ingest accepted")
	}
	if _, err := os.Stat(filepath.Dir(target)); !os.IsNotExist(err) {
		t.Fatalf("created state inside source: %v", err)
	}
	root := t.TempDir()
	link := filepath.Join(root, "linked-source")
	if err := os.Symlink(source, link); err == nil {
		if _, err := preparePublishDirectory(filepath.Join(link, "nested"), "faktury", source); err == nil {
			t.Fatal("symlinked self-ingest accepted")
		}
		if err := os.Symlink(source, filepath.Join(root, "faktury")); err != nil {
			t.Fatal(err)
		}
		if _, err := preparePublishDirectory(root, "faktury", source); err == nil {
			t.Fatal("symlinked job directory accepted")
		}
	}
}

func TestActivationUsesSavedPortWithoutManualOverride(t *testing.T) {
	p := deploy.OnboardPassport{RemotePort: 42319}
	if port, err := activationRemotePort(p, 0); err != nil || port != 42319 {
		t.Fatalf("saved port=%d: %v", port, err)
	}
	if port, err := activationRemotePort(p, 42001); err != nil || port != 42001 {
		t.Fatalf("explicit port=%d: %v", port, err)
	}
	if _, err := activationRemotePort(deploy.OnboardPassport{}, 0); err == nil {
		t.Fatal("invented port for legacy passport")
	}
}

func TestPublishViewOutputIsBounded(t *testing.T) {
	var out boundedPublishView
	if _, err := out.Write([]byte(strings.Repeat("x", 1<<20))); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("x")); err == nil {
		t.Fatal("projection exceeded bound")
	}
}

func TestPublishActivationDiscoveryFailsClosedAndSelectsByName(t *testing.T) {
	root := t.TempDir()
	makeProfile := func(id string) string {
		dir, err := clientprofile.ServerDir(root, id)
		if err != nil {
			t.Fatal(err)
		}
		p := clientprofile.Profile{Schema: clientprofile.Schema, ServerID: id, DisplayName: "Office", Address: "example.net:2222", ClientID: "00000000-0000-0000-0000-000000000001", IdentityFile: filepath.Join(dir, "id"), KnownHosts: filepath.Join(dir, "known"), SSHPort: 2222, ServiceURL: "svn+ssh://_filees-client@example.net/clients/id/", ServiceWC: filepath.Join(dir, "wc"), RelativeViewPath: "view.json", CachePath: filepath.Join(dir, "cache", "view.json"), PollInterval: time.Minute}
		path := filepath.Join(dir, "client-profile.json")
		if err := clientprofile.Store(path, p); err != nil {
			t.Fatal(err)
		}
		return path
	}
	makeProfile("office:a")
	if p, _, _, err := selectPublishActivation("", "", root, "Office"); err != nil || p.ServerID != "office:a" {
		t.Fatalf("selected=%s: %v", p.ServerID, err)
	}
	second := makeProfile("office:b")
	if _, _, _, err := selectPublishActivation("", "", root, ""); err == nil {
		t.Fatal("silently selected one of several activations")
	}
	if _, _, _, err := selectPublishActivation("", "", root, "Office"); err == nil {
		t.Fatal("ambiguous server name accepted")
	}
	if p, _, _, err := selectPublishActivation("", "", root, "office:a"); err != nil || p.ServerID != "office:a" {
		t.Fatalf("selected=%s: %v", p.ServerID, err)
	}
	if err := os.WriteFile(second, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := selectPublishActivation("", "", root, ""); err == nil {
		t.Fatal("silently ignored malformed activated profile")
	}
}
