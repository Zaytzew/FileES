package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

// ConversionInventoryReader is optional: conversion must refuse an older
// adapter/helper, never fall back to Status (which intentionally drops '.').
// The caller must quiesce the writer and bind WC/FileID/URL independently.
type ConversionInventoryReader interface {
	ConversionInventory(context.Context, string) (ConversionInventory, error)
}

// ConversionInventory contains metadata only. It does not authorize cleanup,
// prove HEAD, enumerate files inside ignored directories or fence external apps.
type ConversionInventory struct {
	Entries []ConversionInventoryEntry `json:"entries"`
}

type ConversionInventoryEntry struct {
	Path         string `json:"path"`
	Item         string `json:"item"`
	Props        string `json:"props"`
	Kind         string `json:"kind"`
	Depth        string `json:"depth"`
	Revision     *int64 `json:"revision"`
	Versioned    bool   `json:"versioned"`
	Copied       bool   `json:"copied"`
	Switched     bool   `json:"switched"`
	FileExternal bool   `json:"file_external"`
	WCLocked     bool   `json:"wc_locked"`
	Conflicted   bool   `json:"conflicted"`
}

func (c *execClient) ConversionInventory(ctx context.Context, wc string) (ConversionInventory, error) {
	if c.nativeSVNPath == "" {
		return ConversionInventory{}, errors.New("conversion inventory requires the native SVN helper")
	}
	if err := c.nativeRequireFeature(ctx, "conversion_inventory_v1"); err != nil {
		return ConversionInventory{}, err
	}
	raw, err := c.nativeRun(ctx, wc, "status", "--inspect-wc", wc, "--depth", "infinity", "--conversion-inventory")
	if err != nil {
		return ConversionInventory{}, err
	}
	return decodeConversionInventory(raw)
}

func decodeConversionInventory(raw map[string]any) (ConversionInventory, error) {
	refuse := func() (ConversionInventory, error) {
		return ConversionInventory{}, errors.New("native SVN returned an incomplete conversion inventory")
	}
	if raw["inventory_version"] != float64(1) || raw["remote"] != false {
		return refuse()
	}
	rows, ok := raw["entries"].([]any)
	if !ok || len(rows) == 0 {
		return refuse()
	}
	out := ConversionInventory{Entries: make([]ConversionInventoryEntry, 0, len(rows))}
	seen := make(map[string]bool, len(rows))
	for _, value := range rows {
		row, ok := value.(map[string]any)
		if !ok {
			return refuse()
		}
		// Booleans must actually be present. JSON zero values from a legacy or
		// incomplete helper cannot assert an absence of switched/external/locks.
		for _, key := range []string{"versioned", "copied", "switched", "file_external", "wc_locked"} {
			if _, ok := row[key].(bool); !ok {
				return refuse()
			}
		}
		for _, key := range []string{"path", "item", "props", "kind", "depth"} {
			if v, ok := row[key].(string); !ok || v == "" {
				return refuse()
			}
		}
		if revision, present := row["revision"]; !present || (revision != nil && !inventoryRevision(revision)) {
			return refuse()
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return refuse()
		}
		var entry ConversionInventoryEntry
		if err := json.Unmarshal(encoded, &entry); err != nil {
			return refuse()
		}
		if !inventoryPath(entry.Path) || seen[entry.Path] {
			return refuse()
		}
		seen[entry.Path] = true
		out.Entries = append(out.Entries, entry)
	}
	if !seen["."] {
		return refuse()
	}
	return out, nil
}

func inventoryRevision(value any) bool {
	n, ok := value.(float64)
	return ok && n >= 0 && n <= 1<<53-1 && n == float64(int64(n))
}

func inventoryPath(value string) bool {
	return value == "." || (value != "" && !strings.ContainsAny(value, "\\:\x00\r\n") &&
		!strings.HasPrefix(value, "/") && value != ".." && !strings.HasPrefix(value, "../") &&
		path.Clean(value) == value)
}

// CheckFullClean rejects local changes including root properties. Mixed BASE
// revisions are valid SVN and are returned, not silently promoted to HEAD.
// Ignored/unversioned objects are preserved separately, never cleanup proof.
func (inventory ConversionInventory) CheckFullClean() error {
	root := false
	seen := map[string]bool{}
	for _, entry := range inventory.Entries {
		if !inventoryPath(entry.Path) || seen[entry.Path] {
			return errors.New("conversion inventory has an invalid path")
		}
		seen[entry.Path] = true
		if entry.Conflicted || entry.Copied || entry.Switched || entry.FileExternal || entry.WCLocked {
			return fmt.Errorf("working copy has unsupported conversion state: %s", entry.Path)
		}
		if !entry.Versioned {
			if entry.Path == "." || (entry.Item != "ignored" && entry.Item != "unversioned") || entry.Conflicted {
				return fmt.Errorf("conversion inventory has an unsupported local object: %s", entry.Path)
			}
			continue
		}
		if entry.Path == "." {
			root = entry.Kind == "dir"
		}
		if entry.Item != "normal" || (entry.Props != "none" && entry.Props != "normal") ||
			entry.Revision == nil || *entry.Revision < 0 ||
			(entry.Kind != "dir" && entry.Kind != "file") || (entry.Kind == "dir" && entry.Depth != "infinity") {
			return fmt.Errorf("working copy is not ready for conversion: %s", entry.Path)
		}
	}
	if !root {
		return errors.New("conversion inventory is missing its versioned directory root")
	}
	return nil
}
