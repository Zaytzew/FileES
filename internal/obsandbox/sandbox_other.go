//go:build !openbsd

package obsandbox

func Begin(string) error { return nil }

func Apply(profile Profile) error { return Validate(profile) }

func ApplyForExec(profile Profile, execPromises string) error {
	if execPromises == "" {
		return Validate(Profile{})
	}
	return Validate(profile)
}
