// Package serveralerts publishes bounded snapshots to an isolated service-repo
// mailbox using svnmucc. It never creates a working copy.
package serveralerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"filees/internal/svnurl"
	"filees/pkg/alertchannel"
	"github.com/google/uuid"
)

type Publisher struct{ Repository, SVNLook, SVNMucc, TempDir string }

// boundedBuffer drains excess output so a faulty helper cannot exhaust memory.
type boundedBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	left := alertchannel.MaxBytes + 1 - b.Len()
	if left < 0 {
		left = 0
	}
	if len(p) > left {
		p = p[:left]
		b.overflow = true
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
func output(ctx context.Context, bin string, args ...string) ([]byte, error) {
	var out, stderr boundedBuffer
	c := exec.CommandContext(ctx, bin, args...)
	c.Stdout = &out
	c.Stderr = &stderr
	err := c.Run()
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %s", filepath.Base(bin), err, stderr.String())
	}
	if out.overflow {
		return nil, errors.New("alert helper output exceeds limit")
	}
	return out.Bytes(), nil
}

// Publish uses optimistic SVN revision checks. Concurrent publication is safe:
// an out-of-date write is retried against a freshly read snapshot.
func (p Publisher) Publish(ctx context.Context, realm, key, severity, status, text string) (bool, error) {
	if _, err := uuid.Parse(realm); err != nil {
		return false, err
	}
	if !filepath.IsAbs(p.Repository) || !filepath.IsAbs(p.SVNLook) || !filepath.IsAbs(p.SVNMucc) {
		return false, errors.New("publisher paths must be absolute")
	}
	for attempt := 0; attempt < 3; attempt++ {
		rev, err := output(ctx, p.SVNLook, "youngest", p.Repository)
		if err != nil {
			return false, err
		}
		r, err := strconv.ParseInt(strings.TrimSpace(string(rev)), 10, 64)
		if err != nil {
			return false, err
		}
		root, err := output(ctx, p.SVNLook, "tree", "--non-recursive", "--full-paths", "-r", strconv.FormatInt(r, 10), p.Repository)
		if err != nil {
			return false, err
		}
		hasRoot := hasLine(root, "alerts/")
		hasRealm := false
		if hasRoot {
			children, e := output(ctx, p.SVNLook, "tree", "--non-recursive", "--full-paths", "-r", strconv.FormatInt(r, 10), p.Repository, "alerts")
			if e != nil {
				return false, e
			}
			hasRealm = hasLine(children, "alerts/"+realm+"/")
		}
		var s alertchannel.Snapshot
		if hasRealm {
			raw, e := output(ctx, p.SVNLook, "cat", "-r", strconv.FormatInt(r, 10), p.Repository, "alerts/"+realm+"/snapshot.json")
			if e != nil {
				return false, e
			}
			s, e = alertchannel.Decode(raw, realm)
			if e != nil {
				return false, e
			}
		}
		next, changed, err := s.Change(realm, key, severity, status, text, time.Now())
		if err != nil || !changed {
			return false, err
		}
		raw, err := json.Marshal(next)
		if err != nil {
			return false, err
		}
		f, err := os.CreateTemp(p.TempDir, "filees-alert-*.json")
		if err != nil {
			return false, err
		}
		name := f.Name()
		_, writeErr := io.Copy(f, bytes.NewReader(raw))
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			os.Remove(name)
			return false, errors.Join(writeErr, closeErr)
		}
		base := svnurl.File(p.Repository)
		// Log text is deliberately constant: svn:log can be visible at a broader
		// scope than the mailbox path. Never put incident text or realm IDs here.
		args := []string{"--non-interactive", "--no-auth-cache", "-r", strconv.FormatInt(r, 10), "-m", "FileES service snapshot"}
		if !hasRoot {
			args = append(args, "mkdir", base+"/alerts")
		}
		if !hasRealm {
			args = append(args, "mkdir", base+"/alerts/"+realm)
		}
		args = append(args, "put", name, base+"/alerts/"+realm+"/snapshot.json")
		_, err = output(ctx, p.SVNMucc, args...)
		os.Remove(name)
		if err == nil {
			return true, nil
		}
		// Retry by rereading, also handles an uncertain successful commit: Change
		// recognises the existing contents and does not publish a second incident.
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if attempt == 2 {
			return false, err
		}
	}
	return false, errors.New("alert publication failed")
}
func hasLine(raw []byte, want string) bool {
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}
