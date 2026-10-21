//go:build windows && !nocfapi

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"filees/pkg/client"
	"filees/pkg/commit"
	"filees/pkg/localrepo"
	"golang.org/x/sys/windows"
)

type anchorRemoteTree struct {
	Projection string              `json:"projection,omitempty"` // only the separate reprojection hint file
	Schema     string              `json:"schema"`
	Operation  string              `json:"operation"`
	URL        string              `json:"url"`
	Root       anchorReleaseFileID `json:"root"`
	Revision   int64               `json:"revision"`
	Nodes      []anchorRemoteNode  `json:"nodes"`
}

type anchorTreeChange struct {
	node     anchorRemoteNode
	previous string
	remove   bool
}
type anchorTreeRefresh struct {
	reprojection string
	next         time.Time
	target       *anchorRemoteTree
	changes      []anchorTreeChange
	cursor       int
}

func anchorTreePath(root string) string { return filepath.Join(root, ".filees", "anchor-tree.json") }

func guardAnchorSeedDirectory(root, parent string, create func() error) error {
	guards, err := guardAnchorReleaseDirectories(filepath.Join(root, filepath.FromSlash(parent)), root)
	if err != nil {
		return err
	}
	defer closeAnchorReleaseGuards(guards)
	return create()
}

func (m *anchorManager) noteReleasedRemote(ctx context.Context, record localrepo.Record, released anchorReleaseRecord) error {
	guards, err := guardAnchorReleaseDirectories(record.LocalPath, record.LocalPath)
	if err != nil {
		return err
	}
	defer closeAnchorReleaseGuards(guards)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(guards[len(guards)-1], &info); err != nil {
		return err
	}
	root := anchorReleaseID(info)
	tree, err := loadAnchorRemoteTree(record, root)
	if os.IsNotExist(err) {
		raw, readErr := os.ReadFile(anchorSeedPath(record.LocalPath))
		var seed anchorSeed
		if readErr != nil || len(raw) > 4096 || json.Unmarshal(raw, &seed) != nil || seed.Schema != anchorSeedSchema || seed.Revision <= 0 {
			return errors.New("restored tree seed unavailable")
		}
		tree = anchorRemoteTree{Schema: "filees.anchor-tree/v1", Operation: record.OperationID, URL: record.RepoURL, Root: root, Revision: seed.Revision}
		tree.Nodes, err = m.readRemoteTree(ctx, record, seed.Revision, true)
	}
	if err != nil {
		return err
	}
	id, err := anchorRemoteIdentity(released.Identity.Rel, released.Identity.Revision, released.Size)
	if err != nil {
		return err
	}
	node := anchorRemoteNode{client.HistoryTreeNode{Path: released.Identity.Rel, Kind: "file", Size: released.Size}, id}
	found := false
	for i := range tree.Nodes {
		if tree.Nodes[i].Path == node.Path {
			tree.Nodes[i], found = node, true
			break
		}
	}
	if !found {
		tree.Nodes = append(tree.Nodes, node)
	}
	if err := validateAnchorRemoteNodes(tree.Nodes); err != nil {
		return err
	}
	return saveAnchorRemoteTree(record, tree)
}

func (m *anchorManager) readRemoteTree(ctx context.Context, record localrepo.Record, revision int64, legacy bool) ([]anchorRemoteNode, error) {
	svn, err := m.svn(record.ServerID)
	if err != nil {
		return nil, err
	}
	reader, ok := svn.(anchorTreeReader)
	if !ok {
		return nil, errors.New("remote tree reader unavailable")
	}
	dir, err := os.MkdirTemp("", "filees-remote-tree-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	plan := filepath.Join(dir, "tree.jsonl")
	if _, err = reader.HistoryListTree(ctx, record.RepoURL, revision, plan); err != nil {
		return nil, err
	}
	var nodes []anchorRemoteNode
	pathBytes := 0
	err = client.EachHistoryTreeNode(plan, func(node client.HistoryTreeNode) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		pathBytes += len(node.Path)
		if len(nodes) >= anchorBatchFileLimit || pathBytes > anchorBatchPathBytes {
			return errors.New("remote tree too large")
		}
		size := node.Size
		if node.Kind == "dir" {
			size = 0
		}
		id, err := anchorRemoteIdentity(node.Path, revision, size)
		if err != nil {
			return err
		}
		if legacy {
			id = node.Path
		}
		nodes = append(nodes, anchorRemoteNode{node, id})
		return nil
	})
	if err == nil {
		err = validateAnchorRemoteNodes(nodes)
	}
	return nodes, err
}

func loadAnchorRemoteTree(record localrepo.Record, root anchorReleaseFileID) (anchorRemoteTree, error) {
	return loadAnchorRemoteTreeAt(record, root, anchorTreePath(record.LocalPath))
}

func loadAnchorRemoteTreeAt(record localrepo.Record, root anchorReleaseFileID, path string) (anchorRemoteTree, error) {
	// Missing/null revision is unknown, not SVN's legitimate empty revision 0.
	tree := anchorRemoteTree{Revision: -1}
	file, info, err := openAnchorReleaseFile(path, false)
	if err != nil {
		return tree, err
	}
	defer file.Close()
	if info.FileSizeHigh != 0 || info.FileSizeLow > 20<<20 {
		return tree, errors.New("remote tree metadata exceeds limit")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 20<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&tree); err != nil {
		return tree, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return tree, errors.New("trailing remote tree data")
	}
	if tree.Schema != "filees.anchor-tree/v1" || tree.Operation != record.OperationID || tree.URL != record.RepoURL || tree.Root != root || tree.Revision < 0 || (tree.Revision == 0 && len(tree.Nodes) != 0) {
		return tree, errors.New("remote tree binding changed")
	}
	return tree, validateAnchorRemoteNodes(tree.Nodes)
}

func saveAnchorRemoteTree(record localrepo.Record, tree anchorRemoteTree) error {
	return saveAnchorRemoteTreeAt(record, tree, anchorTreePath(record.LocalPath))
}

func saveAnchorRemoteTreeAt(record localrepo.Record, tree anchorRemoteTree, path string) error {
	guards, err := guardAnchorReleaseDirectories(filepath.Join(record.LocalPath, ".filees"), record.LocalPath)
	if err != nil {
		return err
	}
	defer closeAnchorReleaseGuards(guards)
	raw, err := json.Marshal(tree)
	if err != nil || len(raw) > 20<<20 {
		return errors.New("remote tree encoding failed or too large")
	}
	file, err := os.CreateTemp(filepath.Join(record.LocalPath, ".filees"), ".anchor-tree-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// Deletes children before parents, then adds parents before children. A rename
// is a remote delete+create, never a move of an adopted/edited local document.
// The old manifest remains until the entire pass is acknowledged. Replay
// accepts the intended new identity, but never an arbitrary replacement.
func anchorTreeChanges(old, target []anchorRemoteNode) []anchorTreeChange {
	previous, wanted := map[string]anchorRemoteNode{}, map[string]anchorRemoteNode{}
	for _, node := range old {
		previous[node.Path] = node
	}
	for _, node := range target {
		wanted[node.Path] = node
	}
	var changes []anchorTreeChange
	for _, node := range old {
		next, exists := wanted[node.Path]
		if !exists || next.Kind != node.Kind {
			changes = append(changes, anchorTreeChange{node, node.Identity, true})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return anchorWalkBefore(changes[j].node.Path, changes[i].node.Path) })
	adds := make([]anchorTreeChange, 0, len(target))
	for _, node := range target {
		old := previous[node.Path]
		if old.Identity == node.Identity && old.Kind == node.Kind && old.Size == node.Size {
			continue
		}
		adds = append(adds, anchorTreeChange{node, old.Identity, false})
	}
	sort.Slice(adds, func(i, j int) bool { return anchorWalkBefore(adds[i].node.Path, adds[j].node.Path) })
	return append(changes, adds...)
}

// The existing observer owns a bounded metadata-only remote pass. No new
// worker and no per-GUI-poll network listing. Errors back off for a minute;
// pending release, lifecycle and WC writers retain their existing fences.
func (m *anchorManager) refreshRemoteTree(ctx context.Context, record localrepo.Record, bridge *anchorBridge, refresh *anchorTreeRefresh) {
	if refresh.target == nil && time.Now().Before(refresh.next) {
		return
	}
	turn, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	unlock, err := m.enterObserverIntent()
	if err != nil {
		return
	}
	defer unlock()
	refresh.next = time.Now().Add(time.Minute)
	if m.currentReleaseRecord(record) != nil || commit.HasPendingLocalMutation(record.LocalPath) {
		return
	}
	guards, err := guardAnchorReleaseDirectories(record.LocalPath, record.LocalPath)
	if err != nil {
		return
	}
	defer closeAnchorReleaseGuards(guards)
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(guards[len(guards)-1], &info) != nil {
		return
	}
	root := anchorReleaseID(info)
	if refresh.target == nil {
		old, loadErr := loadAnchorRemoteTree(record, root)
		if os.IsNotExist(loadErr) {
			raw, readErr := os.ReadFile(anchorSeedPath(record.LocalPath))
			seed := anchorSeed{Revision: -1}
			if readErr != nil || len(raw) > 4096 || json.Unmarshal(raw, &seed) != nil || seed.Schema != anchorSeedSchema || seed.Revision < 0 || (seed.Revision == 0 && (seed.Dirs != 0 || seed.Files != 0)) {
				return
			}
			old = anchorRemoteTree{Schema: "filees.anchor-tree/v1", Operation: record.OperationID, URL: record.RepoURL, Root: root, Revision: seed.Revision}
			old.Nodes, loadErr = m.readRemoteTree(turn, record, seed.Revision, true)
			if loadErr == nil {
				// Persist the known seed baseline before attempting any HEAD
				// refresh. Merge publication needs this bound revision even if
				// the WC writer delays physical placeholder updates. This is
				// not a receipt of those updates or proof of clean local bytes.
				loadErr = saveAnchorRemoteTree(record, old)
			}
		}
		if loadErr != nil {
			m.log.Warnf("remote tree metadata unavailable: %v", loadErr)
			return
		}
		svn, svnErr := m.svn(record.ServerID)
		if svnErr != nil {
			return
		}
		reader, ok := svn.(anchorTreeReader)
		if !ok {
			return
		}
		revision, readErr := reader.Revision(turn, record.RepoURL)
		if readErr != nil {
			return
		}
		target := old
		target.Revision = revision
		target.Nodes, readErr = m.readRemoteTree(turn, record, revision, false)
		if readErr != nil {
			return
		}
		hints, hintErr := loadAnchorRemoteTreeAt(record, root, anchorReprojectionPath(record.LocalPath))
		if hintErr != nil && !os.IsNotExist(hintErr) {
			m.log.Warnf("remote reprojection evidence unavailable: %v", hintErr)
			return
		}
		refresh.reprojection = hints.Projection
		// Hints only force a safe add/identity check through the existing
		// provider. They never authorize deletion or replacement of local data.
		baseline := anchorReprojectionBaseline(old.Nodes, target.Nodes, hints.Nodes)
		refresh.target, refresh.changes, refresh.cursor = &target, anchorTreeChanges(baseline, target.Nodes), 0
	}
	if refresh.target.Root != root {
		refresh.target = nil
		return
	}
	end := min(refresh.cursor+64, len(refresh.changes))
	var paths []string
	for _, change := range refresh.changes[refresh.cursor:end] {
		paths = append(paths, change.node.Path)
	}
	repo := m.repos(record.ServerID, record.RepoID)
	if repo == nil {
		return
	}
	apply := func(_ []client.StatusEntry) error {
		for refresh.cursor < end {
			change := refresh.changes[refresh.cursor]
			parent := filepath.Dir(filepath.Join(record.LocalPath, filepath.FromSlash(change.node.Path)))
			parents, err := guardAnchorReleaseDirectories(parent, record.LocalPath)
			if os.IsNotExist(err) && change.remove {
				refresh.cursor++
				continue
			}
			if err != nil {
				return err
			}
			kind, size, remove := "f", change.node.Size, "0"
			if change.node.Kind == "dir" {
				kind, size = "d", 0
			}
			if change.remove {
				remove = "1"
			}
			err = bridge.connectedCommand(turn, "tree-set", "tree-set", change.node.Path, kind,
				strconv.FormatInt(size, 10), change.previous, change.node.Identity, remove)
			closeAnchorReleaseGuards(parents)
			if err != nil {
				// Local bytes, foreign identity, or a nonempty deleted folder
				// remain. This is an intentional omission, not a destructive retry.
				if strings.HasSuffix(err.Error(), "0x00000001") || (change.remove && strings.HasSuffix(err.Error(), "0x80070091")) {
					refresh.cursor++
					continue
				}
				return err
			}
			refresh.cursor++
		}
		return nil
	}
	if len(paths) > 0 {
		err = repo.WithLocalStatus(turn, paths, apply)
	}
	if err != nil {
		m.log.Warnf("remote tree refresh deferred: %v", err)
		refresh.target = nil
		return
	}
	if refresh.cursor == len(refresh.changes) {
		if err = saveAnchorRemoteTree(record, *refresh.target); err != nil {
			m.log.Warnf("remote tree receipt unavailable: %v", err)
		} else if refresh.reprojection != "" {
			// Intent admission still held: a new native callback cannot add a
			// hint between this confirmed projection and its retirement.
			hints, readErr := loadAnchorRemoteTreeAt(record, root, anchorReprojectionPath(record.LocalPath))
			if readErr == nil && hints.Projection == refresh.reprojection {
				hints.Nodes, hints.Projection = nil, ""
				if err = saveAnchorRemoteTreeAt(record, hints, anchorReprojectionPath(record.LocalPath)); err != nil {
					m.log.Warnf("remote reprojection receipt unavailable: %v", err)
				}
			}
		}
		refresh.target = nil
	}
}

// Force an add only for paths still present in the bound target. Keep old
// entries absent from it so the normal provider pass can remove empty remote
// directories retained by SVN replay. Hints must not hide a remote deletion.
func anchorReprojectionBaseline(old, target, hints []anchorRemoteNode) []anchorRemoteNode {
	forced, wanted := make(map[string]bool, len(hints)), make(map[string]bool, len(target))
	for _, node := range hints {
		forced[node.Path] = true
	}
	for _, node := range target {
		wanted[node.Path] = true
	}
	baseline := make([]anchorRemoteNode, 0, len(old))
	for _, node := range old {
		if !forced[node.Path] || !wanted[node.Path] {
			baseline = append(baseline, node)
		}
	}
	return baseline
}
