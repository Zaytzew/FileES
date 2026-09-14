package main

import "testing"

func TestAdminNeedsStateIdentity(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{name: "version is stateless", args: []string{"version"}, want: false},
		{name: "ticket list", args: []string{"ticket", "list"}, want: true},
		{name: "ticket create", args: []string{"ticket", "create", "user@example.test"}, want: true},
		{name: "usage refusal", args: nil, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := adminNeedsStateIdentity(tc.args); got != tc.want {
				t.Fatalf("adminNeedsStateIdentity(%q)=%v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
