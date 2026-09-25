package actions

import "time"

// SetUpdateDownloadPoll shortens the wait between plan requests while a
// release downloads, and returns the previous value.
func SetUpdateDownloadPoll(interval time.Duration) time.Duration {
	previous := updateDownloadPoll
	updateDownloadPoll = interval
	return previous
}
