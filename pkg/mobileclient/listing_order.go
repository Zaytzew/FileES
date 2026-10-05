package mobileclient

import (
	"sort"
	"strings"

	v1 "filees/pkg/mobile/v1"
)

// orderManifestEntries is the local directory manifest order: directories,
// then path. It does not follow the order the server happened to send.
func orderManifestEntries(in []v1.ManifestEntry) []v1.ManifestEntry {
	out := append([]v1.ManifestEntry(nil), in...)
	sortManifestEntries(out)
	return out
}

func sortManifestEntries(entries []v1.ManifestEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return manifestEntryLess(entries[i], entries[j])
	})
}

func manifestEntryLess(a, b v1.ManifestEntry) bool {
	aDir := a.Kind == v1.KindDirectory
	bDir := b.Kind == v1.KindDirectory
	if aDir != bDir {
		return aDir
	}
	ap, bp := strings.ToLower(a.Path), strings.ToLower(b.Path)
	if ap != bp {
		return ap < bp
	}
	return a.Path < b.Path
}

func entriesInManifestOrder(entries []v1.ManifestEntry) bool {
	for i := 1; i < len(entries); i++ {
		if manifestEntryLess(entries[i], entries[i-1]) {
			return false
		}
	}
	return true
}

func sameDirectoryPage(a, b *v1.Manifest) bool {
	if a == nil || b == nil {
		return false
	}
	if a.RepoID != b.RepoID || a.ViewGeneration != b.ViewGeneration || a.RepoRevision != b.RepoRevision {
		return false
	}
	return sameManifestEntries(a.Entries, b.Entries)
}

func sameManifestEntries(a, b []v1.ManifestEntry) bool {
	if len(a) != len(b) {
		return false
	}
	aa := orderManifestEntries(a)
	bb := orderManifestEntries(b)
	for i := range aa {
		if aa[i].Path != bb[i].Path || aa[i].Kind != bb[i].Kind || aa[i].Size != bb[i].Size || aa[i].LastChangedRevision != bb[i].LastChangedRevision {
			return false
		}
		if !sameContentHash(aa[i].ContentHash, bb[i].ContentHash) {
			return false
		}
	}
	return true
}

func sameContentHash(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
