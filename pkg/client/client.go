package client

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"filees/pkg/talk"
)

// Options configures the SVN exec wrapper.
type Options struct {
	SvnPath  string        // path to 'svn' binary; default "svn"
	Timeout  time.Duration // per-command timeout; default 30m
	LogScope string        // talk scope (e.g. "svn:repoID")
}

// Client exposes the subset of SVN commands we need.
type Client interface {
	GetInfo(ctx context.Context, repoURL, username, password string) (string, error)
	Checkout(ctx context.Context, repoURL, localPath, username, password string) (string, error)
	Cleanup(ctx context.Context, localPath, username, password string) (string, error)
	Update(ctx context.Context, localPath, username, password string) (string, error)
	Status(ctx context.Context, rootDirectory string, paths []string, username, password string) ([]StatusEntry, error)
	Add(ctx context.Context, rootDirectory string, paths []string, username, password string) (string, error)
	Delete(ctx context.Context, rootDirectory string, paths []string, username, password string) (string, error)
	Commit(ctx context.Context, rootDirectory string, paths []string, message, username, password string) (string, error)
	Lock(ctx context.Context, rootDirectory string, paths []string, username, password string) (string, error)
	Unlock(ctx context.Context, rootDirectory string, paths []string, username, password string) (string, error)
	PropGet(ctx context.Context, rootDirectory, propName string, paths []string, username, password string) (string, error)
	// Revision returns the revision number for target (URL or local WC path).
	// For a remote URL it returns HEAD; for a local WC path it returns the last-updated revision.
	Revision(ctx context.Context, target, username, password string) (int64, error)
	// Resolve marks conflicts as resolved using the given accept strategy
	// (e.g. "theirs-full", "mine-full").
	Resolve(ctx context.Context, wc string, paths []string, accept, username, password string) (string, error)
}

// execClient implements Client by calling the external 'svn' executable.
type execClient struct {
	svnPath string
	timeout time.Duration
	lg      talk.Logger
	mu      sync.Mutex // serialize SVN calls within process
}

// New creates a new SVN CLI client.
func New(opts Options) Client {
	p := opts.SvnPath
	if p == "" { p = "svn" }
	t := opts.Timeout
	if t <= 0 { t = 30 * time.Minute }
	return &execClient{svnPath: p, timeout: t, lg: talk.With(opts.LogScope)}
}

// ---- Types ----

type StatusEntry struct {
	Path string
	Item string
}

// ---- High-level helpers ----

func (c *execClient) GetInfo(ctx context.Context, repoURL, username, password string) (string, error) {
	return c.run(ctx, "", username, password, []string{"info", repoURL})
}

func (c *execClient) Checkout(ctx context.Context, repoURL, localPath, username, password string) (string, error) {
	if _, err := os.Stat(filepath.Join(localPath, ".svn")); err == nil {
		c.lg.Debugf("WC exists at %s → cleanup+update", localPath)
		if out, err := c.Cleanup(ctx, localPath, username, password); err != nil { return out, err }
		return c.Update(ctx, localPath, username, password)
	}
	if err := os.MkdirAll(localPath, 0o755); err != nil { return "", err }
	return c.run(ctx, "", username, password, []string{"checkout", repoURL, localPath})
}

func (c *execClient) Cleanup(ctx context.Context, localPath, username, password string) (string, error) {
	return c.run(ctx, localPath, username, password, []string{"cleanup"})
}

func (c *execClient) Update(ctx context.Context, localPath, username, password string) (string, error) {
	return c.run(ctx, localPath, username, password, []string{"update", "."})
}

func (c *execClient) Status(ctx context.Context, rootDirectory string, paths []string, username, password string) ([]StatusEntry, error) {
	args := append([]string{"status", "--xml", "--verbose", "--ignore-externals", "--depth", "empty"}, c.relativize(rootDirectory, paths)...)
	output, err := c.run(ctx, rootDirectory, username, password, args)
	if err != nil { return nil, fmt.Errorf("svn status failed: %w\n%s", err, output) }
	return parseStatusXML(output, rootDirectory)
}

func parseStatusXML(output, rootDirectory string) ([]StatusEntry, error) {
	var statusXML struct {
		Targets []struct {
			Entries []struct {
				Path string `xml:"path,attr"`
				WCStatus struct {
					Item string `xml:"item,attr"`
				} `xml:"wc-status"`
			} `xml:"entry"`
		} `xml:"target"`
	}
	if err := xml.Unmarshal([]byte(output), &statusXML); err != nil {
		return nil, fmt.Errorf("parse status xml: %w\n%s", err, output)
	}
	var out []StatusEntry
	for _, t := range statusXML.Targets {
		for _, e := range t.Entries {
			path := e.Path
			if rel, err := filepath.Rel(rootDirectory, path); err == nil { path = rel }
			out = append(out, StatusEntry{Path: path, Item: e.WCStatus.Item})
		}
	}
	return out, nil
}

func (c *execClient) Add(ctx context.Context, rootDirectory string, paths []string, username, password string) (string, error) {
	// Keep directory expansion under the commit planner's control. --parents
	// schedules required ancestors, while --depth empty prevents a directory
	// from recursively bypassing file-count and byte limits.
	args := append([]string{"add", "--parents", "--depth", "empty"}, c.relativize(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, username, password, args)
}

func (c *execClient) Delete(ctx context.Context, rootDirectory string, paths []string, username, password string) (string, error) {
	args := append([]string{"delete"}, c.relativize(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, username, password, args)
}

func (c *execClient) Commit(ctx context.Context, rootDirectory string, paths []string, message, username, password string) (string, error) {
	if len(paths) == 0 { return "", errors.New("svn commit refused: empty path list") }
	args := append([]string{"commit", "-m", message}, c.relativize(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, username, password, args)
}

func (c *execClient) Lock(ctx context.Context, rootDirectory string, paths []string, username, password string) (string, error) {
	args := append([]string{"lock"}, c.relativize(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, username, password, args)
}

func (c *execClient) Unlock(ctx context.Context, rootDirectory string, paths []string, username, password string) (string, error) {
	args := append([]string{"unlock"}, c.relativize(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, username, password, args)
}

func (c *execClient) PropGet(ctx context.Context, rootDirectory, propName string, paths []string, username, password string) (string, error) {
	args := append([]string{"propget", propName}, c.relativize(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, username, password, args)
}

func (c *execClient) Resolve(ctx context.Context, wc string, paths []string, accept, username, password string) (string, error) {
	args := append([]string{"resolve", "--accept", accept}, c.relativize(wc, paths)...)
	return c.run(ctx, wc, username, password, args)
}

func (c *execClient) Revision(ctx context.Context, target, username, password string) (int64, error) {
	out, err := c.run(ctx, "", username, password, []string{"info", "--show-item", "revision", target})
	if err != nil { return 0, err }
	n, perr := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if perr != nil { return 0, fmt.Errorf("parse revision %q: %w", strings.TrimSpace(out), perr) }
	return n, nil
}

// ---- Core exec runner ----

func (c *execClient) run(parentCtx context.Context, workingDir, username, password string, args []string) (string, error) {
	c.mu.Lock(); defer c.mu.Unlock()

	cmdArgs := make([]string, 0, 8+len(args))
	if username != "" { cmdArgs = append(cmdArgs, "--username", username) }
	if password != "" { cmdArgs = append(cmdArgs, "--password", password) }
	cmdArgs = append(cmdArgs, "--non-interactive", "--no-auth-cache")
	cmdArgs = append(cmdArgs, args...)

	ctx, cancel := context.WithTimeout(parentCtx, c.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.svnPath, cmdArgs...)
	cmd.Dir = workingDir

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	c.lg.Tracef("exec: %s %s (dir=%s)", c.svnPath, strings.Join(cmdArgs, " "), emptyIf(workingDir, "."))
	if err := cmd.Start(); err != nil {
		name := "svn"
		if len(args) > 0 { name = args[0] }
		return buf.String(), fmt.Errorf("uruchomienie '%s' nie powiodło się: %w", name, err)
	}
	if err := cmd.Wait(); err != nil {
		name := "svn"
		if len(args) > 0 { name = args[0] }
		if ctx.Err() != nil {
			return buf.String(), fmt.Errorf("komenda '%s' anulowana/przekroczono czas: %v\n%s", name, ctx.Err(), buf.String())
		}
		return buf.String(), fmt.Errorf("komenda '%s' zakończyła się błędem: %v\n%s", name, err, buf.String())
	}
	return buf.String(), nil
}

// relativize converts absolute paths under rootDirectory into relative ones for svn CLI.
func (c *execClient) relativize(rootDirectory string, paths []string) []string {
	if len(paths) == 0 { return paths }
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		q := p
		if rootDirectory != "" && filepath.IsAbs(p) && strings.HasPrefix(filepath.Clean(p), filepath.Clean(rootDirectory)) {
			if rel, err := filepath.Rel(rootDirectory, p); err == nil { q = rel }
		}
		out = append(out, q)
	}
	return out
}

func emptyIf(s, def string) string {
	if s == "" { return def }
	return s
}

// IsNetworkError returns true when err indicates a network/connectivity problem
// (as opposed to an SVN logic error like "not under version control").
func IsNetworkError(err error) bool {
	if err == nil { return false }
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"unable to connect",
		"connection refused",
		"connection timed out",
		"network is unreachable",
		"no route to host",
		"host not found",
		"name or service not known",
		"temporary failure in name resolution",
		"e170013", // svn: can't connect to repository
		"e730047", // svn: DNS lookup failed
		"anulowana/przekroczono czas", // our timeout wrapper
	} {
		if strings.Contains(msg, needle) { return true }
	}
	return false
}
