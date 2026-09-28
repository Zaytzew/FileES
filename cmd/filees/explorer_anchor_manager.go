//go:build !nocfapi

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"filees/pkg/client"
	"filees/pkg/clientprofile"
	"filees/pkg/cloudfiles"
	"filees/pkg/errcat"
	"filees/pkg/ipcserver"
	"filees/pkg/localrepo"
	"filees/pkg/talk"
)

// explorerAnchors wires the anchor manager into the IPC server and answers its
// loop, or nil when the Cloud Files helper is not installed next to the daemon.
func explorerAnchors(ipc *ipcserver.Server, lifecycle *localrepo.Store, provisioner *daemonProvisioner) func(context.Context) {
	// A Windows build with the Cloud Files API attaches partially only as an
	// anchor, helper or not (owner, 2026-09-28): a missing helper makes
	// anchors unavailable, it does not switch to plain sparse copies.
	if runtime.GOOS == "windows" {
		ipc.SetPartialAnchorMode(true)
	}
	cfapi := anchorHelperPath()
	if cfapi == "" {
		return nil
	}
	anchors := &anchorManager{
		helper: cfapi, nativeSVN: nativeSVNPath(), profiles: clientprofile.DefaultRoot(),
		lifecycle: lifecycle, repos: ipc.RepoState, log: talk.With("anchor"),
	}
	ipc.SetAnchorPrecheck(anchors.precheck)
	provisioner.detachAnchor = anchors.detach
	return anchors.run
}

// anchorManager keeps every Explorer anchor of this machine connected
// (native/filees-cfapi, implementation notes (not distributed)).
//
// An anchor is a sparse working copy whose record says Anchor. Once the attach
// lifecycle has made the copy (empty, nothing chosen), the manager registers
// the folder with Windows, seeds it with placeholders named after HEAD, and
// runs the helper's connect for as long as the copy stays attached - across
// daemon restarts, because the record and the Windows registration both
// survive them. A folder whose record is gone or detached is left alone:
// taking a registration down is the detach path's decision, not this loop's.
type anchorManager struct {
	helper         string
	nativeSVN      string
	profiles       string
	lifecycle      *localrepo.Store
	repos          func(serverID, repoID string) *ipcserver.RepoState
	log            talk.Logger
	reconcileEvery time.Duration
	// newSVN builds the client for a server; nil means the activated
	// profile's identity (profileSVN). Tests hand over a local client.
	newSVN func(serverID string) (client.Client, error)

	mu      sync.Mutex
	running map[string]anchorConnection
}

type anchorConnection struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// anchorHelperPath finds filees-cfapi next to the daemon. FILEES_CFAPI points a
// development build elsewhere. Empty where anchors do not exist.
func anchorHelperPath() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	if helper := strings.TrimSpace(os.Getenv("FILEES_CFAPI")); helper != "" {
		return helper
	}
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	helper := filepath.Join(filepath.Dir(executable), "filees-cfapi.exe")
	if info, err := os.Stat(helper); err != nil || info.IsDir() {
		return ""
	}
	return helper
}

// cfapiAnswer is the helper's one JSON object.
type cfapiAnswer struct {
	OK       bool   `json:"ok"`
	Detail   string `json:"detail"`
	Error    string `json:"error"`
	HResult  string `json:"hresult"`
	Provider string `json:"provider"`
	Ours     bool   `json:"ours"`
	Status   int    `json:"status"`
}

func (m *anchorManager) call(ctx context.Context, stdin string, args ...string) (cfapiAnswer, error) {
	command := exec.CommandContext(ctx, m.helper, args...)
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	raw, runErr := command.Output()
	var answer cfapiAnswer
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &answer); err != nil {
		if runErr != nil {
			return answer, fmt.Errorf("filees-cfapi %s: %v", args[0], runErr)
		}
		return answer, fmt.Errorf("filees-cfapi %s: unreadable answer: %v", args[0], err)
	}
	return answer, nil
}

// precheck is what ipcserver calls before any lifecycle state exists. Windows
// does not nest sync roots: a folder inside Nextcloud or OneDrive cannot be an
// anchor, and saying so here is kinder than a raw error from the filter later.
func (m *anchorManager) precheck(localPath string) error {
	local := filepath.Clean(strings.TrimSpace(localPath))
	if !filepath.IsAbs(local) {
		return errors.New("the anchor folder must be an absolute path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for probe := local; ; probe = filepath.Dir(probe) {
		if _, err := os.Stat(probe); err == nil {
			answer, err := m.call(ctx, "", "info", "--root", probe)
			if err != nil {
				return err
			}
			if answer.OK {
				provider := answer.Provider
				if provider == "" {
					provider = "another program"
				}
				return errcat.New("head.anchor_under_provider", map[string]string{"provider": provider}, fmt.Errorf("%s is inside a folder synchronised by %s", local, provider))
			}
			break
		}
		if filepath.Dir(probe) == probe {
			break
		}
	}
	return nil
}

func (m *anchorManager) svn(serverID string) (client.Client, error) {
	if m.newSVN != nil {
		return m.newSVN(serverID)
	}
	return profileSVN(m.profiles, m.nativeSVN, serverID, "svn:anchor:")
}

func anchorKey(record localrepo.Record) string { return record.ServerID + "\x00" + record.RepoID }

// run reconciles until ctx ends: every attached anchor gets one connect.
func (m *anchorManager) run(ctx context.Context) {
	period := m.reconcileEvery
	if period <= 0 {
		period = 3 * time.Second
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		m.reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *anchorManager) reconcile(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	wanted := map[string]localrepo.Record{}
	for _, record := range m.lifecycle.List() {
		if record.Anchor && record.State == localrepo.StateAttached && filepath.IsAbs(record.LocalPath) {
			wanted[anchorKey(record)] = record
		}
	}
	if m.running == nil {
		m.running = map[string]anchorConnection{}
	}
	for key, connection := range m.running {
		if _, ok := wanted[key]; !ok {
			connection.cancel()
			select {
			case <-connection.done:
				delete(m.running, key)
			default:
			}
		}
	}
	for key, record := range wanted {
		if _, ok := m.running[key]; ok {
			continue
		}
		anchorCtx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		m.running[key] = anchorConnection{cancel: stop, done: done}
		go func() {
			defer close(done)
			m.keep(anchorCtx, record)
		}()
	}
}

// detach runs after the durable lifecycle fence and before removing metadata.
// Waiting includes fetch/adopt jobs and the connected helper's exit. Windows
// unregisters without fetching: fully hydrated files stay, unhydrated entries
// disappear locally. No SVN deletion is scheduled (owner, 2026-09-28).
func (m *anchorManager) detach(ctx context.Context, record localrepo.Record) error {
	m.mu.Lock()
	connection, active := m.running[anchorKey(record)]
	if active {
		connection.cancel()
	}
	m.mu.Unlock()
	if active {
		select {
		case <-connection.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if !filepath.IsAbs(record.LocalPath) {
		return errors.New("anchor detach requires an absolute folder path")
	}
	if _, err := os.Lstat(record.LocalPath); err != nil {
		if os.IsNotExist(err) {
			return nil // retry after a successful move to the recycle bin
		}
		return err
	}
	info, err := m.call(ctx, "", "info", "--root", record.LocalPath)
	if err != nil {
		return err
	}
	if info.OK && (!info.Ours || !cloudfiles.IsSyncRoot(record.LocalPath)) {
		return errors.New("refusing to unregister a different sync root or provider")
	}
	if !info.OK && !strings.EqualFold(info.HResult, "0x80070186") {
		return fmt.Errorf("inspect Explorer anchor: %s (%s)", info.Error, info.HResult)
	}
	answer, err := m.call(ctx, "", "unregister", "--root", record.LocalPath)
	if err != nil {
		return err
	}
	// ERROR_CLOUD_FILE_NOT_UNDER_SYNC_ROOT: a previous attempt may have
	// unregistered successfully and crashed before metadata cleanup.
	if !answer.OK && !strings.EqualFold(answer.HResult, "0x80070186") {
		return fmt.Errorf("unregister Explorer anchor: %s (%s)", answer.Error, answer.HResult)
	}
	// Windows can skip locked entries during unregister. Do not silently
	// abandon them, strip metadata, or move the folder to the recycle bin.
	return filepath.WalkDir(record.LocalPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if cloudfiles.IsPlaceholder(path) || cloudfiles.IsSyncRoot(path) {
			return fmt.Errorf("Explorer placeholder remains after unregister: %s", path)
		}
		if entry.IsDir() && (entry.Name() == ".svn" || entry.Name() == ".filees") {
			return filepath.SkipDir
		}
		return nil
	})
}

// keep holds one anchor: prepare once, then connect, and connect again after a
// pause if the helper ever stops while the anchor is still wanted.
func (m *anchorManager) keep(ctx context.Context, record localrepo.Record) {
	log := talk.With("anchor:" + record.ServerID + "/" + record.RepoID)
	for ctx.Err() == nil {
		if err := m.prepare(ctx, record); err != nil {
			log.Warnf("prepare %s: %v", record.LocalPath, err)
		} else {
			streams, err := startAnchorHelper(ctx, m.helper, record.LocalPath)
			if err != nil {
				log.Warnf("connect %s: %v", record.LocalPath, err)
			} else {
				log.Infof("anchor connected at %s", record.LocalPath)
				bridge := &anchorBridge{
					root: record.LocalPath,
					materialize: func(ctx context.Context, identity string) (string, int64, error) {
						return m.materialize(ctx, record, identity)
					},
					adopt: func(ctx context.Context, identity string, revision int64) error {
						return m.adopt(ctx, record, identity, revision)
					},
					guard: newAnchorGuard(log),
					log:   log,
				}
				err := bridge.serve(ctx, streams)
				if ctx.Err() == nil {
					log.Warnf("anchor at %s went offline: %v", record.LocalPath, err)
				}
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
}

// anchorSeed is written into .filees once the placeholders are in, so a
// restart connects without seeding the tree again.
type anchorSeed struct {
	Schema   string `json:"schema"`
	Revision int64  `json:"revision"`
	Dirs     int64  `json:"dirs"`
	Files    int64  `json:"files"`
}

const anchorSeedSchema = "filees.anchor-seed/v1"

func anchorSeedPath(root string) string { return filepath.Join(root, ".filees", "anchor-seed.json") }

func (m *anchorManager) prepare(ctx context.Context, record localrepo.Record) error {
	info, err := m.call(ctx, "", "info", "--root", record.LocalPath)
	if err != nil {
		return err
	}
	if info.OK && !info.Ours {
		return fmt.Errorf("%s is registered by %s", record.LocalPath, info.Provider)
	}
	if !info.OK {
		answer, err := m.call(ctx, "", "register", "--root", record.LocalPath, "--identity", record.ServerID+"\x1f"+record.RepoID)
		if err != nil {
			return err
		}
		if !answer.OK {
			return fmt.Errorf("register: %s %s", answer.Error, answer.HResult)
		}
	}
	// Also refresh already seeded points after an upgrade. Decoration is best
	// effort; an icon failure must not stop registration or file delivery.
	if icon, err := anchorFolderIconPath(); err != nil {
		talk.With("anchor").Warnf("prepare folder icon: %v", err)
	} else if err := markManagedFolder(record.LocalPath, icon); err != nil {
		talk.With("anchor").Warnf("mark anchor folder: %v", err)
	} else if os.Getenv("FILEES_CFAPI_SHELL_PREVIEW") == "1" {
		// Opt-in until ordinary adopted SVN files have truthful Shell states.
		// Windows otherwise labels them as pending sync after CfRevertPlaceholder.
		answer, err := m.call(ctx, "", "shell-register", "--root", record.LocalPath, "--identity", record.ServerID+"\x1f"+record.RepoID, "--icon", icon)
		if err != nil || !answer.OK {
			talk.With("anchor").Warnf("register Explorer provider: %s (%s): %v", answer.Error, answer.HResult, err)
		}
	}
	if _, err := os.Stat(anchorSeedPath(record.LocalPath)); err == nil {
		return nil
	}
	return m.seed(ctx, record)
}

type anchorTreeReader interface {
	Revision(context.Context, string) (int64, error)
	HistoryListTree(context.Context, string, int64, string) (client.HistoryTreeSummary, error)
}

// seed lists HEAD once and creates every folder and file of it as a
// placeholder, parents before children. Nothing is downloaded.
func (m *anchorManager) seed(ctx context.Context, record localrepo.Record) error {
	svn, err := m.svn(record.ServerID)
	if err != nil {
		return err
	}
	reader, ok := svn.(anchorTreeReader)
	if !ok {
		return errors.New("SVN client cannot list the repository tree")
	}
	revision, err := reader.Revision(ctx, record.RepoURL)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "filees-anchor-plan-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	plan := filepath.Join(dir, "plan.jsonl")
	summary, err := reader.HistoryListTree(ctx, record.RepoURL, revision, plan)
	if err != nil {
		return err
	}
	levels := map[string][]client.HistoryTreeNode{}
	if err := client.EachHistoryTreeNode(plan, func(node client.HistoryTreeNode) error {
		parent := path.Dir(node.Path)
		if parent == "." {
			parent = ""
		}
		levels[parent] = append(levels[parent], node)
		return nil
	}); err != nil {
		return err
	}
	parents := make([]string, 0, len(levels))
	for parent := range levels {
		parents = append(parents, parent)
	}
	sort.Slice(parents, func(i, j int) bool {
		di, dj := strings.Count(parents[i], "/"), strings.Count(parents[j], "/")
		if parents[i] == "" || parents[j] == "" {
			return parents[i] == ""
		}
		if di != dj {
			return di < dj
		}
		return parents[i] < parents[j]
	})
	for _, parent := range parents {
		nodes := levels[parent]
		for start := 0; start < len(nodes); start += 4000 {
			end := start + 4000
			if end > len(nodes) {
				end = len(nodes)
			}
			var listing strings.Builder
			for _, node := range nodes[start:end] {
				kind, size := "f", node.Size
				if node.Kind == "dir" {
					kind, size = "d", 0
				}
				listing.WriteString(kind + "\t" + strconv.FormatInt(size, 10) + "\t" + node.Path + "\t" + path.Base(node.Path) + "\n")
			}
			args := []string{"placeholders", "--root", record.LocalPath}
			if parent != "" {
				args = append(args, "--rel", filepath.FromSlash(parent))
			}
			answer, err := m.call(ctx, listing.String(), args...)
			if err != nil {
				return err
			}
			if !answer.OK {
				return fmt.Errorf("placeholders in %q: %s %s", parent, answer.Error, answer.HResult)
			}
		}
	}
	raw, err := json.Marshal(anchorSeed{Schema: anchorSeedSchema, Revision: revision, Dirs: summary.Dirs, Files: summary.Files})
	if err != nil {
		return err
	}
	return os.WriteFile(anchorSeedPath(record.LocalPath), raw, 0o600)
}

type anchorFileReader interface {
	Revision(context.Context, string) (int64, error)
	HistoryFetchFile(context.Context, string, int64, string) (int64, error)
}

// materialize reads one file at HEAD into a temporary file and says at which
// revision. The file is removed later: the helper reads it after the answer.
func (m *anchorManager) materialize(ctx context.Context, record localrepo.Record, identity string) (string, int64, error) {
	rel, ok := cleanAnchorIdentity(identity)
	if !ok {
		return "", 0, fmt.Errorf("unsafe anchor path %q", identity)
	}
	svn, err := m.svn(record.ServerID)
	if err != nil {
		return "", 0, err
	}
	reader, ok := svn.(anchorFileReader)
	if !ok {
		return "", 0, errors.New("SVN client cannot read a file at a revision")
	}
	revision, err := reader.Revision(ctx, record.RepoURL)
	if err != nil {
		return "", 0, err
	}
	dir, err := os.MkdirTemp("", "filees-anchor-*")
	if err != nil {
		return "", 0, err
	}
	dest := filepath.Join(dir, path.Base(rel))
	if _, err := reader.HistoryFetchFile(ctx, headJoin(record.RepoURL, rel), revision, dest); err != nil {
		_ = os.RemoveAll(dir)
		return "", 0, err
	}
	time.AfterFunc(30*time.Minute, func() { _ = os.RemoveAll(dir) })
	return dest, revision, nil
}

// adopt takes the opened path into the working copy through its running
// commit service. Right after attach the service may still be starting.
func (m *anchorManager) adopt(ctx context.Context, record localrepo.Record, identity string, revision int64) error {
	rel, ok := cleanAnchorIdentity(identity)
	if !ok {
		return fmt.Errorf("unsafe anchor path %q", identity)
	}
	var err error
	for attempt := 0; attempt < 30; attempt++ {
		repo := m.repos(record.ServerID, record.RepoID)
		if repo == nil {
			err = errors.New("repository is not registered")
		} else if err = repo.Adopt(ctx, rel, revision); !errors.Is(err, ipcserver.ErrDepthUnavailable) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return err
}

// cleanAnchorIdentity accepts a repository path as the placeholders were
// created with it: slash-separated, no climb, nothing private.
func cleanAnchorIdentity(identity string) (string, bool) {
	rel := strings.Trim(strings.ReplaceAll(identity, `\`, "/"), "/")
	if rel == "" || strings.ContainsAny(rel, "\x00\t\n") {
		return "", false
	}
	for _, part := range strings.Split(rel, "/") {
		lower := strings.ToLower(part)
		if part == "" || part == "." || part == ".." || lower == ".svn" || lower == ".filees" {
			return "", false
		}
	}
	return rel, true
}
