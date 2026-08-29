// Package shout implements the shouting-commit marker and local inbox.
// The only durable content of a shout is the svn:log line; this package
// does not talk to the network.
package shout

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"filees/internal/durable"
	contract "filees/pkg/contract/v1"
)

// Marker is injected by the daemon at the start of a shouting commit message.
// A raw `svn commit -m` that includes it is still a shout: same write boundary
// as the rest of the repository.
const Marker = "[!shout@#!]"

const (
	lastSeenName = "shout.last_seen"
	inboxName    = "shouts.json"
)

var (
	ErrEmptyComment      = errors.New("shout comment must not be empty")
	ErrCommentTooLong    = errors.New("shout comment is too long")
	ErrNothingToPublish  = errors.New("no pending changes to publish")
	ErrCommentHasControl = errors.New("shout comment must not contain control characters")
)

const maxCommentRunes = 500

// acknowledgedHistoryLimit bounds durable read history without ever dropping
// an announcement that still requires an explicit acknowledgement.
const acknowledgedHistoryLimit = 50

// Format builds the svn:log message for a shouting commit.
func Format(comment string) string {
	return Marker + " " + strings.TrimSpace(comment)
}

// Parse reports whether message is a shout and returns the comment.
func Parse(message string) (comment string, ok bool) {
	trimmed := strings.TrimSpace(message)
	if !strings.HasPrefix(trimmed, Marker) {
		return "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(trimmed, Marker))
	if rest == "" {
		return "", false
	}
	return rest, true
}

// ValidateComment accepts user text that will be placed after the marker.
func ValidateComment(comment string) error {
	trimmed := strings.TrimSpace(comment)
	if trimmed == "" {
		return ErrEmptyComment
	}
	if strings.ContainsAny(trimmed, "\x00\r\n") {
		return ErrCommentHasControl
	}
	if len([]rune(trimmed)) > maxCommentRunes {
		return ErrCommentTooLong
	}
	return nil
}

// NoticeID is stable across restarts: one shout per repository revision.
func NoticeID(repoID string, revision int64) string {
	return fmt.Sprintf("shout:%s:%d", repoID, revision)
}

// LoadLastSeen returns the last scanned revision. ok is false when the
// installation has never recorded one (first checkout).
func LoadLastSeen(wc string) (int64, bool, error) {
	data, err := os.ReadFile(lastSeenPath(wc))
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || n < 0 {
		return 0, false, fmt.Errorf("shout last_seen: %w", err)
	}
	return n, true, nil
}

// SaveLastSeen persists the high-water mark after a scan or after this
// client authors a shout.
func SaveLastSeen(wc string, revision int64) error {
	if revision < 0 {
		return fmt.Errorf("shout last_seen: negative revision")
	}
	return atomicWrite(lastSeenPath(wc), []byte(strconv.FormatInt(revision, 10)+"\n"))
}

// Record is one discovered shout in the local inbox.
type Record struct {
	ID        string `json:"id"`
	RepoID    string `json:"repo_id"`
	Revision  int64  `json:"revision"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	Acked     bool   `json:"acked"`
}

// LoadInbox reads the durable inbox. Missing file is an empty inbox.
func LoadInbox(wc string) ([]Record, error) {
	data, err := os.ReadFile(inboxPath(wc))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []Record
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("shout inbox: %w", err)
	}
	return records, nil
}

// SaveInbox replaces the durable inbox.
func SaveInbox(wc string, records []Record) error {
	if records == nil {
		records = []Record{}
	}
	raw, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return atomicWrite(inboxPath(wc), raw)
}

// Remember appends shouts that are not already in the inbox. Returns the
// newly added records (for events).
func Remember(wc, repoID string, entries []LogEntry, now time.Time) ([]Record, error) {
	inbox, err := LoadInbox(wc)
	if err != nil {
		return nil, err
	}
	have := make(map[int64]bool, len(inbox))
	for _, rec := range inbox {
		have[rec.Revision] = true
	}
	var added []Record
	stamp := now.UTC().Format(time.RFC3339)
	for _, entry := range entries {
		comment, ok := Parse(entry.Message)
		if !ok || have[entry.Revision] {
			continue
		}
		rec := Record{
			ID:        NoticeID(repoID, entry.Revision),
			RepoID:    repoID,
			Revision:  entry.Revision,
			Title:     comment,
			CreatedAt: stamp,
		}
		inbox = append(inbox, rec)
		added = append(added, rec)
		have[entry.Revision] = true
	}
	if len(added) == 0 {
		return nil, nil
	}
	if err := SaveInbox(wc, inbox); err != nil {
		return nil, err
	}
	return added, nil
}

// Ack marks a notice acknowledged. Unknown IDs are a no-op.
func Ack(wc, noticeID string) error {
	inbox, err := LoadInbox(wc)
	if err != nil {
		return err
	}
	changed := false
	for i := range inbox {
		if inbox[i].ID == noticeID && !inbox[i].Acked {
			inbox[i].Acked = true
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return SaveInbox(wc, pruneAcknowledged(inbox, acknowledgedHistoryLimit))
}

// OpenNotices returns unacked records as contract notices, newest last.
func OpenNotices(wc string) ([]contract.Notice, error) {
	inbox, err := LoadInbox(wc)
	if err != nil {
		return nil, err
	}
	out := make([]contract.Notice, 0, len(inbox))
	for _, rec := range inbox {
		if rec.Acked {
			continue
		}
		out = append(out, contract.Notice{
			ID:        rec.ID,
			RepoID:    rec.RepoID,
			Revision:  rec.Revision,
			CreatedAt: rec.CreatedAt,
			Title:     rec.Title,
			Acked:     false,
		})
	}
	return out, nil
}

// RecentNotices returns every unread announcement and a bounded tail of the
// acknowledged history. The daemon performs the final cross-repository sort
// and limit; this function deliberately never lets read history evict unread
// records.
func RecentNotices(wc string, acknowledgedLimit int) ([]contract.Notice, error) {
	inbox, err := LoadInbox(wc)
	if err != nil {
		return nil, err
	}
	if acknowledgedLimit < 0 {
		acknowledgedLimit = 0
	}
	keepAcknowledged := make(map[int]bool, acknowledgedLimit)
	for i, kept := len(inbox)-1, 0; i >= 0 && kept < acknowledgedLimit; i-- {
		if inbox[i].Acked {
			keepAcknowledged[i] = true
			kept++
		}
	}
	out := make([]contract.Notice, 0, len(inbox))
	for i, rec := range inbox {
		if rec.Acked && !keepAcknowledged[i] {
			continue
		}
		out = append(out, contract.Notice{
			ID: rec.ID, RepoID: rec.RepoID, Revision: rec.Revision,
			CreatedAt: rec.CreatedAt, Title: rec.Title, Acked: rec.Acked,
		})
	}
	return out, nil
}

func pruneAcknowledged(inbox []Record, limit int) []Record {
	if limit < 0 {
		limit = 0
	}
	keep := make([]bool, len(inbox))
	for i, kept := len(inbox)-1, 0; i >= 0; i-- {
		if !inbox[i].Acked {
			keep[i] = true
			continue
		}
		if kept < limit {
			keep[i] = true
			kept++
		}
	}
	result := make([]Record, 0, len(inbox))
	for i := range inbox {
		if keep[i] {
			result = append(result, inbox[i])
		}
	}
	return result
}

// LogEntry is one svn log message (no changed-paths).
type LogEntry struct {
	Revision int64
	Message  string
}

// FetchLogs loads svn:log for an inclusive revision range.
type FetchLogs func(fromRev, toRev int64) ([]LogEntry, error)

// Advance updates last_seen and the inbox after the working copy revision
// moves forward. A missing last_seen is initialized to localRev without
// scanning history (new installation).
func Advance(wc, repoID string, localRev int64, fetch FetchLogs, now time.Time) ([]Record, error) {
	if localRev < 1 {
		return nil, nil
	}
	last, ok, err := LoadLastSeen(wc)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, SaveLastSeen(wc, localRev)
	}
	if localRev <= last {
		return nil, nil
	}
	var entries []LogEntry
	if fetch != nil {
		entries, err = fetch(last+1, localRev)
		if err != nil {
			return nil, err
		}
	}
	added, err := Remember(wc, repoID, entries, now)
	if err != nil {
		return nil, err
	}
	if err := SaveLastSeen(wc, localRev); err != nil {
		return nil, err
	}
	return added, nil
}

func lastSeenPath(wc string) string {
	return filepath.Join(wc, ".filees", "state", lastSeenName)
}

func inboxPath(wc string) string {
	return filepath.Join(wc, ".filees", "state", inboxName)
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".filees-state-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return durable.SyncDirectory(dir)
}
