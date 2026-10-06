package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxRunLogBytes int64 = 5 << 20

// appendRunLog records operational outcomes only. It deliberately excludes
// file names, credentials, command output and error text from the journal.
func appendRunLog(dir, copyID, operation, stage string, success bool) error {
	if !safeCopyID(copyID) {
		return fmt.Errorf("unsafe working-copy id")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("journal directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("journal path is not a directory")
	}
	path := filepath.Join(dir, copyID+".jsonl")
	if old, err := os.Stat(path); err == nil && old.Size() >= maxRunLogBytes {
		if err := os.Rename(path, path+".1"); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	record := struct {
		Time      string `json:"time"`
		Copy      string `json:"copy"`
		Operation string `json:"operation"`
		Stage     string `json:"stage"`
		Success   bool   `json:"success"`
	}{time.Now().UTC().Format(time.RFC3339), copyID, operation, stage, success}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func safeCopyID(id string) bool {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\\`) {
		return false
	}
	for _, r := range id {
		if r > 127 || !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
