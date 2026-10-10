//go:build windows && !nocfapi

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"filees/pkg/client"
	"filees/pkg/localrepo"
	"golang.org/x/sys/windows"
)

const anchorMoveObjectsName = "remote-tree.objects.json"
const anchorMoveObjectsLimit = 20 << 20

type anchorMoveObject struct {
	Path           string
	ID             anchorReleaseFileID
	Directory      bool
	RemoteIdentity string
	Size           int64
}

// Immutable OS identities and remote manifest, not an acknowledgement of any
// current local bytes. Scanner observations carry the published baseline.
type anchorMoveObjects struct {
	Schema  string
	Move    client.RemoteTreeMove
	Tree    anchorRemoteTree
	Objects []anchorMoveObject
	Parents []anchorMoveObject
}

func moveParentPaths(move client.RemoteTreeMove) []string {
	seen := map[string]bool{}
	for _, selected := range []string{move.Source, move.Destination} {
		for parent := path.Dir(selected); ; parent = path.Dir(parent) {
			seen[parent] = true
			if parent == "." {
				break
			}
		}
	}
	var out []string
	for parent := range seen {
		out = append(out, parent)
	}
	sort.Strings(out)
	return out
}

func pinMoveParents(record localrepo.Record, proof *anchorMoveObjects) ([]windows.Handle, error) {
	var pins []windows.Handle
	for _, parent := range proof.Parents {
		guards, err := guardAnchorReleaseDirectories(filepath.Join(record.LocalPath, filepath.FromSlash(parent.Path)), record.LocalPath)
		if err != nil {
			closeAnchorReleaseGuards(pins)
			return nil, err
		}
		pins = append(pins, guards...)
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(guards[len(guards)-1], &info); err != nil || anchorReleaseID(info) != parent.ID {
			closeAnchorReleaseGuards(pins)
			return nil, errors.New("tree move parent was replaced")
		}
	}
	return pins, nil
}

func moveObjectID(id anchorReleaseFileID) string { return fmt.Sprintf("%d:%d", id.Volume, id.Index) }
func moveWithin(root, name string) bool          { return name == root || strings.HasPrefix(name, root+"/") }

// Pin directories/local files without reading bytes. Remote file handles are
// attributes-only, never claimed as a data-write exclusion: atomic CFAPI VERIFY
// and the final exact identity proof are required separately.
func pinMoveObject(record localrepo.Record, rel string, remote bool) (windows.Handle, windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	p, err := windows.UTF16PtrFromString(filepath.Join(record.LocalPath, filepath.FromSlash(rel)))
	if err != nil {
		return windows.InvalidHandle, info, err
	}
	access := uint32(windows.GENERIC_READ)
	if remote {
		access = windows.FILE_READ_ATTRIBUTES
	}
	h, err := windows.CreateFile(p, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err == nil {
		err = windows.GetFileInformationByHandle(h, &info)
	}
	if err == nil && info.NumberOfLinks != 1 {
		err = errors.New("tree move refuses hardlinked objects")
	}
	if err != nil && h != windows.InvalidHandle {
		windows.CloseHandle(h)
		h = windows.InvalidHandle
	}
	return h, info, err
}

func readPinnedMoveDirectory(h windows.Handle, name string) ([]os.DirEntry, error) {
	var duplicate windows.Handle
	process := windows.CurrentProcess()
	if err := windows.DuplicateHandle(process, h, process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(duplicate), name)
	defer f.Close()
	return f.ReadDir(-1)
}

func moveObjectsDigest(proof *anchorMoveObjects) (string, error) {
	raw, err := json.Marshal(proof)
	if err != nil || len(raw) > anchorMoveObjectsLimit {
		return "", errors.New("tree move object evidence exceeds limit")
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

func validateMoveObjects(proof *anchorMoveObjects, record localrepo.Record, root anchorReleaseFileID, move client.RemoteTreeMove) error {
	if proof == nil || proof.Schema != "filees.anchor-move-objects/v1" || proof.Move != move ||
		proof.Tree.Root != root || proof.Tree.Operation != record.OperationID || proof.Tree.URL != record.RepoURL ||
		proof.Tree.Schema != "filees.anchor-tree/v1" || proof.Tree.Revision != move.BaseRevision ||
		len(proof.Objects) == 0 || len(proof.Objects) > 100000 {
		return errors.New("tree move object proof has another binding")
	}
	if err := validateAnchorRemoteNodes(proof.Tree.Nodes); err != nil {
		return err
	}
	parents := moveParentPaths(move)
	if len(parents) != len(proof.Parents) {
		return errors.New("tree move parent evidence is incomplete")
	}
	for i, parent := range proof.Parents {
		if parent.Path != parents[i] || !parent.Directory || parent.ID.Index == 0 || parent.ID.Volume != root.Volume || (parent.Path == "." && parent.ID != root) {
			return errors.New("tree move parent evidence differs")
		}
	}
	known := map[string]anchorRemoteNode{}
	for _, n := range proof.Tree.Nodes {
		known[n.Path] = n
	}
	previous := ""
	for i, n := range proof.Objects {
		if n.Path <= previous || !moveWithin(move.Source, n.Path) || n.ID.Index == 0 || n.ID.Volume != root.Volume || n.Size < 0 {
			return errors.New("tree move object proof is invalid")
		}
		check := move
		check.Source = n.Path
		check.Destination = move.Destination + strings.TrimPrefix(n.Path, move.Source)
		if err := client.ValidateRemoteTreeMove(check); err != nil {
			return err
		}
		if i == 0 && (n.Path != move.Source || !n.Directory) {
			return errors.New("tree move lacks its selected directory identity")
		}
		if n.RemoteIdentity != "" {
			old, ok := known[n.Path]
			rel, revision, size, valid := parseAnchorRemoteIdentity(n.RemoteIdentity)
			if !ok || !valid || rel != n.Path || size != n.Size || (old.Kind == "dir") != n.Directory {
				return errors.New("tree move placeholder differs from its remote manifest")
			}
			if n.Directory {
				// The manifest describes the repository, not a receipt that every
				// container was restamped by refresh (protected updates may skip).
				// Directories have no revision-pinned file bytes. Bind their actual
				// canonical identity/Path/zero size and FileID into this immutable
				// proof, then retarget that exact identity only after the SVN receipt.
				wantedPath, wantedRevision, wantedSize, wantedOK := parseAnchorRemoteIdentity(old.Identity)
				if !wantedOK || wantedPath != n.Path || wantedSize != 0 || n.Size != 0 ||
					revision <= 0 || revision > wantedRevision || wantedRevision > move.BaseRevision {
					return errors.New("tree move directory differs from its remote manifest")
				}
			} else if old.Identity != n.RemoteIdentity || revision != move.BaseRevision {
				// A file's older stamp may describe different bytes. It never
				// inherits the metadata-only directory exception.
				return errors.New("tree move file differs from its remote manifest")
			}
		}
		previous = n.Path
	}
	return nil
}

// Called under real writer/scanner locks. The opened ancestors remain pinned
// by the caller; traversal rejects every foreign reparse point via tree-states.
func (m *anchorManager) captureMoveObjects(ctx context.Context, record localrepo.Record, root anchorReleaseFileID, move client.RemoteTreeMove) (*anchorMoveObjects, []windows.Handle, error) {
	tree, err := loadAnchorRemoteTree(record, root)
	if err != nil {
		return nil, nil, err
	}
	proof := &anchorMoveObjects{Schema: "filees.anchor-move-objects/v1", Move: move, Tree: tree}
	var pins []windows.Handle
	fail := func(err error) (*anchorMoveObjects, []windows.Handle, error) {
		closeAnchorReleaseGuards(pins)
		return nil, nil, err
	}
	for _, parent := range moveParentPaths(move) {
		guards, err := guardAnchorReleaseDirectories(filepath.Join(record.LocalPath, filepath.FromSlash(parent)), record.LocalPath)
		if err != nil {
			return fail(err)
		}
		pins = append(pins, guards...)
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(guards[len(guards)-1], &info); err != nil {
			return fail(err)
		}
		proof.Parents = append(proof.Parents, anchorMoveObject{Path: parent, ID: anchorReleaseID(info), Directory: true})
	}
	names := []string{move.Source}
	bytes := 0
	for len(names) != 0 {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		batch := append([]string(nil), names[:min(64, len(names))]...)
		names = names[len(batch):]
		state, err := m.call(ctx, strings.Join(batch, "\n")+"\n", "tree-states", "--root", record.LocalPath)
		if err != nil || !state.OK || len(state.States) != len(batch) {
			return fail(errors.New("tree move cannot prove selected object metadata"))
		}
		for index, rel := range batch {
			bytes += len(rel)
			if len(proof.Objects) >= 100000 || bytes > 16<<20 {
				return fail(errors.New("tree move selection exceeds limit"))
			}
			if !state.States[index].OK {
				return fail(errors.New("tree move cannot prove selected object metadata"))
			}
			s := state.States[index]
			remote := s.Placeholder && !s.Local
			if s.Size == nil || (!s.Directory && remote && (!s.InSync || s.OnDisk != 0 || s.Modified != 0)) ||
				(!s.Directory && s.Local && s.Partial) {
				return fail(errors.New("tree move has a partially downloaded or changed remote object"))
			}
			// Attribute-only for remote files; directory LIST handles do not hydrate.
			h, info, err := pinMoveObject(record, rel, remote && !s.Directory)
			if err != nil {
				return fail(err)
			}
			pins = append(pins, h)
			if (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != s.Directory {
				return fail(errors.New("tree move object kind changed"))
			}
			object := anchorMoveObject{Path: rel, ID: anchorReleaseID(info), Directory: s.Directory, Size: *s.Size}
			if s.Directory {
				// NTFS directory EOF measures its index, not file content. Keep
				// logical size zero even when using an older helper's tree-states.
				object.Size = 0
			}
			if remote {
				object.RemoteIdentity = s.Identity
			}
			proof.Objects = append(proof.Objects, object)
			if s.Directory {
				// Go's ordinary directory open omits FILE_SHARE_DELETE. During
				// NOTIFY_RENAME Windows already holds a DELETE handle, so use
				// the pinned, delete-sharing LIST handle instead of reopening.
				entries, err := readPinnedMoveDirectory(h, filepath.Join(record.LocalPath, filepath.FromSlash(rel)))
				if err != nil {
					return fail(err)
				}
				if len(entries)+len(names)+len(proof.Objects) > 100000 {
					return fail(errors.New("tree move directory exceeds limit"))
				}
				for _, e := range entries {
					names = append(names, rel+"/"+e.Name())
				}
			}
		}
	}
	sort.Slice(proof.Objects, func(i, j int) bool { return proof.Objects[i].Path < proof.Objects[j].Path })
	if err := validateMoveObjects(proof, record, root, move); err != nil {
		return fail(err)
	}
	// Current physical selection, not just the enumerated name, must still agree.
	if err := m.verifyMoveObjects(ctx, record, proof, move.Source, 0); err != nil {
		return fail(err)
	}
	return proof, pins, nil
}

func (m *anchorManager) verifyMoveObjects(ctx context.Context, record localrepo.Record, proof *anchorMoveObjects, selected string, revision int64) error {
	for start := 0; start < len(proof.Objects); start += 64 {
		objects := proof.Objects[start:min(start+64, len(proof.Objects))]
		var paths []string
		for _, object := range objects {
			paths = append(paths, selected+strings.TrimPrefix(object.Path, proof.Move.Source))
		}
		state, err := m.call(ctx, strings.Join(paths, "\n")+"\n", "tree-states", "--root", record.LocalPath)
		if err != nil || !state.OK || len(state.States) != len(objects) {
			return errors.New("tree move metadata proof unavailable")
		}
		for index, object := range objects {
			rel := selected + strings.TrimPrefix(object.Path, proof.Move.Source)
			h, info, err := pinMoveObject(record, rel, true)
			if err != nil {
				return err
			}
			windows.CloseHandle(h)
			if anchorReleaseID(info) != object.ID {
				return errors.New("tree move object was replaced")
			}
			if !state.States[index].OK {
				return errors.New("tree move metadata proof unavailable")
			}
			s := state.States[index]
			if s.Directory != object.Directory {
				return errors.New("tree move object kind differs")
			}
			if object.RemoteIdentity == "" && s.Placeholder && !s.Local && !s.Directory {
				return errors.New("tree move local file became a remote placeholder")
			}
			if object.RemoteIdentity != "" {
				wanted := object.RemoteIdentity
				if revision > 0 {
					wanted, err = anchorRemoteIdentity(rel, revision, object.Size)
				}
				if err != nil || s.Identity != wanted || (!s.Directory && (!s.InSync || s.OnDisk != 0 || s.Modified != 0)) {
					return errors.New("tree move remote identity or bytes differ")
				}
			}
		}
	}
	return ctx.Err()
}

func readMoveObjects(ctx context.Context, record localrepo.Record, in anchorMoveIntent) (*anchorMoveObjects, error) {
	if in.Schema != "filees.anchor-move-intent/v3" {
		return nil, errors.New("tree move lacks bound OS object evidence")
	}
	f, info, err := openAnchorReleaseFile(filepath.Join(anchorMoveStateDir(record), anchorMoveObjectsName), false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info.FileSizeHigh != 0 || info.FileSizeLow > anchorMoveObjectsLimit {
		return nil, errors.New("tree move object evidence exceeds limit")
	}
	raw, err := io.ReadAll(anchorReleaseReader{ctx, io.LimitReader(f, anchorMoveObjectsLimit+1)})
	if err != nil || len(raw) > anchorMoveObjectsLimit {
		return nil, errors.New("tree move object evidence unreadable")
	}
	hash := sha256.Sum256(raw)
	if hex.EncodeToString(hash[:]) != in.ObjectsSHA256 {
		return nil, errors.New("tree move object evidence changed")
	}
	// Verify the hash before allocating the immutable, locally generated array.
	var p anchorMoveObjects
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if err := validateMoveObjects(&p, record, in.Root, in.Move); err != nil {
		return nil, err
	}
	return &p, ctx.Err()
}
