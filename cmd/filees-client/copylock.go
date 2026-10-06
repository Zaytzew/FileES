package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func acquireCopyLock(wc string) (*os.File, error) {
	path := filepath.Join(wc, ".svn", "filees-client.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("working-copy lock: %w", err)
	}
	if err := platformLockCopy(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("working-copy already active or lock unavailable: %w", err)
	}
	return f, nil
}
