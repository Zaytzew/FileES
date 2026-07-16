# FileES server toolchain bundle

This bundle contains short-lived tools, not services:

- `filees-admin` and `filees-operation` are administrative commands;
- `filees-onboard` is the public-key bootstrap forced command;
- `filees-ssh-auth` is the local BSD Authentication OTP style;
- `filees-entry` is the tunnel-account forced command;
- `filees-worker` is exec'd once per authenticated deploy and exits after its bounded action;
- `filees-client-entry` is the per-key forced SVN entry used for possession proof and active read-only access;
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
The installer generates a server-local Ed25519 worker key. Distribute only its
public `.pub` file to the client policy which starts the loopback helper; the
private key remains mode 0600 under `/etc/filees`.

An existing S2 `server.json` must be extended with absolute
`worker_private_key_file` and `worker_public_key_file` paths before enabling
S3. Re-running the generic installer creates a missing keypair but never
overwrites the existing configuration.
