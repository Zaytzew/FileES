//go:build openbsd

package main

import (
	"fmt"

	"filees/internal/obsandbox"
)

func lockPublishSandbox(cfg Config) error {
	profile, err := publishProfile(cfg)
	if err != nil {
		return err
	}
	// ApplyForExec pledges this process and the svn/ssh it execs. PledgePromises
	// alone would leave the child invocation unspecified.
	if err := obsandbox.ApplyForExec(profile, publishChildPromises); err != nil {
		return err
	}
	fmt.Fprintf(osStderr(), "[SECURITY] unveil locked promises=%q child=%q copies=%d\n", profile.Promises, publishChildPromises, len(cfg.allowed()))
	return nil
}

func lockActivationSandbox(stateRoot, knownHosts string) error {
	profile, err := activationProfile(stateRoot, knownHosts)
	if err != nil {
		return err
	}
	if err := obsandbox.Apply(profile); err != nil {
		return err
	}
	fmt.Fprintf(osStderr(), "[SECURITY] unveil locked promises=%q state=%q\n", profile.Promises, stateRoot)
	return nil
}
