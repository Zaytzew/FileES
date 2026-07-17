//go:build !openbsd

package obsandbox

import "errors"

func Begin(string) error { return nil }

func Apply(profile Profile) error { return Validate(profile) }

func ApplyForExec(profile Profile, execPromises string) error {
	if execPromises == "" {
		return Validate(Profile{})
	}
	return Validate(profile)
}

func PledgeForExec(runtimePromises, execPromises string) error {
	if runtimePromises == "" || execPromises == "" {
		return errors.New("exec pledge requires runtime and child promises")
	}
	return nil
}
