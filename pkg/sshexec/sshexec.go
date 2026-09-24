// Package sshexec is the SSH client Subversion runs as its svn+ssh tunnel
// ("filees ssh-exec", named in SVN_SSH by pkg/client). It replaces the system
// OpenSSH client for SVN: a clean Windows has none, and FileES used exactly
// one, fully pinned shape of it (pkg/client buildSSHCommand):
//
//	-F /dev/null, BatchMode=yes          -> no configuration, no prompts
//	-i identity, IdentitiesOnly=yes,
//	  IdentityAgent=none, no password
//	  or keyboard-interactive            -> publickey with that one key
//	StrictHostKeyChecking=yes,
//	  UserKnownHostsFile=pinned,
//	  HostKeyAlgorithms=ssh-ed25519      -> pinned file only, ed25519 only
//	HostName=, HostKeyAlias=             -> connect there, verify under alias
//	ServerAliveInterval=15,
//	  ServerAliveCountMax=3              -> keepalive@openssh.com probes
//	-T and the remote command            -> one exec, stdin/stdout bridged,
//	                                        remote exit status returned
//
// Messages keep OpenSSH's wording where pkg/errmap reads them from the stderr
// svn captures ("Permission denied (publickey)", "not responding").
package sshexec

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"filees/pkg/sshlink"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Options is the pinned connection policy the flags carry.
type Options struct {
	Identity      string
	KnownHosts    string
	Port          int
	HostName      string // where to connect; the destination host when empty
	HostKeyAlias  string // name the host key is pinned under; host[:port] form
	AliveInterval time.Duration
	AliveCountMax int
}

const dialTimeout = 30 * time.Second

// Main parses "ssh-exec [flags] [user@]host command..." as Subversion calls
// the tunnel, runs it and answers the process exit code: the remote command's
// status, or 255 for a failure of the connection itself, as ssh(1) does.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("ssh-exec", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var opts Options
	var alive int
	flags.StringVar(&opts.Identity, "i", "", "private key (the only one offered)")
	flags.StringVar(&opts.KnownHosts, "known-hosts", "", "pinned known_hosts file")
	flags.IntVar(&opts.Port, "p", 22, "server port")
	flags.StringVar(&opts.HostName, "host-name", "", "host to connect to instead of the destination")
	flags.StringVar(&opts.HostKeyAlias, "host-key-alias", "", "name the host key is pinned under")
	flags.IntVar(&alive, "alive", 15, "keepalive interval in seconds")
	flags.IntVar(&opts.AliveCountMax, "alive-max", 3, "unanswered keepalives before giving up")
	if err := flags.Parse(args); err != nil {
		return 255
	}
	if flags.NArg() < 2 {
		fmt.Fprintln(stderr, "usage: filees ssh-exec [flags] user@host command...")
		return 255
	}
	opts.AliveInterval = time.Duration(alive) * time.Second
	code, err := Run(context.Background(), opts, flags.Arg(0), strings.Join(flags.Args()[1:], " "), stdin, stdout, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
	}
	return code
}

// Run connects, runs command and bridges the streams until the remote side
// exits. A non-nil error is a failure of the connection, reported with 255.
func Run(ctx context.Context, opts Options, destination, command string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	user, host, ok := strings.Cut(destination, "@")
	if !ok || user == "" || host == "" {
		return 255, fmt.Errorf("ssh: destination %q has no user", destination)
	}
	host = strings.Trim(host, "[]")
	if opts.HostName != "" {
		host = opts.HostName
	}
	if opts.Port < 1 || opts.Port > 65535 {
		return 255, fmt.Errorf("ssh: bad port %d", opts.Port)
	}
	port := strconv.Itoa(opts.Port)
	signer, err := loadIdentity(opts.Identity)
	if err != nil {
		return 255, err
	}
	pinned, err := knownhosts.New(opts.KnownHosts)
	if err != nil {
		return 255, fmt.Errorf("ssh: pinned known_hosts: %w", err)
	}
	pinnedAs := net.JoinHostPort(host, port)
	if opts.HostKeyAlias != "" {
		if aliasHost, aliasPort, err := net.SplitHostPort(opts.HostKeyAlias); err == nil {
			pinnedAs = net.JoinHostPort(aliasHost, aliasPort)
		} else {
			pinnedAs = net.JoinHostPort(opts.HostKeyAlias, "22")
		}
	}
	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, remote net.Addr, key ssh.PublicKey) error {
			return pinned(pinnedAs, remote, key)
		},
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		Timeout:           dialTimeout,
	}

	address := net.JoinHostPort(host, port)
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return 255, fmt.Errorf("ssh: connect to host %s port %s: %w", host, port, err)
	}
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	clientConn, channels, requests, err := ssh.NewClientConn(conn, address, config)
	if err != nil {
		_ = conn.Close()
		return 255, handshakeError(user, host, err)
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(clientConn, channels, requests)
	defer client.Close()

	link := sshlink.Watch(ctx, client, opts.AliveInterval, opts.AliveCountMax, host)
	defer link.Stop()

	session, err := client.NewSession()
	if err != nil {
		return 255, link.Cause(err)
	}
	defer session.Close()
	// StdinPipe, not session.Stdin: Wait would otherwise also wait for the
	// copy from stdin, and Subversion keeps our stdin open until we exit.
	remoteIn, err := session.StdinPipe()
	if err != nil {
		return 255, err
	}
	go func() {
		_, _ = io.Copy(remoteIn, stdin)
		_ = remoteIn.Close()
	}()
	session.Stdout = stdout
	session.Stderr = stderr
	err = session.Run(command)
	var exit *ssh.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &exit):
		return exit.ExitStatus(), nil
	default:
		return 255, link.Cause(err)
	}
}

// loadIdentity reads the one key offered, as -i with IdentitiesOnly does.
func loadIdentity(path string) (ssh.Signer, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ssh: identity file %s not accessible: %w", path, err)
	}
	defer clear(raw)
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("ssh: identity file %s: %w", path, err)
	}
	return signer, nil
}

// handshakeError words a failed handshake the way OpenSSH did: svn folds it
// into its own error and pkg/errmap classifies by these words.
func handshakeError(user, host string, err error) error {
	var keyErr *knownhosts.KeyError
	var revoked *knownhosts.RevokedError
	switch {
	case errors.As(err, &keyErr) && len(keyErr.Want) == 0:
		return fmt.Errorf("No ED25519 host key is known for %s and you have requested strict checking.\r\nHost key verification failed. (%w)", host, err)
	case errors.As(err, &keyErr), errors.As(err, &revoked):
		return fmt.Errorf("REMOTE HOST IDENTIFICATION HAS CHANGED for %s.\r\nHost key verification failed. (%w)", host, err)
	case strings.Contains(err.Error(), "unable to authenticate"):
		return fmt.Errorf("%s@%s: Permission denied (publickey). (%w)", user, host, err)
	default:
		return fmt.Errorf("ssh: %s: %w", host, err)
	}
}
