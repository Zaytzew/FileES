package mobileclient

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Android has one app process. Do not share the upload queue's lock: sending
// a large capture can hold that lock for hours, while listing writes are short.
var directoryCacheMu sync.Mutex

type listingPair struct {
	gen, rev int64
	dir      string
}

func listingPage(name string) bool {
	if !strings.HasSuffix(name, ".json") || len(name) != 69 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimSuffix(name, ".json"))
	return err == nil
}

// Only recognized, non-symlink cache directories/pages are candidates.
func (s Store) listingPairs(repoID string) []listingPair {
	root := filepath.Join(s.Root, "dir-listings", repoID)
	if info, err := os.Lstat(root); err != nil || !info.IsDir() {
		return nil
	}
	gens, _ := os.ReadDir(root)
	var pairs []listingPair
	for _, gen := range gens {
		g, err := strconv.ParseInt(gen.Name(), 10, 64)
		if err != nil || g < 1 || !gen.IsDir() {
			continue
		}
		revs, _ := os.ReadDir(filepath.Join(root, gen.Name()))
		for _, rev := range revs {
			r, err := strconv.ParseInt(rev.Name(), 10, 64)
			if err != nil || r < 1 || !rev.IsDir() {
				continue
			}
			dir := filepath.Join(root, gen.Name(), rev.Name())
			pages, _ := os.ReadDir(dir)
			for _, page := range pages {
				if page.Type().IsRegular() && listingPage(page.Name()) {
					pairs = append(pairs, listingPair{g, r, dir})
					break
				}
			}
		}
	}
	return pairs
}

// Called under Store's lock after a durable save. Incomparable pairs remain;
// an older reply cannot remove a newer generation OR revision. Cache GC is
// best effort; never recursively delete unknown content from these directories.
func (s Store) pruneListingPairs(pairs []listingPair, gen, rev int64) {
	for _, pair := range pairs {
		if pair.gen > gen || pair.rev > rev || (pair.gen == gen && pair.rev == rev) {
			continue
		}
		pages, _ := os.ReadDir(pair.dir)
		for _, page := range pages {
			if page.Type().IsRegular() && listingPage(page.Name()) {
				_ = os.Remove(filepath.Join(pair.dir, page.Name()))
			}
		}
		_ = os.Remove(pair.dir)
		_ = os.Remove(filepath.Dir(pair.dir))
	}
}
