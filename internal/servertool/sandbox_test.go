package servertool

import (
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
			access:   toolAccess{name: "filees-operation/recover", areas: onboarding.AreaAll, write: true, needOTP: true},
			promises: writePromises,
			paths: []obsandbox.Path{
				{Label: "lock", Name: "/srv/filees/.toolchain.lock", Perms: "rw"},
				{Label: "tickets", Name: "/srv/filees/tickets", Perms: "rwc"},
				{Label: "operations", Name: "/srv/filees/operations", Perms: "rwc"},
				{Label: "audit", Name: "/srv/filees/audit", Perms: "rwc"},
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
	for _, test := range tests {
		profile := repositoryProfile("/srv/filees", test.access, activation.Config{})
		if profile.Name != test.access.name || profile.Promises != test.promises || !reflect.DeepEqual(profile.Paths, test.paths) {
			t.Fatalf("profile %s = %+v", test.access.name, profile)
		}
		if err := obsandbox.Validate(profile); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkerSVNProfileIncludesOnlyExactRuntimeAndTreeParents(t *testing.T) {
	config := activation.Config{
		Root: "/srv/activation", AuthorizedKeysFile: "/srv/activation/authorized_keys", AuthzFile: "/srv/activation/authz",
		ServiceWorkingCopy: "/srv/svn/service-wc", ServiceRepository: "/srv/svn/service-repo",
		SVNBinary: "/usr/local/bin/svn", SVNServeBinary: "/usr/local/bin/svnserve",
	}
	profile := repositoryProfile("/srv/filees", toolAccess{name: "worker", needSVN: true}, config)
	wanted := map[string]obsandbox.Path{
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
