package obsandbox

import (
	"errors"
	"fmt"
	"path/filepath"
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
