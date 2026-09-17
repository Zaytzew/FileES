//go:build !windows

package obsandbox

import (
	"reflect"
	"testing"
)

func TestValidateRejectsRelativeAndConfigurableLookingProfiles(t *testing.T) {
	valid := Profile{Name: "tool/action", Promises: "stdio rpath", Paths: []Path{{Label: "input", Name: "/srv/filees/input", Perms: "r"}}}
	if err := Validate(valid); err != nil {
		t.Fatal(err)
	}
	createOnly := Profile{Name: "tool/cleanup", Promises: "stdio cpath", Paths: []Path{{Label: "parent", Name: "/srv/filees/sessions", Perms: "c"}}}
	if err := Validate(createOnly); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []Profile{
		{Name: "", Promises: valid.Promises},
		{Name: valid.Name, Promises: ""},
		{Name: valid.Name, Promises: valid.Promises, Paths: []Path{{Label: "input", Name: "relative", Perms: "r"}}},
		{Name: valid.Name, Promises: valid.Promises, Paths: []Path{{Label: "input", Name: "/tmp", Perms: "rwx"}}},
	} {
		if err := Validate(profile); err == nil {
			t.Fatalf("invalid profile accepted: %+v", profile)
		}
	}
}

func TestNarrowRejectsEmptyPromises(t *testing.T) {
	if err := Narrow(""); err == nil {
		t.Fatal("empty runtime promise set accepted")
	}
}

func TestUnveilPathsMergesRepeatedPathByUnion(t *testing.T) {
	profile := Profile{Name: "tool/action", Promises: "stdio rpath", Paths: []Path{
		{Label: "authz-parent", Name: "/srv/filees", Perms: "rwc"},
		{Label: "input", Name: "/srv/filees/input", Perms: "r"},
		{Label: "wc-parent", Name: "/srv/filees/", Perms: "r"},
		{Label: "binary", Name: "/srv/filees", Perms: "rx"},
	}}
	want := []Path{
		{Label: "authz-parent+wc-parent+binary", Name: "/srv/filees", Perms: "rwxc"},
		{Label: "input", Name: "/srv/filees/input", Perms: "r"},
	}
	if got := UnveilPaths(profile); !reflect.DeepEqual(got, want) {
		t.Fatalf("UnveilPaths = %+v, want %+v", got, want)
	}
}
