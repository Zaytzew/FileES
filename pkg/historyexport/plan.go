// Package historyexport decides where each object of a historical tree lands
// on the local disk (concepts/REPOSITORY_HISTORY_CONCEPT.md §4.3, owner
// decisions of 2026-09-15 in §2). It performs no I/O: the daemon lists the tree,
// asks this package for a plan, and hands the pairs to the native helper.
//
// The rules:
//   - a name the target cannot hold (portablepath) or that the working-copy
//     guard reserves (.svn, .filees) is skipped and reported with its reason -
//     the rest is still exported;
//   - on a case-folding target, colliding names keep one original and the
//     others take the original in brackets: a.txt + A.txt -> a.txt, a(A).txt,
//     and a further clash takes _2;
//   - the Whale namespace at the repository root is left out, with an
//     annotation, because Whale payloads are not ordinary files of the tree;
//   - names the export itself writes at the root (its report) are reserved.
package historyexport

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"filees/pkg/portablepath"
)

// WhaleNamespace mirrors the reserved Whale namespace of pkg/whale/v1.
const WhaleNamespace = ".filees-whales"

// Reason tokens beyond portablepath's Kind tokens.
const ReasonWorkingCopyName = "working_copy_name"

// Node is one plan entry with a repository-root-relative path.
type Node struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // file or dir
	Size int64  `json:"size"` // bytes for a file
}

type Options struct {
	// FoldCase is true when the target filesystem folds letter case (Windows).
	FoldCase bool
	// Reserved are root-level names the export writes itself.
	Reserved []string
}

type File struct {
	RepoPath  string
	LocalPath string
	Size      int64
}

type Rename struct {
	RepoPath  string `json:"repo_path"`
	LocalPath string `json:"local_path"`
}

// Skip is an object left out, with what it withholds: itself for a file, the
// whole subtree for a folder.
type Skip struct {
	RepoPath string `json:"repo_path"`
	Reason   string `json:"reason"`
	Files    int64  `json:"files"`
	Bytes    int64  `json:"bytes"`
}

type Plan struct {
	Dirs    []string // local paths, every parent before its children
	Files   []File   // sorted by local path, so "x" is fetched before "x.part"
	Renamed []Rename
	Skipped []Skip
	Bytes   int64

	WhaleExcluded bool
	WhaleFiles    int64
	WhaleBytes    int64
}

type totals struct{ files, bytes int64 }

func splitPath(p string) (string, string) {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i], p[i+1:]
	}
	return "", p
}

func joinPath(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

func validRepoPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") {
		return false
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// Build turns a plan into local placements. It refuses a plan that is not a
// tree: a duplicate, a child without its folder, an unknown kind.
func Build(nodes []Node, opts Options) (Plan, error) {
	byPath := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		if !validRepoPath(node.Path) {
			return Plan{}, fmt.Errorf("history export: invalid plan path %q", node.Path)
		}
		if node.Kind != "file" && node.Kind != "dir" {
			return Plan{}, fmt.Errorf("history export: %q has unknown kind %q", node.Path, node.Kind)
		}
		if node.Kind == "file" && node.Size < 0 {
			return Plan{}, fmt.Errorf("history export: %q has a negative size", node.Path)
		}
		if _, dup := byPath[node.Path]; dup {
			return Plan{}, fmt.Errorf("history export: plan names %q twice", node.Path)
		}
		byPath[node.Path] = node
	}
	children := make(map[string][]string)
	sums := make(map[string]totals)
	for p, node := range byPath {
		parent, name := splitPath(p)
		if parent != "" && byPath[parent].Kind != "dir" {
			return Plan{}, fmt.Errorf("history export: plan has %q without its folder", p)
		}
		children[parent] = append(children[parent], name)
		if node.Kind == "file" {
			for dir := p; dir != ""; dir, _ = splitPath(dir) {
				t := sums[dir]
				t.files++
				t.bytes += node.Size
				sums[dir] = t
			}
		}
	}

	var plan Plan
	var walk func(repoDir, localDir string)
	walk = func(repoDir, localDir string) {
		names := children[repoDir]
		kept := names[:0:0]
		for _, name := range names {
			if repoDir == "" && name == WhaleNamespace {
				t := sums[name]
				plan.WhaleExcluded, plan.WhaleFiles, plan.WhaleBytes = true, t.files, t.bytes
				continue
			}
			kept = append(kept, name)
		}
		kinds := make(map[string]string, len(kept))
		for _, name := range kept {
			kinds[name] = byPath[joinPath(repoDir, name)].Kind
		}
		var reserved []string
		if repoDir == "" {
			reserved = opts.Reserved
		}
		placed := place(kept, kinds, reserved, opts.FoldCase)
		sort.Strings(kept)
		for _, name := range kept {
			repoPath := joinPath(repoDir, name)
			decision := placed[name]
			if decision.reason != "" {
				t := sums[repoPath]
				plan.Skipped = append(plan.Skipped, Skip{RepoPath: repoPath, Reason: decision.reason, Files: t.files, Bytes: t.bytes})
				continue
			}
			local := joinPath(localDir, decision.local)
			if decision.local != name {
				plan.Renamed = append(plan.Renamed, Rename{RepoPath: repoPath, LocalPath: local})
			}
			if kinds[name] == "dir" {
				plan.Dirs = append(plan.Dirs, local)
				walk(repoPath, local)
				continue
			}
			size := byPath[repoPath].Size
			plan.Files = append(plan.Files, File{RepoPath: repoPath, LocalPath: local, Size: size})
			plan.Bytes += size
		}
	}
	walk("", "")
	sort.Slice(plan.Files, func(i, j int) bool { return plan.Files[i].LocalPath < plan.Files[j].LocalPath })
	sort.Slice(plan.Renamed, func(i, j int) bool { return plan.Renamed[i].RepoPath < plan.Renamed[j].RepoPath })
	sort.Slice(plan.Skipped, func(i, j int) bool { return plan.Skipped[i].RepoPath < plan.Skipped[j].RepoPath })
	return plan, nil
}

type placement struct {
	local  string
	reason string
}

func workingCopyName(name string) bool {
	for _, reserved := range []string{".svn", ".filees", ".filees-native-probe"} {
		if strings.EqualFold(name, reserved) {
			return true
		}
	}
	return false
}

func splitExt(name, kind string) (string, string) {
	if kind != "file" {
		return name, ""
	}
	if dot := strings.LastIndexByte(name, '.'); dot > 0 {
		return name[:dot], name[dot:]
	}
	return name, ""
}

// place names the children of one folder. Keepers are chosen first for every
// collision group, so a bracketed name can never take a name another object
// keeps.
func place(names []string, kinds map[string]string, reserved []string, fold bool) map[string]placement {
	key := func(s string) string {
		if fold {
			// Uppercase, not EqualFold: portablepath.Collides explains why.
			return strings.ToUpper(s)
		}
		return s
	}
	out := make(map[string]placement, len(names))
	taken := make(map[string]bool, len(names)+len(reserved))
	for _, name := range reserved {
		taken[key(name)] = true
	}
	groups := make(map[string][]string)
	var order []string
	for _, name := range names {
		if problem := portablepath.SegmentProblem(name); problem != nil {
			out[name] = placement{reason: problem.Kind.Token()}
			continue
		}
		if workingCopyName(name) {
			out[name] = placement{reason: ReasonWorkingCopyName}
			continue
		}
		k := key(name)
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], name)
	}
	sort.Strings(order)
	unique := func(stem, ext string) string {
		candidate := stem + ext
		for n := 2; taken[key(candidate)]; n++ {
			candidate = fmt.Sprintf("%s_%d%s", stem, n, ext)
		}
		taken[key(candidate)] = true
		return candidate
	}
	// Within a group the name last in byte order keeps its spelling, which
	// makes a.txt the keeper of a.txt and A.txt, as in the owner's example.
	for _, k := range order {
		group := groups[k]
		sort.Sort(sort.Reverse(sort.StringSlice(group)))
		stem, ext := splitExt(group[0], kinds[group[0]])
		out[group[0]] = placement{local: unique(stem, ext)}
	}
	for _, k := range order {
		group := groups[k]
		keptStem, _ := splitExt(out[group[0]].local, kinds[group[0]])
		for _, name := range group[1:] {
			ownStem, ownExt := splitExt(name, kinds[name])
			out[name] = placement{local: unique(keptStem+"("+ownStem+")", ownExt)}
		}
	}
	return out
}

// FolderName is the new subfolder every export gets: the repository name made
// portable, the historical moment as the caller shows it, and the revision.
func FolderName(repoName string, moment time.Time, revision int64) (string, error) {
	if revision < 0 || moment.IsZero() {
		return "", errors.New("history export: folder name needs a moment and a revision")
	}
	var b strings.Builder
	for _, r := range repoName {
		if r == '/' || r == '\\' || portablepath.IsControlRune(r) || portablepath.IsReservedRune(r) {
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	name := strings.TrimRight(b.String(), ". ")
	if name == "" {
		name = "export"
	}
	if portablepath.IsReservedDeviceName(name) {
		name = "_" + name
	}
	return fmt.Sprintf("%s_%s_r%d", name, moment.Format("2006-01-02_15-04-05"), revision), nil
}
