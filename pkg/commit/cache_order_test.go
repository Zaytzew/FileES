package commit

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestCacheSaveIsStableAcrossMapIterationAndRestart(t *testing.T) {
	for _, receipts := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "receipts"}[receipts], func(t *testing.T) {
			s := intentFixture(t)
			if receipts {
				s.intentReceipts = []intentReceipt{{PlanID: "first"}, {PlanID: "second"}}
			}
			var first []byte
			for i := 0; i < 64; i++ {
				if err := s.saveCacheChecked(); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(s.cachePath)
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					first = raw
				} else if !bytes.Equal(first, raw) {
					t.Fatal("unchanged queue changed bytes")
				}
			}
			restarted := &Service{wc: s.wc, repoID: s.repoID, cachePath: s.cachePath, staging: map[string]*stageItem{}}
			restarted.loadCache()
			if err := restarted.saveCacheChecked(); err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(s.cachePath)
			if !bytes.Equal(first, raw) {
				t.Fatal("restart changed unchanged queue")
			}
		})
	}
}

func TestCacheValueSortsWithoutMutatingCaller(t *testing.T) {
	entries := []cacheEntry{{Rel: "z"}, {Rel: "a"}}
	raw, err := json.Marshal(intentCacheValue(entries, nil))
	if err != nil {
		t.Fatal(err)
	}
	var saved []cacheEntry
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if entries[0].Rel != "z" || saved[0].Rel != "a" {
		t.Fatal("caller mutated or output unsorted")
	}
}
