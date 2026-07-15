//go:build !openbsd

package obsandbox

func Begin(string) error { return nil }

func Apply(profile Profile) error { return Validate(profile) }
