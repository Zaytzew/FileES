package client

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// MetadataMover is optional so clients without the explicitly enabled native
// runtime retain their existing behavior. Enabled clients NEVER fall back.
type MetadataMover interface {
	MetadataMovesEnabled() bool
	RecordMove(context.Context, string, string, string) (string, error)
	VerifyCommittedMove(context.Context, string, string, string) (bool, error)
}

func (c *execClient) MetadataMovesEnabled() bool { return c.nativeSVNPath != "" }

func missingPathLockConfirmed(raw, comment string) bool {
	if comment == "" {
		return false
	}
	var doc struct {
		Targets []struct {
			Entries []struct {
				WC struct {
					Item string   `xml:"item,attr"`
					Lock *lockXML `xml:"lock"`
				} `xml:"wc-status"`
				Repos struct {
					Lock *lockXML `xml:"lock"`
				} `xml:"repos-status"`
			} `xml:"entry"`
		} `xml:"target"`
	}
	if xml.Unmarshal([]byte(raw), &doc) != nil || len(doc.Targets) != 1 || len(doc.Targets[0].Entries) != 1 {
		return false
	}
	e := doc.Targets[0].Entries[0]
	return (e.WC.Item == "missing" || e.WC.Item == "deleted") && e.WC.Lock != nil && e.Repos.Lock != nil && e.WC.Lock.Token != "" && e.WC.Lock.Token == e.Repos.Lock.Token && e.WC.Lock.Comment == comment && e.Repos.Lock.Comment == comment
}

const (
	nativeReceiptLimit  = 64 * 1024
	nativeListingLimit  = 8 << 20
	nativePathBatch     = 512 // lockstep with FILEES_SVN_MAX_PATHS; daemon batches are 1000
)

type nativeOutput struct {
	buffer    bytes.Buffer // not embedded: io.ReaderFrom must not bypass Write
	truncated bool
	max       int
}

func (b *nativeOutput) Write(p []byte) (int, error) {
	n := len(p)
	limit := b.max
	if limit <= 0 {
		limit = nativeReceiptLimit
	}
	remaining := limit - b.buffer.Len()
	if n > remaining {
		b.truncated = true
		p = p[:remaining]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

func validMovePath(p string) bool {
	if p == "" || p == "." || path.IsAbs(p) || path.Clean(p) != p || strings.ContainsAny(p, "\\:\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || strings.EqualFold(part, ".svn") || strings.EqualFold(part, ".filees") {
			return false
		}
	}
	return true
}

// RecordMove serializes with every CLI operation on this client. Its only
// mutation is SVN's metadata-only move; no shell, remote URL or force verb.
func (c *execClient) RecordMove(ctx context.Context, wc, old, dst string) (string, error) {
	if !filepath.IsAbs(c.nativeSVNPath) || !filepath.IsAbs(wc) || !validMovePath(old) || !validMovePath(dst) || old == dst {
		return "", errors.New("native SVN move: explicit absolute executable/WC and distinct relative data paths required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	deadline := min(c.timeout, 30*time.Second)
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.nativeSVNPath, "record-move", "--wc", wc, old, dst)
	cmd.Dir = wc
	cmd.Env = svnProcessEnvironment(os.Environ(), "")
	var stdout, stderr nativeOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	started := time.Now()
	err := cmd.Run()
	if err != nil || stdout.truncated || stderr.truncated {
		return "", fmt.Errorf("native SVN move %q -> %q failed (no delete/add fallback): %v; context=%v; output-truncated=%v\n%s\n%s", old, dst, err, ctx.Err(), stdout.truncated || stderr.truncated, stdout.buffer.String(), stderr.buffer.String())
	}
	var result struct {
		Schema string
		OK     bool
		State  string
	}
	if err := json.Unmarshal(stdout.buffer.Bytes(), &result); err != nil || result.Schema != "filees.native-svn/v1" || !result.OK || (result.State != "scheduled" && result.State != "already_scheduled") {
		return "", fmt.Errorf("native SVN move returned invalid receipt (no delete/add fallback): %v\n%s\n%s", err, stdout.buffer.String(), stderr.buffer.String())
	}
	c.lg.Infof("native-move %s: %s -> %s (%s)", result.State, old, dst, time.Since(started).Round(time.Millisecond))
	return result.State, nil
}

// VerifyCommittedMove handles a commit whose reply/cache acknowledgement was
// lost. Normal status alone is not proof: require the actual last-change
// revision to contain the unique copyfrom + delete pair. Later unrelated
// edits or inconclusive history return false, leaving the intent blocked.
func (c *execClient) VerifyCommittedMove(ctx context.Context, wc, old, dst string) (bool, error) {
	if !validMovePath(old) || !validMovePath(dst) || old == dst {
		return false, errors.New("invalid move receipt paths")
	}
	info, err := c.run(ctx, wc, []string{"info", "--xml", "--", dst + "@BASE"})
	if err != nil {
		return false, err
	}
	var doc struct {
		Entry struct {
			URL        string `xml:"url"`
			Repository struct {
				Root string `xml:"root"`
			} `xml:"repository"`
			Commit struct {
				Revision int64 `xml:"revision,attr"`
			} `xml:"commit"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal([]byte(info), &doc); err != nil {
		return false, err
	}
	u, err := url.Parse(doc.Entry.URL)
	if err != nil {
		return false, err
	}
	r, err := url.Parse(doc.Entry.Repository.Root)
	if err != nil {
		return false, err
	}
	if u.Scheme != r.Scheme || u.Host != r.Host || !strings.HasPrefix(u.Path, strings.TrimSuffix(r.Path, "/")+"/") {
		return false, errors.New("invalid move receipt repository URL")
	}
	dstRepo := strings.TrimPrefix(u.Path, strings.TrimSuffix(r.Path, "/"))
	if !strings.HasSuffix(dstRepo, "/"+dst) || doc.Entry.Commit.Revision <= 0 {
		return false, nil
	}
	oldRepo := strings.TrimSuffix(dstRepo, "/"+dst) + "/" + old
	raw, err := c.run(ctx, wc, []string{"log", "--xml", "--verbose", "-r", fmt.Sprint(doc.Entry.Commit.Revision), "--", dst + "@BASE"})
	if err != nil {
		return false, err
	}
	var log struct {
		Entries []struct {
			Paths []struct {
				Path   string `xml:",chardata"`
				Action string `xml:"action,attr"`
				Copy   string `xml:"copyfrom-path,attr"`
			} `xml:"paths>path"`
		} `xml:"logentry"`
	}
	if err := xml.Unmarshal([]byte(raw), &log); err != nil {
		return false, err
	}
	if len(log.Entries) != 1 {
		return false, nil
	}
	deleted, copied, successors := false, false, 0
	for _, change := range log.Entries[0].Paths {
		if change.Path == oldRepo && change.Action == "D" {
			deleted = true
		}
		if change.Copy == oldRepo {
			successors++
			if change.Path == dstRepo && change.Action == "A" {
				copied = true
			}
		}
	}
	return deleted && copied && successors == 1, nil
}
