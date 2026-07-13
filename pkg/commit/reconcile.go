package commit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/errmap"
)

const kolizjeDir = "!kolizje"

// conflictMeta is written as <file>.meta alongside each saved local copy.
type conflictMeta struct {
	OrigRel   string `json:"orig_rel"`  // original relative path in WC
	SavedAs   string `json:"saved_as"`  // path within !kolizje/ subtree
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

// reconcile saves local copies of conflicted files to !kolizje/ and resolves in favour of the
// server (HEAD rules). Each conflict gets a .meta JSON file describing the saved copy.
func (s *Service) reconcile(ctx context.Context, wc, username, password string, conflicted []string) {
	if len(conflicted) == 0 {
		return
	}

	ts := time.Now().Format("2006.01.02@15.04")
	kolizjeBase := filepath.Join(wc, kolizjeDir, ts+"_lokalne")

	resolved := 0
	for _, rel := range conflicted {
		absFile := filepath.Join(wc, filepath.FromSlash(rel))

		// SVN creates <file>.mine for binary conflicts; prefer it as it is the pure local version.
		src := absFile + ".mine"
		if _, err := os.Stat(src); err != nil {
			src = absFile // fallback: use the file itself
		}

		if _, err := os.Stat(src); err != nil {
			s.Logger.Warnf("reconcile: cannot find local copy for %s — resolving without save", rel)
		} else {
			if err := saveConflictCopy(src, rel, kolizjeBase, ts); err != nil {
				s.Logger.Warnf("reconcile: save %s: %v", rel, err)
				// non-fatal: still attempt to resolve
			}
		}

		out, err := s.Cli.Resolve(ctx, wc, []string{rel}, "theirs-full", username, password)
		if err != nil {
			s.Logger.Warnf("reconcile: svn resolve %s: %v\n%s", rel, err, out)
			continue
		}
		resolved++
		s.Logger.Infof("reconcile: %s — server version accepted, local copy saved to %s",
			rel, filepath.Join(kolizjeDir, ts+"_lokalne"))
	}

	if resolved > 0 {
		s.ErrSink.Emit(errmap.Entry{
			Code:     errmap.CodeReconFlict,
			Severity: errmap.SevWarn,
			Hint:     errmap.HintRequireAction,
			Msg: fmt.Sprintf(
				"%d conflict(s) resolved (HEAD wins) — local copies in %s",
				resolved, kolizjeDir),
		})
	}
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

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
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
	return os.WriteFile(dst+".meta", metaB, 0o644)
}
