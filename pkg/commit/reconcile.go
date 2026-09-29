package commit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/client"
	"filees/pkg/errmap"
)

const kolizjeDir = "!kolizje"

// conflictMeta is written as <file>.meta alongside each saved local copy.
type conflictMeta struct {
	OrigRel   string `json:"orig_rel"` // original relative path in WC
	SavedAs   string `json:"saved_as"` // path within !kolizje/ subtree
	Timestamp string `json:"timestamp"`
	Size      int64  `json:"size"`
	Type      string `json:"type"` // "lokalne" | "usuniete" | "nazwy"
}

// parseConflicts extracts conflicted paths from svn update output.
// SVN --non-interactive postpones conflicts; they appear as:
//
//	"C    path"   – text/binary conflict
//	"   C path"   – tree conflict
func parseConflicts(out string) []string {
	if paths, ok := client.UpdateConflicts(out); ok {
		return paths
	}
	seen := make(map[string]struct{})
	var conflicts []string
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(trimmed, "C ") && !strings.HasPrefix(trimmed, "C\t") {
			continue
		}
		path := strings.TrimSpace(trimmed[2:])
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		conflicts = append(conflicts, path)
	}
	return conflicts
}

// ReconcileUpdateConflicts preserves local versions and resolves every conflict
// reported by svn update in favour of the server. It is shared by startup and
// periodic updates so crash recovery cannot bypass the reconciliation policy.
func (s *Service) ReconcileUpdateConflicts(ctx context.Context, wc, updateOutput string) {
	conflicts := parseConflicts(updateOutput)
	if len(conflicts) == 0 {
		if s.OnConflicts != nil {
			s.OnConflicts(0)
		}
		return
	}
	s.Logger.Warnf("update: %d conflict(s) detected — reconciling", len(conflicts))
	s.reconcile(ctx, wc, conflicts)
}

// reconcile saves local copies of conflicted files to !kolizje/ and resolves in favour of the
// server (HEAD rules). Each conflict gets a .meta JSON file describing the saved copy.
func (s *Service) reconcile(ctx context.Context, wc string, conflicted []string) {
	if len(conflicted) == 0 {
		return
	}

	ts := time.Now().Format("2006.01.02@15.04")
	kolizjeBase := filepath.Join(wc, kolizjeDir, ts+"_lokalne")

	resolved, identical := 0, 0
	for _, rel := range conflicted {
		absFile := filepath.Join(wc, filepath.FromSlash(rel))

		// Read the actual local variant from SVN, never an unrelated .mine.
		reader, ok := s.Cli.(client.ConflictReader)
		if !ok {
			s.Logger.Warnf("reconcile: conflict metadata unavailable for %s", rel)
			continue
		}
		details, err := reader.ConflictDetails(ctx, wc, rel)
		if err != nil || len(details) != 1 || (details[0].Type != "text" && details[0].Type != "tree") {
			s.Logger.Warnf("reconcile: incomplete or unsupported conflict metadata for %s: %v", rel, err)
			continue
		}
		sourceRel := rel
		if details[0].Type == "text" && details[0].Mine != "" {
			sourceRel = details[0].Mine
		}
		src, err := intentSafePath(wc, cacheEntry{Rel: sourceRel, Abs: filepath.Join(wc, filepath.FromSlash(sourceRel))})
		if err != nil {
			s.Logger.Warnf("reconcile: unsafe conflict copy for %s: %v", rel, err)
			continue
		}

		if _, err := os.Stat(src); err != nil {
			// Can't find local copy — leave conflict unresolved so the user can act.
			s.Logger.Warnf("reconcile: cannot find local copy for %s — leaving conflict unresolved", rel)
			continue
		}
		if err := saveConflictCopy(src, rel, kolizjeBase, ts); err != nil {
			// Save failed — do NOT resolve; preserve local copy by leaving the conflict in WC.
			s.Logger.Warnf("reconcile: save %s failed (%v) — leaving conflict unresolved", rel, err)
			continue
		}

		out, err := s.Cli.Resolve(ctx, wc, []string{rel}, "theirs-full")
		if err != nil && s.revertAddedOverAdded(ctx, wc, rel) {
			err = nil
		}
		if err != nil {
			s.Logger.Warnf("reconcile: svn resolve %s: %v\n%s", rel, err, out)
			continue
		}
		if sameFileContents(filepath.Join(kolizjeBase, filepath.FromSlash(rel)), absFile) {
			// The server holds exactly these bytes: nothing of the local
			// version is lost, so no copy is kept and nobody is told to look.
			removeConflictCopy(kolizjeBase, rel)
			identical++
			s.Logger.Infof("reconcile: %s — identical on both sides, resolved", rel)
			continue
		}
		resolved++
		s.Logger.Infof("reconcile: %s — server version accepted, local copy saved to %s",
			rel, filepath.Join(kolizjeDir, ts+"_lokalne"))
	}
	if identical > 0 {
		s.Logger.Infof("reconcile: %d conflict(s) with identical contents resolved without a copy", identical)
	}

	if resolved > 0 {
		s.ErrSink.Emit(errmap.Entry{
			Code:     errmap.CodeReconFlict,
			Key:      "recon.conflict",
			Severity: errmap.SevWarn,
			Hint:     errmap.HintRequireAction,
			Msg: fmt.Sprintf(
				"%d conflict(s) resolved (HEAD wins) — local copies in %s",
				resolved, kolizjeDir),
		})
	}
	if s.OnConflicts != nil {
		s.OnConflicts(len(conflicted) - resolved - identical)
	}
}

// ReconcileStandingConflicts resolves conflicts already present in the
// working copy when the repository starts. The update that produced them may
// predate this client (owner's KRAŃCOWA-PŁOŃSK, 2026-09-25: 93 tree conflicts
// from August, invisible until the editing-policy migration refused a dirty
// working copy). The same policy as after an update applies: the server
// version wins and a differing local version is kept in !kolizje.
func (s *Service) ReconcileStandingConflicts(ctx context.Context, wc string) {
	entries, err := s.Cli.Status(ctx, wc, nil)
	if err != nil {
		s.Logger.Warnf("reconcile: status for standing conflicts: %v", err)
		return
	}
	var conflicted []string
	for _, entry := range entries {
		if !entry.Conflicted {
			continue
		}
		if info, err := os.Stat(filepath.Join(wc, entry.Path)); err != nil || !info.Mode().IsRegular() {
			continue // a directory or missing node is not ours to settle here
		}
		conflicted = append(conflicted, filepath.ToSlash(entry.Path))
	}
	if len(conflicted) == 0 {
		return
	}
	s.Logger.Warnf("reconcile: %d standing conflict(s) in the working copy — reconciling", len(conflicted))
	s.reconcile(ctx, wc, conflicted)
}

type pathReverter interface {
	Revert(ctx context.Context, rootDirectory string, paths []string) (string, error)
}

// revertAddedOverAdded settles the one tree conflict "theirs-full" cannot:
// a file scheduled for addition here while the update brought the same path
// from the server. Reverting the local addition leaves the incoming file; the
// caller already keeps the local bytes in !kolizje. Any other tree conflict
// (an incoming delete over a local edit, say) is left for the user.
func (s *Service) revertAddedOverAdded(ctx context.Context, wc, rel string) bool {
	reverter, ok := s.Cli.(pathReverter)
	if !ok {
		return false
	}
	entries, err := s.Cli.Status(ctx, wc, []string{rel})
	if err != nil || len(entries) != 1 || !entries[0].Conflicted || entries[0].Item != "replaced" {
		return false
	}
	if out, err := reverter.Revert(ctx, wc, []string{rel}); err != nil {
		s.Logger.Warnf("reconcile: revert local addition of %s: %v\n%s", rel, err, out)
		return false
	}
	after, err := s.Cli.Status(ctx, wc, []string{rel})
	return err == nil && len(after) == 1 && !after[0].Conflicted
}

func sameFileContents(a, b string) bool {
	left, err := os.ReadFile(a)
	if err != nil {
		return false
	}
	right, err := os.ReadFile(b)
	return err == nil && bytes.Equal(left, right)
}

// removeConflictCopy drops a copy that turned out identical to the server's,
// with its .meta and the directories this batch created and left empty.
func removeConflictCopy(kolizjeBase, rel string) {
	copyPath := filepath.Join(kolizjeBase, filepath.FromSlash(rel))
	_ = os.Remove(copyPath)
	_ = os.Remove(copyPath + ".meta")
	for dir := filepath.Dir(copyPath); strings.HasPrefix(dir, kolizjeBase); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			break
		}
	}
	_ = os.Remove(filepath.Dir(kolizjeBase)) // !kolizje itself, only when empty
}

// saveConflictCopy copies src to <kolizjeBase>/<rel> and writes a .meta file alongside.
func saveConflictCopy(src, rel, kolizjeBase, ts string) error {
	dst := filepath.Join(kolizjeBase, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	fi, _ := in.Stat()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := copyConflictContents(out, in); err != nil {
		_ = os.Remove(dst)
		return err
	}

	var size int64
	if fi != nil {
		size = fi.Size()
	}

	// relative path of saved file inside !kolizje tree (for the meta record)
	savedRel := filepath.ToSlash(rel)

	meta := conflictMeta{
		OrigRel:   rel,
		SavedAs:   savedRel,
		Timestamp: ts,
		Size:      size,
		Type:      "lokalne",
	}
	metaB, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	metaFile, err := os.OpenFile(dst+".meta", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return copyConflictContents(metaFile, bytes.NewReader(metaB))
}

type conflictCopyWriter interface {
	io.Writer
	Sync() error
	Close() error
}

// copyConflictContents does not report success until buffered data has reached
// the filesystem and the destination descriptor has closed successfully. The
// caller may safely resolve the SVN conflict only after this function returns.
func copyConflictContents(dst conflictCopyWriter, src io.Reader) error {
	if _, err := io.Copy(dst, src); err != nil {
		return errors.Join(err, dst.Close())
	}
	if err := dst.Sync(); err != nil {
		return errors.Join(err, dst.Close())
	}
	return dst.Close()
}
