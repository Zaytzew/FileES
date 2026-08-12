package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"filees/internal/obsandbox"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"filees/public-shares/authority"
	"filees/public-shares/backchannel"
	"filees/public-shares/channel"
	"filees/public-shares/recipientotp"
)

const authoritySandboxPromises = "stdio rpath wpath cpath fattr flock proc exec prot_exec unix inet"
const publicMailerPath = "/usr/local/libexec/filees/filees-mail"

func main() {
	configPath := flag.String("config", "/etc/filees/server.json", "FileES server configuration")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *configPath); err != nil {
		fmt.Fprintln(os.Stderr, "filees-public-authority:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, configPath string) error {
	config, err := serverconfig.LoadFor(configPath, serverconfig.SecretPublicShares)
	if err != nil {
		return err
	}
	if !config.PublicShares.Enabled {
		return errors.New("public shares are disabled")
	}
	listener, cleanup, err := listen(config.PublicShares)
	if err != nil {
		return err
	}
	defer cleanup()
	r := config.Repositories
	stateRoot := config.PublicShares.EffectiveStateRoot(r.ResultsRoot)
	stagingRoot := config.PublicShares.EffectiveAuthorityStagingRoot()
	if err := os.MkdirAll(stateRoot, 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(stagingRoot, 0700); err != nil {
		return err
	}
	mailer, mailerDone, err := startPublicMailer(configPath)
	if err != nil {
		return err
	}
	mailerWaited := false
	defer func() {
		if !mailerWaited {
			_ = mailer.Process.Kill()
			<-mailerDone
		}
	}()
	svnlook := r.EffectiveSVNLookBinary()
	if !filepath.IsAbs(svnlook) {
		return errors.New("svnlook path must be absolute")
	}
	paths := []obsandbox.Path{
		{Label: "public-share-state", Name: stateRoot, Perms: "rwc"},
		{Label: "service-working-copy", Name: config.Activation.ServiceWorkingCopy, Perms: "r"},
		{Label: "repositories", Name: r.Root, Perms: "r"},
		{Label: "authority-staging", Name: stagingRoot, Perms: "rwc"},
		{Label: "svnlook", Name: svnlook, Perms: "rx"},
		{Label: "null-device", Name: "/dev/null", Perms: "rw"},
		{Label: "loader", Name: "/usr/libexec/ld.so", Perms: "rx"},
		{Label: "loader-hints", Name: "/var/run/ld.so.hints", Perms: "r"},
		{Label: "system-libraries", Name: "/usr/lib", Perms: "r"},
		{Label: "local-libraries", Name: "/usr/local/lib", Perms: "r"},
	}
	if config.PublicShares.BackchannelNetwork == "unix" {
		paths = append(paths, obsandbox.Path{Label: "backchannel-socket", Name: config.PublicShares.BackchannelAddress, Perms: "rwc"})
	}
	if err := obsandbox.Apply(obsandbox.Profile{Name: "filees-public-authority", Promises: authoritySandboxPromises, Paths: paths}); err != nil {
		return err
	}
	publisher := repoworker.ServicePublisher{ServiceWC: config.Activation.ServiceWorkingCopy}
	channels := &channel.Store{Root: stateRoot, Authority: publisher}
	otp := &recipientotp.Service{
		Root: filepath.Join(stateRoot, "recipient-otp"), Key: config.PublicShareFrostKey, Channels: channels,
		Outbox: repoworker.PublicShareOutbox{Root: filepath.Join(stateRoot, "outbox")},
	}
	resolver := authority.Resolver{Channels: channels, Source: authority.SVNLookSource{SVNLook: svnlook, RepositoriesRoot: r.Root}, FrostKey: config.PublicShareFrostKey, StagingRoot: stagingRoot, MaxLeafSize: config.PublicShares.EffectiveMaxLeafSize(), RecipientOTP: otp}
	server := &http.Server{Handler: backchannel.Server{Authority: resolver, FetchSlots: make(chan struct{}, 2)}, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 0, IdleTimeout: 30 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		_ = server.Close()
		_ = mailer.Process.Kill()
		<-mailerDone
		mailerWaited = true
		return nil
	case err := <-serveDone:
		_ = mailer.Process.Kill()
		<-mailerDone
		mailerWaited = true
		return err
	case err := <-mailerDone:
		mailerWaited = true
		_ = server.Close()
		if err == nil {
			err = errors.New("public share mailer stopped")
		}
		return fmt.Errorf("public share mailer: %w", err)
	}
}

func startPublicMailer(configPath string) (*exec.Cmd, <-chan error, error) {
	command := exec.Command(publicMailerPath, "-config", configPath, "public-loop")
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		return nil, nil, fmt.Errorf("start public share mailer: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	return command, done, nil
}

func listen(config serverconfig.PublicSharesFile) (net.Listener, func(), error) {
	if config.BackchannelNetwork == "tcp" {
		listener, err := net.Listen("tcp", config.BackchannelAddress)
		return listener, func() {
			if listener != nil {
				_ = listener.Close()
			}
		}, err
	}
	path := config.BackchannelAddress
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, nil, errors.New("backchannel address exists and is not a socket")
		}
		if err := os.Remove(path); err != nil {
			return nil, nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(path, 0660); err != nil {
		listener.Close()
		os.Remove(path)
		return nil, nil, err
	}
	if config.BackchannelSocketGroup != "" {
		group, err := user.LookupGroup(config.BackchannelSocketGroup)
		if err != nil {
			listener.Close()
			os.Remove(path)
			return nil, nil, err
		}
		gid, err := strconv.Atoi(group.Gid)
		if err != nil || os.Chown(path, -1, gid) != nil {
			listener.Close()
			os.Remove(path)
			return nil, nil, errors.New("cannot assign backchannel socket group")
		}
	}
	created, _ := os.Lstat(path)
	cleanup := func() {
		_ = listener.Close()
		if current, err := os.Lstat(path); err == nil && created != nil && os.SameFile(created, current) {
			_ = os.Remove(path)
		}
	}
	return listener, cleanup, nil
}
