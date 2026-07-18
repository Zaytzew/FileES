package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"filees/pkg/deploy"
)

type clientActivator struct {
	root       string
	profile    deploy.ServerProfile
	remotePort int
}

func (a clientActivator) Begin(parent context.Context, email string) error {
	if err := a.validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	_, err := deploy.BeginOnboarding(ctx, a.root, a.profile, email)
	return err
}

func (a clientActivator) Finish(parent context.Context, otp []byte) error {
	if err := a.validate(); err != nil {
		return err
	}
	passport, err := deploy.LoadOnboardPassport(a.root, a.profile)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	return deploy.RunActivation(ctx, passport, deploy.ActivationOptions{Root: a.root, ServerProfile: a.profile, RemotePort: a.remotePort}, otp)
}

func (a clientActivator) validate() error {
	if strings.TrimSpace(a.root) == "" || strings.TrimSpace(a.profile.ID) == "" || strings.TrimSpace(a.profile.Address) == "" || strings.TrimSpace(a.profile.KnownHostsPath) == "" {
		return errors.New("profil aktywacji nie jest skonfigurowany")
	}
	return nil
}
