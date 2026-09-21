package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"filees/pkg/deploy"
)

type activationFlags struct {
	serverID, address, knownHosts, stateRoot string
}

func addActivationFlags(flags *flag.FlagSet, values *activationFlags) {
	flags.StringVar(&values.serverID, "server-id", "", "local server profile ID")
	flags.StringVar(&values.address, "server", "", "FileES server address (SSH port defaults to 22)")
	flags.StringVar(&values.knownHosts, "known-hosts", "", "absolute pinned known_hosts path")
	flags.StringVar(&values.stateRoot, "state-root", "", "absolute activation state root")
}

func (values activationFlags) profile() deploy.ServerProfile {
	return deploy.ServerProfile{ID: values.serverID, Address: values.address, KnownHostsPath: values.knownHosts}
}

func cmdActivateBegin(args []string) int {
	flags := flag.NewFlagSet("activate-begin", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var common activationFlags
	var email string
	addActivationFlags(flags, &common)
	flags.StringVar(&email, "email", "", "ticket delivery address")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	passport, err := deploy.BeginOnboarding(ctx, common.stateRoot, common.profile(), email)
	if err != nil {
		fmt.Fprintln(os.Stderr, "activate-begin:", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "accepted server_id=%s onboarding_request_id=%s\n", passport.ServerID, passport.OnboardingRequestID)
	return 0
}

func cmdActivateFinish(args []string) int {
	flags := flag.NewFlagSet("activate-finish", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var common activationFlags
	var remotePort int
	addActivationFlags(flags, &common)
	flags.IntVar(&remotePort, "remote-port", 42000, "server-assigned reverse loopback port")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	passport, err := deploy.LoadOnboardPassport(common.stateRoot, common.profile())
	if err != nil {
		fmt.Fprintln(os.Stderr, "activate-finish:", err)
		return 1
	}
	raw, err := bufio.NewReader(io.LimitReader(os.Stdin, 1026)).ReadBytes('\n')
	if err != nil {
		fmt.Fprintln(os.Stderr, "activate-finish: read OTP:", err)
		return 1
	}
	otp := []byte(strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r"))
	defer clear(otp)
	if len(otp) == 0 || len(otp) > 1024 || len(raw) > 1025 {
		fmt.Fprintln(os.Stderr, "activate-finish: invalid OTP input")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	err = deploy.RunActivation(ctx, passport, deploy.ActivationOptions{Root: common.stateRoot, ServerProfile: common.profile(), RemotePort: remotePort}, otp)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintln(os.Stderr, "activate-finish: activation timed out")
		} else {
			fmt.Fprintln(os.Stderr, "activate-finish:", err)
		}
		return 1
	}
	fmt.Fprintf(os.Stdout, "active server_id=%s\n", passport.ServerID)
	return 0
}

func cmdActivateResume(args []string) int {
	flags := flag.NewFlagSet("activate-resume", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var common activationFlags
	var remotePort int
	addActivationFlags(flags, &common)
	flags.IntVar(&remotePort, "remote-port", 42000, "server-assigned reverse loopback port")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	passport, err := deploy.LoadOnboardPassport(common.stateRoot, common.profile())
	if err != nil {
		fmt.Fprintln(os.Stderr, "activate-resume:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	err = deploy.ResumeActivation(ctx, passport, deploy.ActivationOptions{Root: common.stateRoot, ServerProfile: common.profile(), RemotePort: remotePort})
	if err != nil {
		fmt.Fprintln(os.Stderr, "activate-resume:", err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "active server_id=%s\n", passport.ServerID)
	return 0
}
