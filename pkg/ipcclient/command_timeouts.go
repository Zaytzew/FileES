package ipcclient

import "time"

// timeoutFor is how long a call without its own deadline waits for command.
// The default suits the interface's refresh; commands the daemon itself lets
// run longer (its context.WithTimeout per handler) are waited for that long,
// plus a margin for the answer. command_timeouts_test.go reads the daemon's
// limits from its source, so a new long command cannot be forgotten here.
func timeoutFor(command string, fallback time.Duration) time.Duration {
	if limit, ok := commandTimeouts[command]; ok && limit+commandAnswerMargin > fallback {
		return limit + commandAnswerMargin
	}
	return fallback
}

const commandAnswerMargin = 15 * time.Second

// commandTimeouts mirrors the daemon's per-command limits (pkg/ipcserver).
var commandTimeouts = map[string]time.Duration{
	"activation.begin":             20 * time.Second,
	"lock.release-request-accept":  45 * time.Second,
	"lock.release-request-dismiss": 45 * time.Second,
	"lock.release-request":         45 * time.Second,
	"mobile_pairing.begin":         20 * time.Second,
	"realm.alias_claim":            30 * time.Second,
	"realm.grant_recipients":       30 * time.Second,
	"realm.public_branding_get":    30 * time.Second,
	"realm.public_branding_set":    30 * time.Second,
	"realm.remove_begin":           2 * time.Minute,
	"realm.remove_confirm":         2 * time.Hour,
	"realm.set_visibility":         30 * time.Second,
	"recovery.download":            2 * time.Hour,
	"repo.delete":                  45 * time.Minute,
	"repo.detach":                  45 * time.Minute,
	"repo.grant_access":            30 * time.Second,
	"repo.lifecycle_repair":        2 * time.Minute,
	"repo.lock":                    30 * time.Second,
	"repo.public_share_create":     1 * time.Minute,
	"repo.public_share_delete":     1 * time.Minute,
	"repo.public_share_list":       1 * time.Minute,
	"repo.public_share_revoke":     1 * time.Minute,
	"repo.public_share_update":     1 * time.Minute,
	"repo.publish":                 30 * time.Minute,
	"repo.quarantine_fetch":        1 * time.Minute,
	"repo.quarantine_hide":         1 * time.Minute,
	"repo.quarantine_list":         1 * time.Minute,
	"repo.reservation_list":        45 * time.Second,
	"repo.reservation_release":     45 * time.Second,
	"repo.revoke_access":           30 * time.Second,
	"repo.set_editing_policy":      30 * time.Second,
	"repo.shelf_list":              1 * time.Minute,
	"repo.unlock":                  30 * time.Second,
	"repo.upload_channel_create":   1 * time.Minute,
	"repo.upload_channel_delete":   1 * time.Minute,
	"repo.upload_channel_list":     1 * time.Minute,
	"repo.upload_channel_revoke":   1 * time.Minute,
	"repo.upload_channel_update":   1 * time.Minute,
	"server.detach":                45 * time.Minute,
	"server.set_session_timeout":   15 * time.Second,
	"update.apply":                 15 * time.Minute,
	"update.plan":                  30 * time.Second,
	"update.status":                15 * time.Second,
	"whale.cancel":                 30 * time.Second,
	"whale.get":                    30 * time.Second,
	"whale.get_begin":              30 * time.Second,
	"whale.get_confirm":            30 * time.Second,
	"whale.list":                   30 * time.Second,
	"whale.put_begin":              30 * time.Second,
	"whale.retry":                  30 * time.Second,
}
