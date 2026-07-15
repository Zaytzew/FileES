# FileES server toolchain bundle

This bundle contains short-lived tools, not services:

- `filees-admin` and `filees-operation` are administrative commands;
- `filees-onboard` is the future forced command invoked by system `sshd`;
- `filees-mail` submits one pending outbox entry to a configured smarthost.

Run `install-server.sh` as the target system administrator, edit
`/etc/filees/server.json`, and keep both configuration and OTP pepper private.
The installer deliberately does not create an `rc.d`/systemd service and does
not modify `sshd_config`. OpenBSD SSH integration is the separate S2 stage.
