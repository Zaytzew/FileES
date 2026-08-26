package authority

import "testing"

func TestTreeCacheIsRevisionScopedAndBounded(t *testing.T) {
	cache := NewTreeCache(2)
	first := treeCacheKey{repoID: "repo", sourceRoot: "docs", revision: 1}
	second := treeCacheKey{repoID: "repo", sourceRoot: "docs", revision: 2}
	third := treeCacheKey{repoID: "repo", sourceRoot: "docs", revision: 3}
	cache.put(first, []TreeObject{{RepoPath: "docs/a"}})
	cache.put(second, []TreeObject{{RepoPath: "docs/b"}})
	if _, ok := cache.get(first); !ok {
		t.Fatal("first revision was not cached")
	}
	cache.put(third, []TreeObject{{RepoPath: "docs/c"}})
	if _, ok := cache.get(second); ok {
		t.Fatal("least-recently-used revision was not evicted")
	}
	objects, ok := cache.get(first)
	if !ok || len(objects) != 1 || objects[0].RepoPath != "docs/a" {
		t.Fatalf("first revision = %#v, %v", objects, ok)
	}
	objects[0].RepoPath = "mutated"
	again, _ := cache.get(first)
	if again[0].RepoPath != "docs/a" {
		t.Fatal("cache returned an aliased object slice")
	}
}
