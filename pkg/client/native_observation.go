package client

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

// Native inspection covers URL HEAD and unmanaged WC before adoption. Reading
// never stamps .filees and never falls back to CLI when evidence is unavailable.
func (c *execClient) nativeTargetInfo(ctx context.Context, target string) (nativeInfoEntry, error) {
	if !strings.Contains(target, "://") {
		abs, err := filepath.Abs(target)
		if err != nil {
			return nativeInfoEntry{}, err
		}
		wc, rel, ok := nativeInfoTarget(abs)
		if !ok {
			return nativeInfoEntry{}, errors.New("native info target is not a working copy")
		}
		return c.nativeInfo(ctx, wc, rel)
	}
	raw, err := c.nativeRemote(ctx, "", "info", "--url", target)
	if err != nil {
		return nativeInfoEntry{}, err
	}
	return parseNativeInfo(raw)
}

func parseNativeInfo(raw map[string]any) (nativeInfoEntry, error) {
	rows, ok := raw["entries"].([]any)
	if !ok || len(rows) != 1 {
		return nativeInfoEntry{}, errors.New("native info requires exactly one entry")
	}
	row, ok := rows[0].(map[string]any)
	if !ok {
		return nativeInfoEntry{}, errors.New("native info invalid entry")
	}
	for _, field := range []string{"revision", "last_changed_rev"} {
		if _, err := nativeRevisionValue(row, field, false); err != nil {
			return nativeInfoEntry{}, err
		}
	}
	var v nativeInfoEntry
	if err := json.Unmarshal([]byte(nativeJSON(row)), &v); err != nil {
		return v, err
	}
	if v.URL == "" || v.ReposRootURL == "" || v.ReposUUID == "" || (v.Kind != "file" && v.Kind != "dir") {
		return v, errors.New("native info incomplete entry")
	}
	return v, nil
}

// Repository locks are authoritative. A cached local token is exposed by C for
// diagnostics but must never substitute for a fresh repos_lock observation.
func (c *execClient) nativeLockObservations(ctx context.Context, wc, rel string) ([]LockEntry, error) {
	args := []string{"status", "--wc", wc, "--show-updates", "--depth"}
	if rel == "" {
		args = append(args, "infinity")
	} else {
		args = append(args, "empty", "--", rel)
	}
	raw, err := c.nativeRemote(ctx, wc, args...)
	if err != nil {
		return nil, err
	}
	return parseNativeLockObservations(raw, rel)
}

func parseNativeLockObservations(raw map[string]any, target string) ([]LockEntry, error) {
	if raw["remote"] != true {
		return nil, errors.New("native status did not confirm a repository observation")
	}
	if _, err := nativeRevisionValue(raw, "against_revision", false); err != nil {
		return nil, err
	}
	rows, ok := raw["entries"].([]any)
	if !ok {
		return nil, errors.New("native status missing entries")
	}
	seen := make(map[string]bool)
	var out []LockEntry
	for _, value := range rows {
		row, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("native status invalid row")
		}
		path, ok := row["path"].(string)
		if !ok || path == "" || seen[path] || (target != "" && path != target) {
			return nil, errors.New("native status unexpected path")
		}
		seen[path] = true
		lockValue, present := row["repos_lock"]
		if !present {
			return nil, errors.New("native status missing repository lock result")
		}
		if lockValue == nil {
			continue
		}
		lock, ok := lockValue.(map[string]any)
		if !ok || !validMovePath(path) {
			return nil, errors.New("native status invalid lock target")
		}
		token, tokOK := lock["token"].(string)
		owner, ownerOK := lock["owner"].(string)
		comment, commentOK := lock["comment"].(string)
		created, createdOK := lock["created"].(string)
		item, itemOK := row["item"].(string)
		props, propsOK := row["props"].(string)
		if !tokOK || token == "" || !ownerOK || owner == "" || !commentOK || !createdOK || !itemOK || !propsOK {
			return nil, errors.New("native status incomplete lock")
		}
		when, err := time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		out = append(out, LockEntry{Path: filepath.FromSlash(path), LockInfo: LockInfo{Token: token, Owner: owner, Comment: comment, Created: when}, LocalItem: item, LocalProps: props})
	}
	if target != "" && !seen[target] {
		return nil, errors.New("native status omitted requested path")
	}
	return out, nil
}

func (c *execClient) nativeReadLockObservation(ctx context.Context, wc, path string) (LockObservation, error) {
	rels, err := nativeRelatives(wc, []string{path})
	if err != nil {
		return LockObservation{}, err
	}
	if len(rels) != 1 {
		return LockObservation{}, errors.New("lock observation requires one file target")
	}
	raw, err := c.nativeRemote(ctx, wc, "status", "--wc", wc, "--show-updates", "--depth", "empty", "--", rels[0])
	if err != nil {
		return LockObservation{}, err
	}
	return parseNativeLockObservation(raw, rels[0])
}

func parseNativeLockObservation(raw map[string]any, target string) (LockObservation, error) {
	locks, err := parseNativeLockObservations(raw, target)
	if err != nil {
		return LockObservation{}, err
	}
	rows := raw["entries"].([]any)
	if len(rows) != 1 {
		return LockObservation{}, errors.New("lock observation requires exactly one entry")
	}
	row := rows[0].(map[string]any)
	switch row["item"] {
	case "normal", "modified", "missing", "deleted", "replaced", "conflicted":
	default:
		return LockObservation{}, errors.New("lock observation requires a versioned target")
	}
	var result LockObservation
	if len(locks) == 1 {
		result.Remote = &locks[0].LockInfo
	}
	// Validate local token with the same shape checks, without substituting it
	// for the repository's result. This is needed for receipt/ARM ownership.
	value, present := row["local_lock"]
	if !present {
		return result, errors.New("native status missing local token observation")
	}
	copyRow := make(map[string]any, len(row))
	for k, v := range row {
		copyRow[k] = v
	}
	copyRow["repos_lock"] = value
	local, err := parseNativeLockObservations(map[string]any{"remote": true, "against_revision": raw["against_revision"], "entries": []any{copyRow}}, target)
	if err != nil {
		return LockObservation{}, err
	}
	if len(local) == 1 {
		result.Local = &local[0].LockInfo
	}
	return result, nil
}
