// Package filepolicy defines file-selection rules shared by every FileES
// ingestion path. The built-in rules are deterministic: user_ignore.cfg is a
// working-copy preference and must not influence provisioning decisions.
package filepolicy

import (
	"path"
	"strings"
)

// BuiltinIgnorePatterns are applied to every FileES-originated import. They
// use FileES glob semantics: ** matches zero or more path components.
//
// Keep this list as the sole source of built-in ignores. Consumers of other
// toolchains must translate and test it explicitly rather than maintain a
// second, subtly different list.
var BuiltinIgnorePatterns = []string{
	"**/~$*", "**/.~lock.*#", "**/*.tmp", "**/*.bak",
	"**/.DS_Store", "**/Thumbs.db", "**/desktop.ini",
	"**/.vscode", "**/.idea", "**/*.swp", "**/*.swo",
	"**/node_modules", "**/__pycache__", "**/*.o", "**/*.pyc",
	"**/.git",
	// CAD churn. dwl and dwl2 are AutoCAD's lock files: they exist only while
	// a drawing is open, name whoever opened it, and are deleted on close.
	// Versioning them is worse than pointless - they are a cruder answer to
	// the question FileES reservations already answer, and their appearing and
	// vanishing is exactly the churn that puts a path into a commit batch and
	// then removes it before the commit runs.
	//
	// sv$ and ac$ are autosave and temporary drawings, transient by design.
	//
	// Measured on 2026-09-03 in a live project: an open drawing kept dwl and
	// dwl2 cycling through the watcher for hours. FileES exists for teams
	// working on binary files, so CAD is the flagship case and this list had
	// nothing for it - only office and developer clutter.
	"**/*.dwl", "**/*.dwl2", "**/*.sv$", "**/*.ac$",
}

// IsBuiltinIgnored reports whether rel (a slash-separated path relative to a
// working-copy root) is excluded by the non-overridable FileES policy.
func IsBuiltinIgnored(rel string) bool {
	rel = strings.TrimPrefix(strings.ReplaceAll(rel, "\\", "/"), "./")
	for _, pattern := range BuiltinIgnorePatterns {
		if match(pattern, rel) {
			return true
		}
	}
	return false
}

func match(pattern, rel string) bool {
	if !strings.Contains(pattern, "**") {
		matched, _ := path.Match(pattern, rel)
		return matched
	}
	return matchDoublestar(pattern, rel)
}

func matchDoublestar(pattern, rel string) bool {
	if pattern == "**" {
		return true
	}
	idx := strings.Index(pattern, "**/")
	if idx < 0 {
		return strings.HasPrefix(rel, strings.TrimSuffix(pattern, "**"))
	}
	prefix, suffix := pattern[:idx], pattern[idx+3:]
	if prefix != "" {
		if !strings.HasPrefix(rel, prefix) {
			return false
		}
		rel = rel[len(prefix):]
	}
	for {
		matched, _ := path.Match(suffix, rel)
		if matched {
			return true
		}
		idx := strings.IndexByte(rel, '/')
		if idx < 0 {
			return false
		}
		rel = rel[idx+1:]
	}
}
