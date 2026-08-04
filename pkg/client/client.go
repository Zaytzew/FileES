package client

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"filees/pkg/talk"
	"github.com/google/uuid"
)

// Options configures the SVN exec wrapper.
type Options struct {
	SvnPath         string        // path to 'svn' binary; default "svn"
	Timeout         time.Duration // per-command timeout; default 30m
	LogScope        string        // talk scope (e.g. "svn:repoID")
	SSHIdentityFile string        // absolute installation Ed25519 private key
	SSHKnownHosts   string        // absolute pinned known_hosts file
	SSHPort         int           // OpenSSH port; zero means the default port 22
	SSHHostName     string        // optional connection host overriding the hostname in a canonical URL
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
		sshCommand = buildSSHCommand(opts.SSHIdentityFile, opts.SSHKnownHosts, opts.SSHPort, opts.SSHHostName)
	}
	return &execClient{svnPath: p, timeout: t, lg: talk.With(opts.LogScope), sshCommand: sshCommand}
}

func buildSSHCommand(identityFile, knownHosts string, port int, connectHost ...string) string {
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
	// Subversion reads this whole string from SVN_SSH and unescapes it before
	// splitting, so a Windows path arrives at ssh with its separators eaten:
	// C:\Users\...\id_ed25519 becomes C:UsersDELL...id_ed25519 and both the
	// identity and the pinned known_hosts silently fail to open. Measured
	// against a real server — ssh then reported "Identity file ... not
	// accessible" and "No ED25519 host key is known", and every projection
	// sync failed. OpenSSH accepts forward slashes on Windows, and ToSlash is
	// a no-op everywhere else, so this is the whole fix. Doubling the
	// backslashes would work too but leaves the escaping rule spread across
	// two layers.
	identityFile = filepath.ToSlash(identityFile)
	knownHosts = filepath.ToSlash(knownHosts)
	hostName := ""
	if len(connectHost) > 0 {
		hostName = strings.TrimSpace(connectHost[0])
		if hostName != "" {
			if host, _, err := net.SplitHostPort(hostName); err == nil {
				hostName = host
			}
			hostName = strings.Trim(hostName, "[]")
			if hostName == "" || strings.ContainsAny(hostName, " \t\r\n\x00") {
				return ""
			}
		}
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
	if hostName != "" {
		hostKeyAlias := hostName
		if port > 0 && port != 22 {
			hostKeyAlias = fmt.Sprintf("[%s]:%d", hostName, port)
		}
		args = append(args, "-o", "HostName="+hostName, "-o", "HostKeyAlias="+hostKeyAlias)
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

// LockEntry is one live repository lock found while scanning a working copy.
// Path is relative to RootDirectory and uses the native filesystem separator;
// callers converting it to a protocol value must normalize it deliberately.
// LocalItem/LocalProps are SVN's current local status for the same path and
// allow the daemon to require an explicit user acknowledgement before release.
type LockEntry struct {
	Path string
	LockInfo
	LocalItem, LocalProps string
}

// LockLister is intentionally optional: keeping it separate from Client
// avoids making every lightweight test/client implementation pretend it can
// enumerate repository locks.  The production exec client implements it.
type LockLister interface {
	ListLocks(ctx context.Context, rootDirectory string) ([]LockEntry, error)
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
	// --force preserves unversioned content already present in a directory.
	// This is required when a newly provisioned repository adopts an existing
	// local tree; Subversion still refuses conflicting versioned obstructions.
	return c.run(ctx, "", []string{"checkout", "--force", repoURL, localPath})
}

func (c *execClient) Cleanup(ctx context.Context, localPath string) (string, error) {
	return c.run(ctx, localPath, []string{"cleanup"})
}

func (c *execClient) Update(ctx context.Context, localPath string) (string, error) {
	return c.run(ctx, localPath, []string{"update", "."})
}

// Revert removes local scheduling metadata for selected paths. It is kept off
// the broad Client interface because only restart recovery needs it.
func (c *execClient) Revert(ctx context.Context, rootDirectory string, paths []string) (string, error) {
	args := append([]string{"revert"}, c.pathArgs(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

func (c *execClient) UpdateDepthEmpty(ctx context.Context, rootDirectory string, paths []string) (string, error) {
	args := append([]string{"update", "--depth", "empty"}, c.pathArgs(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

func (c *execClient) Status(ctx context.Context, rootDirectory string, paths []string) ([]StatusEntry, error) {
	depth := "empty"
	if len(paths) == 0 {
		depth = "infinity"
	}
	args := append([]string{"status", "--xml", "--verbose", "--ignore-externals", "--depth", depth}, c.pathArgs(rootDirectory, paths)...)
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
	args := append([]string{"add", "--parents", "--depth", "empty"}, c.pathArgs(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

func (c *execClient) Delete(ctx context.Context, rootDirectory string, paths []string) (string, error) {
	args := append([]string{"delete"}, c.pathArgs(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

func (c *execClient) Commit(ctx context.Context, rootDirectory string, paths []string, message string) (string, error) {
	if len(paths) == 0 {
		return "", errors.New("svn commit refused: empty path list")
	}
	args := append([]string{"commit", "-m", message}, c.pathArgs(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

func (c *execClient) CommitKeepLocks(ctx context.Context, rootDirectory string, paths []string, message string) (string, error) {
	if len(paths) == 0 {
		return "", errors.New("svn commit refused: empty path list")
	}
	args := append([]string{"commit", "--no-unlock", "-m", message}, c.pathArgs(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

// CommitWithRevision commits paths and returns the exact repository revision
// created by this transaction. A WC root can remain at an older base revision
// after committing one of its children, so `svn info --show-item revision
// <wc-root>` is not a valid commit receipt. The per-transaction revprop is
// machine-readable, locale-independent, survives deletion-only commits, and
// disambiguates this commit from concurrent writers.
//
// This method intentionally remains an optional extension to Client: packages
// that only need the historical Commit API do not have to implement receipt
// lookup, while pkg/commit uses it whenever the real exec client is present.
func (c *execClient) CommitWithRevision(ctx context.Context, rootDirectory, repoURL string, paths []string, message string, keepLocks bool) (string, int64, error) {
	if len(paths) == 0 {
		return "", 0, errors.New("svn commit refused: empty path list")
	}
	if strings.TrimSpace(repoURL) == "" {
		return "", 0, errors.New("svn commit receipt requires repository URL")
	}
	headBefore, err := c.Revision(ctx, repoURL)
	if err != nil {
		return "", 0, fmt.Errorf("read repository revision before commit: %w", err)
	}
	marker := uuid.NewString()
	args := []string{"commit"}
	if keepLocks {
		args = append(args, "--no-unlock")
	}
	args = append(args, "--with-revprop", "filees:commit-id="+marker, "-m", message)
	args = append(args, c.pathArgs(rootDirectory, paths)...)
	out, err := c.run(ctx, rootDirectory, args)
	if err != nil {
		return out, 0, err
	}
	revision, err := c.commitRevisionByMarker(ctx, repoURL, headBefore+1, marker)
	if err != nil {
		return out, 0, fmt.Errorf("read exact commit revision: %w", err)
	}
	return out, revision, nil
}

func (c *execClient) commitRevisionByMarker(ctx context.Context, repoURL string, firstRevision int64, marker string) (int64, error) {
	if firstRevision < 1 {
		firstRevision = 1
	}
	out, err := c.run(ctx, "", []string{
		"log", "--xml", "--with-revprop", "filees:commit-id",
		"-r", fmt.Sprintf("HEAD:%d", firstRevision), repoURL,
	})
	if err != nil {
		return 0, err
	}
	var logXML struct {
		Entries []struct {
			Revision int64 `xml:"revision,attr"`
			RevProps struct {
				Properties []struct {
					Name  string `xml:"name,attr"`
					Value string `xml:",chardata"`
				} `xml:"property"`
			} `xml:"revprops"`
		} `xml:"logentry"`
	}
	if err := xml.Unmarshal([]byte(out), &logXML); err != nil {
		return 0, fmt.Errorf("parse commit receipt log: %w", err)
	}
	for _, entry := range logXML.Entries {
		for _, property := range entry.RevProps.Properties {
			if property.Name == "filees:commit-id" && strings.TrimSpace(property.Value) == marker {
				if entry.Revision <= 0 {
					return 0, errors.New("commit receipt contains invalid revision")
				}
				return entry.Revision, nil
			}
		}
	}
	return 0, errors.New("commit receipt marker is absent from repository log")
}

func (c *execClient) Lock(ctx context.Context, rootDirectory string, paths []string) (string, error) {
	args := append([]string{"lock"}, c.pathArgs(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

func (c *execClient) LockWithComment(ctx context.Context, rootDirectory string, paths []string, comment string, force bool) (string, error) {
	args := []string{"lock", "--message", comment}
	if force {
		args = append(args, "--force")
	}
	args = append(args, c.pathArgs(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

func (c *execClient) Unlock(ctx context.Context, rootDirectory string, paths []string) (string, error) {
	args := append([]string{"unlock"}, c.pathArgs(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

// ListLocks contacts the repository once and extracts every live lock visible
// below rootDirectory.  svn status --show-updates is used rather than svn info:
// the latter only knows locks previously seen by this very working copy.
func (c *execClient) ListLocks(ctx context.Context, rootDirectory string) ([]LockEntry, error) {
	out, err := c.run(ctx, rootDirectory, []string{"status", "--show-updates", "--xml", "--verbose", "--ignore-externals", "--depth", "infinity", "--", "."})
	if err != nil {
		return nil, fmt.Errorf("svn reservation list: %w", err)
	}
	return parseLockListXML(out, rootDirectory)
}

// LockInfo reports the CURRENT repository lock on path, regardless of which
// working copy is asking. `svn info` (no -u) only ever reflects a lock this
// same working copy already knows about from its own last lock/update
// operation - querying it from a WC that never touched the lock itself
// returns no lock at all even when one genuinely exists server-side
// (confirmed empirically: a lock taken from one WC is invisible to `svn
// info --xml` run from a sibling checkout of the same repository, even
// after `svn update`). `svn status --show-updates` contacts the repository
// and reports the live lock under <repos-status>, independent of which WC
// asks - required for AUTOLOCK_CREATOR_OWNERSHIP_CONCEPT_V2.md's
// cross-machine guarantees (silent migration between one realm's own
// instances, never stealing a different realm's lock) to work at all
// between two different machines, not just two calls from the same one.
func (c *execClient) LockInfo(ctx context.Context, rootDirectory, path string) (*LockInfo, error) {
	targets := c.relativize(rootDirectory, []string{path})
	if len(targets) != 1 {
		return nil, errors.New("svn info lock requires exactly one path")
	}
	out, err := c.run(ctx, rootDirectory, []string{"status", "--show-updates", "--xml", "--", targets[0]})
	if err != nil {
		return nil, err
	}
	return parseLockInfoXML(out)
}

type lockXML struct {
	Token   string `xml:"token"`
	Owner   string `xml:"owner"`
	Comment string `xml:"comment"`
	Created string `xml:"created"`
}

func parseLockInfoXML(output string) (*LockInfo, error) {
	var statusXML struct {
		Target struct {
			Entry struct {
				ReposStatus *struct {
					Lock *lockXML `xml:"lock"`
				} `xml:"repos-status"`
				WCStatus struct {
					Lock *lockXML `xml:"lock"`
				} `xml:"wc-status"`
			} `xml:"entry"`
		} `xml:"target"`
	}
	if err := xml.Unmarshal([]byte(output), &statusXML); err != nil {
		return nil, fmt.Errorf("parse status xml: %w", err)
	}
	entry := statusXML.Target.Entry
	// The live, authoritative repository lock always wins when present; the
	// local wc-status lock is only a fallback for output shapes that omit
	// repos-status entirely (observed for a path this same WC already holds
	// the lock on, with nothing else changed server-side to report).
	lock := entry.WCStatus.Lock
	if entry.ReposStatus != nil && entry.ReposStatus.Lock != nil {
		lock = entry.ReposStatus.Lock
	}
	if lock == nil {
		return nil, nil
	}
	created, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(lock.Created))
	if err != nil {
		return nil, fmt.Errorf("parse lock created time: %w", err)
	}
	return &LockInfo{
		Token:   strings.TrimSpace(lock.Token),
		Owner:   strings.TrimSpace(lock.Owner),
		Comment: lock.Comment,
		Created: created,
	}, nil
}

// parseLockListXML accepts the same status XML variants as parseLockInfoXML,
// but walks all entries in a working-copy scan.  The live repos-status lock
// wins over the local wc-status fallback for each path.
func parseLockListXML(output, rootDirectory string) ([]LockEntry, error) {
	var statusXML struct {
		Targets []struct {
			Entries []struct {
				Path        string `xml:"path,attr"`
				ReposStatus *struct {
					Lock *lockXML `xml:"lock"`
				} `xml:"repos-status"`
				WCStatus struct {
					Item  string   `xml:"item,attr"`
					Props string   `xml:"props,attr"`
					Lock  *lockXML `xml:"lock"`
				} `xml:"wc-status"`
			} `xml:"entry"`
		} `xml:"target"`
	}
	if err := xml.Unmarshal([]byte(output), &statusXML); err != nil {
		return nil, fmt.Errorf("parse lock list xml: %w", err)
	}
	var entries []LockEntry
	for _, target := range statusXML.Targets {
		for _, entry := range target.Entries {
			lock := entry.WCStatus.Lock
			if entry.ReposStatus != nil && entry.ReposStatus.Lock != nil {
				lock = entry.ReposStatus.Lock
			}
			if lock == nil {
				continue
			}
			if strings.TrimSpace(lock.Token) == "" {
				return nil, fmt.Errorf("lock list returned a lock without token for %q", entry.Path)
			}
			created, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(lock.Created))
			if err != nil {
				return nil, fmt.Errorf("parse lock created time: %w", err)
			}
			path := entry.Path
			if rel, err := filepath.Rel(rootDirectory, path); err == nil && pathInsideRoot(rel) {
				path = rel
			}
			path = filepath.Clean(path)
			if path == "." || !pathInsideRoot(path) {
				return nil, fmt.Errorf("lock list returned invalid path %q", entry.Path)
			}
			entries = append(entries, LockEntry{Path: path, LockInfo: LockInfo{
				Token: strings.TrimSpace(lock.Token), Owner: strings.TrimSpace(lock.Owner), Comment: lock.Comment, Created: created,
			}, LocalItem: entry.WCStatus.Item, LocalProps: entry.WCStatus.Props})
		}
	}
	return entries, nil
}

func (c *execClient) PropGet(ctx context.Context, rootDirectory, propName string, paths []string) (string, error) {
	args := append([]string{"propget", propName}, c.pathArgs(rootDirectory, paths)...)
	return c.run(ctx, rootDirectory, args)
}

func (c *execClient) PropSet(ctx context.Context, rootDirectory, propName, value string, paths []string) (string, error) {
	if len(paths) == 0 {
		return "", errors.New("svn propset refused: empty path list")
	}
	args := append([]string{"propset", propName, value}, c.pathArgs(rootDirectory, paths)...)
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
	args := append([]string{"resolve", "--accept", accept}, c.pathArgs(wc, paths)...)
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
	// Callers (infoHasURL, infoHasUUID, ...) scan `svn info` output for
	// fixed English line prefixes ("Repository UUID:", ...); svn localizes
	// those under the process's own locale, so the parent's LANG/LC_* must
	// never leak through here regardless of transport.
	env := append(os.Environ(), "LC_ALL=C")
	if c.sshCommand != "" {
		env = append(env, "SVN_SSH="+c.sshCommand)
	}
	cmd.Env = env

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

// pathArgs builds the trailing positional path arguments of an svn invocation,
// preceded by the "--" end-of-options marker. Every path-taking command must
// go through this helper rather than appending relativize() directly: a file or
// directory whose name begins with '-' is a perfectly legal filename, and a
// collaborator can place one in a shared working copy. Without the marker svn
// parses such a name as an option instead of a path (CWE-88 argument
// injection). No separator is emitted for an empty path list, so commands that
// deliberately fall back to the whole working copy (Status with no paths) keep
// their current meaning.
func (c *execClient) pathArgs(rootDirectory string, paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	return append([]string{"--"}, c.relativize(rootDirectory, paths)...)
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
