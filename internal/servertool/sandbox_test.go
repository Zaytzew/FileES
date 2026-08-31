package servertool

import (
	"path/filepath"
	"reflect"
	"testing"

	"filees/internal/obsandbox"
	"filees/pkg/activation"
	"filees/pkg/onboarding"
)

func TestRepositoryProfilesAreClosedPerAction(t *testing.T) {
	tests := []struct {
		access   toolAccess
		promises string
		paths    []obsandbox.Path
	}{
		{
			access:   toolAccess{name: "filees-admin/ticket-list", areas: onboarding.AreaTickets},
			promises: readPromises,
			paths: []obsandbox.Path{
				{Label: "lock", Name: "/srv/filees/.toolchain.lock", Perms: "rw"},
				{Label: "tickets", Name: "/srv/filees/tickets", Perms: "r"},
			},
		},
		{
			access:   toolAccess{name: "filees-onboard/take", areas: onboarding.AreaAll, write: true, needOTP: true},
			promises: writePromises,
			paths: []obsandbox.Path{
				{Label: "lock", Name: "/srv/filees/.toolchain.lock", Perms: "rw"},
				{Label: "tickets", Name: "/srv/filees/tickets", Perms: "rwc"},
				{Label: "operations", Name: "/srv/filees/operations", Perms: "rwc"},
				{Label: "audit", Name: "/srv/filees/audit", Perms: "rwc"},
			},
		},
		{
			access:   toolAccess{name: "filees-operation/recover", areas: onboarding.AreaAll, write: true, needOTP: true, needActivation: true, needRepoResults: true},
			promises: writePromises,
			paths: []obsandbox.Path{
				{Label: "lock", Name: "/srv/filees/.toolchain.lock", Perms: "rw"},
				{Label: "tickets", Name: "/srv/filees/tickets", Perms: "rwc"},
				{Label: "operations", Name: "/srv/filees/operations", Perms: "rwc"},
				{Label: "audit", Name: "/srv/filees/audit", Perms: "rwc"},
				{Label: "activation", Name: "/srv/activation", Perms: "rwc"},
				{Label: "client-authorized-keys", Name: "/srv/activation/authorized_keys", Perms: "rwc"},
				{Label: "service-authz", Name: "/srv/activation/authz", Perms: "rwc"},
				{Label: "session-root", Name: "/srv/activation/sessions", Perms: "rwc"},
				{Label: "repository-results", Name: "/srv/repository-results", Perms: "rwc"},
				{Label: "repository-deletion-archive", Name: "/srv/repository-archives", Perms: "rwc"},
				{Label: "public-share-state", Name: "/srv/public-share-state", Perms: "rwc"},
			},
		},
		{
			access:   toolAccess{name: "filees-mail/send", areas: onboarding.AreaOperations, write: true, needSMTP: true},
			promises: mailPromises,
			paths: []obsandbox.Path{
				{Label: "lock", Name: "/srv/filees/.toolchain.lock", Perms: "rw"},
				{Label: "operations", Name: "/srv/filees/operations", Perms: "rwc"},
				{Label: "resolver", Name: "/etc/resolv.conf", Perms: "r"},
				{Label: "hosts", Name: "/etc/hosts", Perms: "r"},
			},
		},
		{
			access:   toolAccess{name: "filees-ssh-auth/response", areas: onboarding.AreaOperations, write: true, needOTP: true},
			promises: writePromises,
			paths: []obsandbox.Path{
				{Label: "lock", Name: "/srv/filees/.toolchain.lock", Perms: "rw"},
				{Label: "operations", Name: "/srv/filees/operations", Perms: "rwc"},
			},
		},
		{
			access:   toolAccess{name: "filees-worker/deploy", areas: onboarding.AreaOperations, write: true, needWorker: true, needWorkerPublic: true},
			promises: workerPromises,
			paths: []obsandbox.Path{
				{Label: "lock", Name: "/srv/filees/.toolchain.lock", Perms: "rw"},
				{Label: "operations", Name: "/srv/filees/operations", Perms: "rwc"},
			},
		},
	}
	activationConfig := activation.Config{
		Root:               "/srv/activation",
		AuthorizedKeysFile: "/srv/activation/authorized_keys",
		AuthzFile:          "/srv/activation/authz",
	}
	for _, test := range tests {
		profile := repositoryProfile("/srv/filees", test.access, activationConfig, "/srv/repository-results", "/srv/repository-archives", "/srv/public-share-state")
		if profile.Name != test.access.name || profile.Promises != test.promises || !reflect.DeepEqual(profile.Paths, test.paths) {
			t.Fatalf("profile %s = %+v", test.access.name, profile)
		}
		if err := obsandbox.Validate(profile); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkerSVNProfileIncludesOnlyExactRuntimeAndTreeParents(t *testing.T) {
	access := toolAccess{name: "filees-admin/client-revoke", areas: onboarding.AreaOperations, write: true, needActivation: true, needSVN: true}
	if got := access.promises(); got != svnPromises {
		t.Fatalf("SVN parent promises = %q", got)
	}
	if svnPromises != "stdio rpath wpath cpath fattr flock proc exec" {
		t.Fatalf("closed SVN parent promises = %q", svnPromises)
	}
	if svnExecPromises != "stdio rpath wpath cpath fattr flock proc prot_exec unveil" {
		t.Fatalf("SVN child promises = %q; native OpenBSD svn must retain unveil", svnExecPromises)
	}
	config := activation.Config{
		Root: "/srv/activation", AuthorizedKeysFile: "/srv/activation/authorized_keys", AuthzFile: "/srv/activation/authz",
		DataAuthzFile:      "/srv/data-authority/repositories.authz",
		ServiceWorkingCopy: "/srv/svn/service-wc", ServiceRepository: "/srv/svn/service-repo",
		SVNBinary: "/usr/local/bin/svn", SVNServeBinary: "/usr/local/bin/svnserve",
	}
	profile := repositoryProfile("/srv/filees", access, config, "", "", "")
	wanted := map[string]obsandbox.Path{
		"data-authz-parent":           {Label: "data-authz-parent", Name: "/srv/data-authority", Perms: "rwc"},
		"data-authz":                  {Label: "data-authz", Name: config.DataAuthzFile, Perms: "rwc"},
		"service-working-copy-parent": {Label: "service-working-copy-parent", Name: "/srv/svn", Perms: "r"},
		"service-repository-parent":   {Label: "service-repository-parent", Name: "/srv/svn", Perms: "r"},
		"service-working-copy":        {Label: "service-working-copy", Name: config.ServiceWorkingCopy, Perms: "rwc"},
		"service-repository":          {Label: "service-repository", Name: config.ServiceRepository, Perms: "rwc"},
		"loader-hints":                {Label: "loader-hints", Name: "/var/run/ld.so.hints", Perms: "r"},
		"svn-system-config":           {Label: "svn-system-config", Name: "/etc/subversion", Perms: "r"},
		"random":                      {Label: "random", Name: "/dev/urandom", Perms: "r"},
	}
	for _, path := range profile.Paths {
		if expected, ok := wanted[path.Label]; ok {
			if path != expected {
				t.Fatalf("SVN sandbox path %s = %+v, want %+v", path.Label, path, expected)
			}
			delete(wanted, path.Label)
		}
	}
	if len(wanted) != 0 {
		t.Fatalf("SVN sandbox profile missing paths: %+v", wanted)
	}
}

func TestRepositoryInspectionProfileIncludesConfiguredSVNLook(t *testing.T) {
	access := toolAccess{
		name: "filees-admin/repo-prune", needRepositoryData: true, needRepoInspection: true, needSVN: true,
		repositoryRoot: "/srv/data/repositories", repositoryAuthz: "/srv/data/repositories.authz",
		svnAdminBinary: "/usr/local/bin/svnadmin", svnLookBinary: "/opt/subversion/bin/svnlook",
	}
	config := activation.Config{
		Root:               "/srv/filees/activation",
		ServiceWorkingCopy: "/srv/filees/service-wc", ServiceRepository: "/srv/filees/service-repo",
		SVNBinary: "/usr/local/bin/svn",
	}
	profile := repositoryProfile("/srv/filees/onboarding", access, config, "/srv/filees/results", "", "")
	found, foundLock := false, false
	for _, path := range profile.Paths {
		if path.Label == "svnlook" {
			found = path.Name == access.svnLookBinary && path.Perms == "rx"
		}
		if path.Label == "service-working-copy-lock" {
			foundLock = path.Name == filepath.Join(config.Root, ".service-wc.lock") && path.Perms == "rwc"
		}
	}
	if !found {
		t.Fatalf("repository inspection profile omits configured svnlook: %+v", profile.Paths)
	}
	if !foundLock {
		t.Fatalf("repository inspection profile omits service-WC lock: %+v", profile.Paths)
	}
	if err := obsandbox.Validate(profile); err != nil {
		t.Fatal(err)
	}
}

func TestDataAuthzInsideActivationRootNeedsNoBroaderParentUnveil(t *testing.T) {
	config := activation.Config{
		Root:               "/srv/activation",
		AuthorizedKeysFile: "/srv/activation/authorized_keys",
		AuthzFile:          "/srv/activation/service.authz",
		DataAuthzFile:      "/srv/activation/repositories.authz",
	}
	profile := repositoryProfile("/srv/filees", toolAccess{name: "worker", needActivation: true}, config, "", "", "")
	for _, path := range profile.Paths {
		if path.Label == "data-authz-parent" {
			t.Fatalf("default data authz path unexpectedly widened sandbox: %+v", path)
		}
	}
}

// TestActivationKeyFilesOutsideRootNeedOwnParentUnveil is the same
// missing-parent-unveil class the r665 data-authz fix covered
// (reports/CUSTOM_DATA_AUTHZ_UNVEIL_BLOCKS_ACTIVATION_2026-08-31.md), found
// during the same live audit for authorized_keys_file/authz_file: both are
// independently configurable absolute paths (not required to live under
// activation.root), and renderAccessLocked replaces both by temp-sibling-
// then-rename. The default layout (both inside Root) never triggered this,
// same as data-authz before it moved.
func TestActivationKeyFilesOutsideRootNeedOwnParentUnveil(t *testing.T) {
	config := activation.Config{
		Root:               "/srv/activation",
		AuthorizedKeysFile: "/srv/keys/authorized_keys",
		AuthzFile:          "/srv/authz-store/service.authz",
	}
	profile := repositoryProfile("/srv/filees", toolAccess{name: "worker", needActivation: true}, config, "", "", "")
	wanted := map[string]obsandbox.Path{
		"client-authorized-keys-parent": {Label: "client-authorized-keys-parent", Name: "/srv/keys", Perms: "rwc"},
		"service-authz-parent":          {Label: "service-authz-parent", Name: "/srv/authz-store", Perms: "rwc"},
	}
	for _, path := range profile.Paths {
		if expected, ok := wanted[path.Label]; ok {
			if path != expected {
				t.Fatalf("path %s = %+v, want %+v", path.Label, path, expected)
			}
			delete(wanted, path.Label)
		}
	}
	if len(wanted) != 0 {
		t.Fatalf("external key files did not widen sandbox: missing %+v", wanted)
	}
}

// TestRepositoryAuthzOutsideRepositoryRootNeedsOwnParentUnveil covers the
// third instance of the same bug class, in the needRepositoryData branch
// used by filees-operation recover (needRepositoryData+needSVN). The
// default layout (data_authz_file inside repositories.root's sibling
// activation tree) coincidentally works; an operator-relocated
// data_authz_file outside repositories.root does not without this.
func TestRepositoryAuthzOutsideRepositoryRootNeedsOwnParentUnveil(t *testing.T) {
	access := toolAccess{name: "worker", needRepositoryData: true, repositoryRoot: "/srv/repositories", repositoryAuthz: "/srv/external-authz/repositories.authz", svnAdminBinary: "/usr/local/bin/svnadmin"}
	profile := repositoryProfile("/srv/filees", access, activation.Config{}, "", "", "")
	found := false
	for _, path := range profile.Paths {
		if path.Label == "repository-authz-parent" {
			found = true
			want := obsandbox.Path{Label: "repository-authz-parent", Name: "/srv/external-authz", Perms: "rwc"}
			if path != want {
				t.Fatalf("repository-authz-parent = %+v, want %+v", path, want)
			}
		}
	}
	if !found {
		t.Fatal("external repository data_authz_file did not widen sandbox with repository-authz-parent")
	}
}
