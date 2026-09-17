package obsandbox

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

type Path struct {
	Label string
	Name  string
	Perms string
}

// Profile is constructed from closed profiles in the owning server tool.
// None of its security fields are accepted from configuration.
type Profile struct {
	Name     string
	Promises string
	Paths    []Path
}

func Validate(profile Profile) error {
	if profile.Name == "" || profile.Promises == "" {
		return errors.New("sandbox profile name and promises are required")
	}
	for _, path := range profile.Paths {
		if path.Label == "" || !filepath.IsAbs(path.Name) {
			return fmt.Errorf("sandbox profile %s contains an invalid path", profile.Name)
		}
		switch path.Perms {
		case "c", "r", "rw", "rwc", "x", "rx":
		default:
			return fmt.Errorf("sandbox profile %s path %s has invalid permissions %q", profile.Name, path.Label, path.Perms)
		}
	}
	return nil
}

// UnveilPaths returns the unveil(2) calls a profile needs, one per distinct
// path, each carrying the union of every permission requested for it.
//
// A profile is assembled from independent needs, so one directory can appear
// under several labels: in a flat layout the parent of data_authz_file (rwc,
// for its temp-sibling-then-rename writer) is also the parent of the service
// working copy (r). unveil(2) does not accumulate - a repeated call replaces
// the permissions of that path, so the later, narrower entry silently revoked
// the write. Measured on OpenBSD 7.9 on 2026-09-17: "rwc" then "r" refuses
// the create with EACCES, "r" then "rwc" allows it, and `filees-admin repo
// prune --apply` failed to write the authz temp file in exactly that layout.
// Each entry is individually required, so their union grants nothing a
// profile did not ask for on that path.
func UnveilPaths(profile Profile) []Path {
	merged := make([]Path, 0, len(profile.Paths))
	index := map[string]int{}
	for _, path := range profile.Paths {
		name := filepath.Clean(path.Name)
		i, seen := index[name]
		if !seen {
			index[name] = len(merged)
			merged = append(merged, Path{Label: path.Label, Name: name, Perms: path.Perms})
			continue
		}
		merged[i].Label += "+" + path.Label
		merged[i].Perms = unionPerms(merged[i].Perms, path.Perms)
	}
	return merged
}

func unionPerms(a, b string) string {
	var union strings.Builder
	for _, perm := range "rwxc" {
		if strings.ContainsRune(a, perm) || strings.ContainsRune(b, perm) {
			union.WriteRune(perm)
		}
	}
	return union.String()
}
