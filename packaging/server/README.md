# FileES server toolchain bundle

This bundle contains short-lived tools, not services:

- `filees-admin` and `filees-operation` are administrative commands;
- `filees-onboard` is the public-key bootstrap forced command;
- `filees-ssh-auth` is the local BSD Authentication OTP style;
- `filees-entry` is the tunnel-account forced command;
- `filees-mail` submits one pending outbox entry to a configured smarthost.

Run `install-server.sh` as the target system administrator, edit
`/etc/filees/server.json`, and keep both configuration and OTP pepper private.
The generic installer deliberately does not create an `rc.d`/systemd service
and does not modify `sshd_config`. On OpenBSD, the separate
`openbsd/install-ssh.sh` step creates the two protocol accounts, installs the
local login style and a validated `Match User` fragment, then reloads the
existing system sshd. It does not install a service, listener or `inetd` entry.

The shipped bootstrap private key is deliberately compiled into the client and
must be considered public. Its authorized-key entry can only reach the
enumeration-free onboarding forced command and has forwarding disabled.
