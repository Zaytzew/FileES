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
	SvnPath         string        // path to 'svn' binary; default "svn"
	Timeout         time.Duration // per-command timeout; default 30m
	LogScope        string        // talk scope (e.g. "svn:repoID")
	SSHIdentityFile string        // absolute installation Ed25519 private key
	SSHKnownHosts   string        // absolute pinned known_hosts file
	SSHPort         int           // OpenSSH port; zero means the default port 22
}

// Client exposes the subset of SVN commands we need.
type Client interface {
	GetInfo(ctx context.Context, repoURL string) (string, error)
	Checkout(ctx context.Context, repoURL, localPath string) (string, error)
	Cleanup(ctx context.Context, localPath string) (string, error)
	Update(ctx context.Context, localPath string) (string, error)
	UpdateDepthEmpty(ctx context.Context, rootDirectory string, paths []string) (string, error)
	Status(ctx context.Context, rootDirectory string, paths []string) ([]StatusEntry, error)
	Add(ctx context.Context, rootDirectory string, paths []string) (string, error)
	Delete(ctx context.Context, rootDirectory string, paths []string) (string, error)
	Commit(ctx context.Context, rootDirectory string, paths []string, message string) (string, error)
	CommitKeepLocks(ctx context.Context, rootDirectory string, paths []string, message string) (string, error)
	Lock(ctx context.Context, rootDirectory string, paths []string) (string, error)
	LockWithComment(ctx context.Context, rootDirectory string, paths []string, comment string, force bool) (string, error)
	Unlock(ctx context.Context, rootDirectory string, paths []string) (string, error)
	LockInfo(ctx context.Context, rootDirectory, path string) (*LockInfo, error)
	PropGet(ctx context.Context, rootDirectory, propName string, paths []string) (string, error)
	PropSet(ctx context.Context, rootDirectory, propName, value string, paths []string) (string, error)
	PropList(ctx context.Context, rootDirectory, propName string) (map[string]bool, error)
	// Revision returns the revision number for target (URL or local WC path).
	// For a remote URL it returns HEAD; for a local WC path it returns the last-updated revision.
	Revision(ctx context.Context, target string) (int64, error)
	// Resolve marks conflicts as resolved using the given accept strategy
	// (e.g. "theirs-full", "mine-full").
	Resolve(ctx context.Context, wc string, paths []string, accept string) (string, error)
}

// execClient implements Client by calling the external 'svn' executable.
type execClient struct {
	svnPath    string
	timeout    time.Duration
	lg         talk.Logger
	sshCommand string
	mu         sync.Mutex // serialize SVN calls within process
}

// New creates a new SVN CLI client.
func New(opts Options) Client {
	p := opts.SvnPath
	if p == "" {
		p = "svn"
	}
	t := opts.Timeout
	if t <= 0 {
		t = 30 * time.Minute
	}
	sshCommand := ""
	if opts.SSHIdentityFile != "" || opts.SSHKnownHosts != "" {
		sshCommand = buildSSHCommand(opts.SSHIdentityFile, opts.SSHKnownHosts, opts.SSHPort)
	}
	return &execClient{svnPath: p, timeout: t, lg: talk.With(opts.LogScope), sshCommand: sshCommand}
}

func buildSSHCommand(identityFile, knownHosts string, port int) string {
	// Config validates these as absolute deployment-owned paths. Rejecting
	// whitespace here avoids relying on shell quoting in SVN's tunnel parser.
	for _, path := range []string{identityFile, knownHosts} {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, " \t\r\n") {
			return ""
		}
	}
	if port < 0 || port > 65535 {
		return ""
	}
	args := []string{
		"ssh", "-F", "/dev/null", "-T", "-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none",
		"-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no",
		"-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "HostKeyAlgorithms=ssh-ed25519", "-i", identityFile,
	}
	if port > 0 {
		args = append(args, "-p", strconv.Itoa(port))
	}
	return strings.Join(args, " ")
}

// ---- Types ----

type StatusEntry struct {
	Path  string
	Item  string
	Props string
}

// LockInfo is the repository lock attached to a working-copy path. Token is
// the authoritative fencing token issued by Subversion.
type LockInfo struct {
	Token   string
	Owner   string
	Comment string
	Created time.Time
}

// ---- High-level helpers ----

func (c *execClient) GetInfo(ctx context.Context, repoURL string) (string, error) {
	return c.run(ctx, "", []string{"info", repoURL})
}

func (c *execClient) Checkout(ctx context.Context, repoURL, localPath string) (string, error) {
	if _, err := os.Stat(filepath.Join(localPath, ".svn")); err == nil {
		c.lg.Debugf("WC exists at %s → cleanup+update", localPath)
		if out, err := c.Cleanup(ctx, localPath); err != nil {
			return out, err
		}
		return c.Update(ctx, localPath)
	}
	if err := os.MkdirAll(localPath, 0o755); err != nil {
		return "", err
	}
	return c.run(ctx, "", []string{"checkout", repoURL, localPath})
}

func (c *execClient) Cleanup(ctx context.Context, localPath string) (string, error) {
	return c.run(ctx, localPath, []string{"cleanup"})
}

func (c *execClient) Update(ctx context.Context, localPath string) (string, error) {
	return c.run(ctx, localPath, []string{"update", "."})
}

func (c *execClient) UpdateDepthEmpty(ctx context.Context, rootDirectory string, paths []string) (string, error) {
	args := append([]string{"update", "--depth", "empty"}, c.relativize(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

func (c *execClient) Status(ctx context.Context, rootDirectory string, paths []string) ([]StatusEntry, error) {
	depth := "empty"
	if len(paths) == 0 {
		depth = "infinity"
	}
	args := append([]string{"status", "--xml", "--verbose", "--ignore-externals", "--depth", depth}, c.relativize(rootDirectory, paths)...)
	output, err := c.run(ctx, rootDirectory, args)
	if err != nil {
		return nil, fmt.Errorf("svn status failed: %w\n%s", err, output)
	}
	return parseStatusXML(output, rootDirectory)
}

// HasMissingPaths reports local, unscheduled removals. Running svn update while
// any such path exists restores it from the repository, destroying the user's
// delete and degrading a rename into an add/copy pair.
func HasMissingPaths(entries []StatusEntry) bool {
	for _, entry := range entries {
		if entry.Item == "missing" {
			return true
		}
	}
	return false
}

func parseStatusXML(output, rootDirectory string) ([]StatusEntry, error) {
	var statusXML struct {
		Targets []struct {
			Entries []struct {
				Path     string `xml:"path,attr"`
				WCStatus struct {
					Item  string `xml:"item,attr"`
					Props string `xml:"props,attr"`
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
			if rel, err := filepath.Rel(rootDirectory, path); err == nil {
				path = rel
			}
			out = append(out, StatusEntry{Path: path, Item: e.WCStatus.Item, Props: e.WCStatus.Props})
		}
	}
	return out, nil
}

func (c *execClient) Add(ctx context.Context, rootDirectory string, paths []string) (string, error) {
	// Keep directory expansion under the commit planner's control. --parents
	// schedules required ancestors, while --depth empty prevents a directory
	// from recursively bypassing file-count and byte limits.
	args := append([]string{"add", "--parents", "--depth", "empty"}, c.relativize(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

func (c *execClient) Delete(ctx context.Context, rootDirectory string, paths []string) (string, error) {
	args := append([]string{"delete"}, c.relativize(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

func (c *execClient) Commit(ctx context.Context, rootDirectory string, paths []string, message string) (string, error) {
	if len(paths) == 0 {
		return "", errors.New("svn commit refused: empty path list")
	}
	args := append([]string{"commit", "-m", message}, c.relativize(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

func (c *execClient) CommitKeepLocks(ctx context.Context, rootDirectory string, paths []string, message string) (string, error) {
	if len(paths) == 0 {
		return "", errors.New("svn commit refused: empty path list")
	}
	args := append([]string{"commit", "--no-unlock", "-m", message}, c.relativize(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

func (c *execClient) Lock(ctx context.Context, rootDirectory string, paths []string) (string, error) {
	args := append([]string{"lock"}, c.relativize(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

func (c *execClient) LockWithComment(ctx context.Context, rootDirectory string, paths []string, comment string, force bool) (string, error) {
	args := []string{"lock", "--message", comment}
	if force {
		args = append(args, "--force")
	}
	args = append(args, c.relativize(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

func (c *execClient) Unlock(ctx context.Context, rootDirectory string, paths []string) (string, error) {
	args := append([]string{"unlock"}, c.relativize(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

func (c *execClient) LockInfo(ctx context.Context, rootDirectory, path string) (*LockInfo, error) {
	targets := c.relativize(rootDirectory, []string{path})
	if len(targets) != 1 {
		return nil, errors.New("svn info lock requires exactly one path")
	}
	out, err := c.run(ctx, rootDirectory, []string{"info", "--xml", targets[0]})
	if err != nil {
		return nil, err
	}
	return parseLockInfoXML(out)
}

func parseLockInfoXML(output string) (*LockInfo, error) {
	var infoXML struct {
		Entry struct {
			Lock *struct {
				Token   string `xml:"token"`
				Owner   string `xml:"owner"`
				Comment string `xml:"comment"`
				Created string `xml:"created"`
			} `xml:"lock"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal([]byte(output), &infoXML); err != nil {
		return nil, fmt.Errorf("parse info xml: %w", err)
	}
	if infoXML.Entry.Lock == nil {
		return nil, nil
	}
	created, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(infoXML.Entry.Lock.Created))
	if err != nil {
		return nil, fmt.Errorf("parse lock created time: %w", err)
	}
	return &LockInfo{
		Token:   strings.TrimSpace(infoXML.Entry.Lock.Token),
		Owner:   strings.TrimSpace(infoXML.Entry.Lock.Owner),
		Comment: infoXML.Entry.Lock.Comment,
		Created: created,
	}, nil
}

func (c *execClient) PropGet(ctx context.Context, rootDirectory, propName string, paths []string) (string, error) {
	args := append([]string{"propget", propName}, c.relativize(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

func (c *execClient) PropSet(ctx context.Context, rootDirectory, propName, value string, paths []string) (string, error) {
	if len(paths) == 0 {
		return "", errors.New("svn propset refused: empty path list")
	}
	args := append([]string{"propset", propName, value}, c.relativize(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

func (c *execClient) PropList(ctx context.Context, rootDirectory, propName string) (map[string]bool, error) {
	out, err := c.run(ctx, rootDirectory, []string{"propget", "--xml", "--recursive", propName, "."})
	if err != nil {
		return nil, err
	}
	return parsePropGetXML(out, rootDirectory)
}

func parsePropGetXML(output, rootDirectory string) (map[string]bool, error) {
	var propsXML struct {
		Targets []struct {
			Path     string `xml:"path,attr"`
			Property []struct {
				Name string `xml:"name,attr"`
			} `xml:"property"`
		} `xml:"target"`
	}
	if err := xml.Unmarshal([]byte(output), &propsXML); err != nil {
		return nil, fmt.Errorf("parse propget xml: %w", err)
	}
	out := make(map[string]bool)
	for _, target := range propsXML.Targets {
		if len(target.Property) == 0 {
			continue
		}
		path := target.Path
		if filepath.IsAbs(path) {
			if rel, err := filepath.Rel(rootDirectory, path); err == nil {
				path = rel
			}
		}
		path = filepath.ToSlash(filepath.Clean(path))
		if path != "." {
			out[path] = true
		}
	}
	return out, nil
}

func (c *execClient) Resolve(ctx context.Context, wc string, paths []string, accept string) (string, error) {
	args := append([]string{"resolve", "--accept", accept}, c.relativize(wc, paths)...)
	return c.run(ctx, wc, args)
}

func (c *execClient) Revision(ctx context.Context, target string) (int64, error) {
	out, err := c.run(ctx, "", []string{"info", "--show-item", "revision", target})
	if err != nil {
		return 0, err
	}
	n, perr := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if perr != nil {
		return 0, fmt.Errorf("parse revision %q: %w", strings.TrimSpace(out), perr)
	}
	return n, nil
}

// ---- Core exec runner ----

func (c *execClient) run(parentCtx context.Context, workingDir string, args []string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, arg := range args {
		if strings.HasPrefix(arg, "svn+ssh://") && c.sshCommand == "" {
			return "", errors.New("svn+ssh transport requires an installation identity and pinned known_hosts")
		}
		// Subversion also uses '@' to introduce a peg revision.  A URL with
		// SSH userinfo must therefore carry an empty peg revision explicitly;
		// otherwise `_filees-client@host` is parsed as URL + peg `host`.
		if strings.HasPrefix(arg, "svn+ssh://") && strings.Contains(strings.TrimPrefix(arg, "svn+ssh://"), "@") && !strings.HasSuffix(arg, "@") {
			args[i] = arg + "@"
		}
	}

	cmdArgs := make([]string, 0, 8+len(args))
	cmdArgs = append(cmdArgs, "--non-interactive", "--no-auth-cache")
	cmdArgs = append(cmdArgs, args...)

	ctx, cancel := context.WithTimeout(parentCtx, c.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.svnPath, cmdArgs...)
	cmd.Dir = workingDir
	if c.sshCommand != "" {
		cmd.Env = append(os.Environ(), "SVN_SSH="+c.sshCommand)
	}

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	c.lg.Tracef("exec: %s %s (dir=%s)", c.svnPath, strings.Join(cmdArgs, " "), emptyIf(workingDir, "."))
	if err := cmd.Start(); err != nil {
		name := "svn"
		if len(args) > 0 {
			name = args[0]
		}
		return buf.String(), fmt.Errorf("uruchomienie '%s' nie powiodło się: %w", name, err)
	}
	if err := cmd.Wait(); err != nil {
		name := "svn"
		if len(args) > 0 {
			name = args[0]
		}
		if ctx.Err() != nil {
			return buf.String(), fmt.Errorf("komenda '%s' anulowana/przekroczono czas: %v\n%s", name, ctx.Err(), buf.String())
		}
		return buf.String(), fmt.Errorf("komenda '%s' zakończyła się błędem: %v\n%s", name, err, buf.String())
	}
	return buf.String(), nil
}

// relativize converts absolute paths under rootDirectory into relative ones for svn CLI.
func (c *execClient) relativize(rootDirectory string, paths []string) []string {
	if len(paths) == 0 {
		return paths
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		q := p
		if rootDirectory != "" && filepath.IsAbs(p) {
			if rel, err := filepath.Rel(filepath.Clean(rootDirectory), filepath.Clean(p)); err == nil && pathInsideRoot(rel) {
				q = rel
			}
		}
		out = append(out, q)
	}
	return out
}

func pathInsideRoot(rel string) bool {
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func emptyIf(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// IsNetworkError returns true when err indicates a network/connectivity problem
// (as opposed to an SVN logic error like "not under version control").
func IsNetworkError(err error) bool {
	if err == nil {
		return false
	}
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
		"e170013",                     // svn: can't connect to repository
		"e730047",                     // svn: DNS lookup failed
		"anulowana/przekroczono czas", // our timeout wrapper
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
