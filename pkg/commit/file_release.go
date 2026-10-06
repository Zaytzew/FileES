package commit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// The daemon owns the contents and exact recovery proof. Presence alone is
// sufficient to fence ordinary SVN writers, even in a later nocfapi build.
// No TTL, parser error, unsupported provider or restart clears this fence.
const FileReleasePendingName = "explorer-release.pending.json"

var ErrFileReleasePending = errors.New("local file release recovery is pending")

func HasPendingFileRelease(wc string) bool {
	_, err := os.Lstat(filepath.Join(wc, ".filees", "state", FileReleasePendingName))
	return !errors.Is(err, os.ErrNotExist)
}

func fileReleasePath(rel string) bool {
	if rel == "" || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.ContainsAny(rel, "\\:\x00\r\n\t") ||
		filepath.ToSlash(filepath.Clean(rel)) != rel || strings.HasPrefix(rel, "../") {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if strings.EqualFold(part, ".svn") || strings.EqualFold(part, ".filees") {
			return false
		}
	}
	return true
}

// Internal callback only, never arbitrary user-supplied code or an IPC verb.
// Recovery bypasses ordinary status solely to permit the exact private journal
// inspection under the same lock. It must independently prove every effect.
func (s *Service) WithFileRelease(ctx context.Context, wc, rel string, recovery bool, operation func() error, completion ...func() error) error {
	if !fileReleasePath(rel) || operation == nil || len(completion) > 1 {
		return errors.New("invalid file release operation")
	}
	leave, err := s.Pause.Enter(ctx, s.repoID, false)
	if err != nil {
		return err
	}
	defer leave()
	release, err := s.Admission.EnterContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	s.wcOpMu.Lock()
	defer s.wcOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.wc != wc || s.commitWriterBlocked || s.Cli == nil || HasUnresolvedCommit(wc) {
		return ErrLocalStatusBusy
	}
	if HasPendingFileRelease(wc) != recovery {
		return ErrFileReleasePending
	}
	if !recovery {
		s.mu.Lock()
		blocked := false
		for path, item := range s.staging {
			related := func(path string) bool {
				return path != "" && (path == rel || strings.HasPrefix(rel, path+"/") || strings.HasPrefix(path, rel+"/"))
			}
			if related(path) || (item != nil && related(item.OldRel)) {
				blocked = true
				break
			}
		}
		s.mu.Unlock()
		if blocked {
			return ErrLocalStatusBusy
		}
		rows, err := s.Cli.Status(ctx, wc, []string{rel})
		if err != nil {
			return err
		}
		if len(rows) != 1 || filepath.ToSlash(rows[0].Path) != rel || rows[0].Item != "normal" ||
			(rows[0].Props != "normal" && rows[0].Props != "none") || rows[0].Conflicted {
			return errors.New("file release requires an explicitly clean published file")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// RW attaches the real scanner. RO has no publication scanner and uses
	// only this writer guard. operation includes reaped helper acknowledgments.
	if s.FenceFileRelease != nil {
		err = s.FenceFileRelease(ctx, rel, operation)
	} else {
		err = operation()
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Scanner acknowledgement is already durable. The pending marker still
	// fences every new scan/hash while completion runs under this writer lock.
	if len(completion) == 1 && completion[0] != nil {
		return completion[0]()
	}
	return nil
}
