package recipientotp

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/repoworker"
	"filees/public-shares/channel"
	"filees/public-shares/storage"
	"github.com/google/uuid"
)

// Sweep shares RequestCode/Verify's lock. Channel and invitation authority is
// read inside that lock, so a queued old request cannot recreate deleted state.
func (s Service) Sweep(ctx context.Context, now time.Time) (storage.SweepResult, error) {
	var result storage.SweepResult
	if err := s.validate(); err != nil {
		return result, err
	}
	info, err := os.Lstat(s.Root)
	if err != nil {
		return result, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, errors.New("OTP root is not a plain directory")
	}
	err = repoworker.TryWithFileLock(filepath.Join(s.Root, ".lock"), func() error {
		root, err := os.OpenRoot(s.Root)
		if err != nil {
			return err
		}
		defer root.Close()
		dir, err := root.Open(".")
		if err != nil {
			return err
		}
		dirs, err := dir.ReadDir(-1)
		dir.Close()
		if err != nil {
			return err
		}
		var failures error
		for _, entry := range dirs {
			if err := ctx.Err(); err != nil {
				return errors.Join(failures, err)
			}
			id := entry.Name()
			if parsed, e := uuid.Parse(id); e != nil || parsed.String() != id {
				continue
			}
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				failures = errors.Join(failures, fmt.Errorf("OTP channel is not a plain directory: %s", id))
				continue
			}
			child, err := root.OpenRoot(id)
			if err != nil {
				failures = errors.Join(failures, err)
				continue
			}
			removed, e := s.sweepChannel(ctx, child, id, now)
			child.Close()
			result.Add(removed)
			failures = errors.Join(failures, e)
			// Remove only an empty known channel directory. Unknown files remain.
			childDir, e := root.Open(id)
			if e == nil {
				remaining, re := childDir.ReadDir(1)
				childDir.Close()
				if re != nil && !errors.Is(re, io.EOF) {
					failures = errors.Join(failures, re)
				}
				if len(remaining) == 0 && (re == nil || errors.Is(re, io.EOF)) {
					failures = errors.Join(failures, root.Remove(id))
				}
			} else {
				failures = errors.Join(failures, e)
			}
		}
		return failures
	})
	return result, err
}

func (s Service) sweepChannel(ctx context.Context, root *os.Root, id string, now time.Time) (storage.SweepResult, error) {
	var result storage.SweepResult
	dir, err := root.Open(".")
	if err != nil {
		return result, err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return result, err
	}
	var failures error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(failures, err)
		}
		name := entry.Name()
		temporary := storage.TempName(name, ".otp-")
		digest := strings.TrimSuffix(name, ".json")
		hash, hexErr := hex.DecodeString(digest)
		if !temporary && (name == digest || hexErr != nil || len(hash) != 32) {
			continue
		}
		if !entry.Type().IsRegular() {
			failures = errors.Join(failures, errors.New("non-regular OTP state"))
			continue
		}
		if !temporary {
			info, err := entry.Info()
			if err != nil {
				failures = errors.Join(failures, err)
				continue
			}
			if info.Size() > 16<<10 {
				failures = errors.Join(failures, errors.New("oversized OTP maintenance state"))
				continue
			}
			raw, err := root.ReadFile(name)
			if err != nil {
				failures = errors.Join(failures, err)
				continue
			}
			var record state
			if len(raw) > 16<<10 || json.Unmarshal(raw, &record) != nil || record.Schema != stateSchema || record.ChannelID != id || record.InvitationHash != digest || record.ExpiresAt.IsZero() {
				failures = errors.Join(failures, errors.New("invalid OTP maintenance state"))
				continue
			}
			if now.Before(record.ExpiresAt) {
				live, err := s.invitationLive(id, digest)
				if err != nil {
					failures = errors.Join(failures, err)
					continue
				}
				if live {
					result.Active++
					continue
				}
			}
		}
		removed, err := storage.RemoveRegular(root, name)
		result.Add(removed)
		if err == nil && !temporary && removed.Files != 0 {
			result.Entries++
		}
		failures = errors.Join(failures, err)
	}
	return result, failures
}
func (s Service) invitationLive(id, digest string) (bool, error) {
	record, err := s.Channels.Load(id)
	state, recipients := record.State, record.Recipients
	if errors.Is(err, channel.ErrNotFound) {
		upload, e := s.Channels.GetUpload(id)
		err = e
		state, recipients = upload.State, upload.Recipients
	}
	if errors.Is(err, channel.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if state != "active" {
		return false, nil
	}
	for _, recipient := range recipients {
		if recipient.TokenHash == digest {
			return true, nil
		}
	}
	return false, nil
}
