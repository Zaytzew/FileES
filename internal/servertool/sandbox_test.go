package servertool

import (
	"reflect"
	"testing"

	"filees/internal/obsandbox"
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
		profile := repositoryProfile("/srv/filees", test.access)
		if profile.Name != test.access.name || profile.Promises != test.promises || !reflect.DeepEqual(profile.Paths, test.paths) {
			t.Fatalf("profile %s = %+v", test.access.name, profile)
		}
		if err := obsandbox.Validate(profile); err != nil {
			t.Fatal(err)
		}
	}
}
