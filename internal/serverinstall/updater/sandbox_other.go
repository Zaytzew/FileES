//go:build !openbsd

package updater

var sandboxEnabled = func() bool { return false }

func (r *Runner) applyUnveils(_ []unveilSpec) error { return nil }

func (r *Runner) reducePledge() error { return nil }
