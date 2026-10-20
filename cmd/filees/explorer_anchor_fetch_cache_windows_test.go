//go:build windows && !nocfapi

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnchorMaterializationCachePinsBytesAndName(t *testing.T) {
	c := &anchorFetchCache{}
	defer c.close()
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	key := anchorFetchKey{"file", 31, 7}
	if _, err := c.get(t.Context(), key, func() (string, error) { return path, nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0600); err == nil {
		t.Fatal("cached bytes mutable")
	}
	if err := os.Rename(path, path+"-moved"); err == nil {
		t.Fatal("cached name replaceable")
	}
	if err := os.Remove(path); err == nil {
		t.Fatal("cached file removable")
	}
	// Expiration/eviction only releases its private pin, not a user document.
	c.mu.Lock()
	c.drop(key, c.entries[key])
	c.mu.Unlock()
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
}
