package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func (c *execClient) nativeRun(ctx context.Context, wc string, args ...string) (map[string]any, error) {
	if !filepath.IsAbs(c.nativeSVNPath) || !filepath.IsAbs(wc) {
		return nil, errors.New("native SVN: explicit absolute executable and WC required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	deadline := min(c.timeout, 30*time.Second)
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.nativeSVNPath, args...)
	cmd.Dir = wc
	cmd.Env = svnProcessEnvironment(os.Environ(), c.sshCommand)
	stdout := nativeOutput{max: nativeListingLimit}
	stderr := nativeOutput{max: nativeReceiptLimit}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil || stdout.truncated || stderr.truncated {
		return nil, fmt.Errorf("native SVN %q failed: %v; context=%v; truncated=%v\n%s\n%s",
			args[0], err, ctx.Err(), stdout.truncated || stderr.truncated, stdout.buffer.String(), stderr.buffer.String())
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.buffer.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("native SVN %q returned invalid JSON: %v\n%s", args[0], err, stdout.buffer.String())
	}
	if result["schema"] != "filees.native-svn/v1" || result["ok"] != true {
		return nil, fmt.Errorf("native SVN %q refused: %s", args[0], stdout.buffer.String())
	}
	return result, nil
}

func nativeRelatives(root string, paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		rel := p
		if filepath.IsAbs(p) {
			var err error
			rel, err = filepath.Rel(root, p)
			if err != nil {
				return nil, err
			}
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			continue
		}
		if !validMovePath(rel) {
			return nil, fmt.Errorf("native SVN: invalid relative path %q", p)
		}
		out = append(out, rel)
	}
	return out, nil
}

func nativeBatches(paths []string) [][]string {
	if len(paths) == 0 {
		return nil
	}
	out := make([][]string, 0, (len(paths)+nativePathBatch-1)/nativePathBatch)
	for start := 0; start < len(paths); start += nativePathBatch {
		end := start + nativePathBatch
		if end > len(paths) {
			end = len(paths)
		}
		out = append(out, paths[start:end])
	}
	return out
}

func (c *execClient) nativeAdd(ctx context.Context, wc string, paths []string) (string, error) {
	rels, err := nativeRelatives(wc, paths)
	if err != nil {
		return "", err
	}
	for _, batch := range nativeBatches(rels) {
		args := append([]string{"add", "--wc", wc, "--"}, batch...)
		if _, err := c.nativeRun(ctx, wc, args...); err != nil {
			return "", err
		}
	}
	return "", nil
}

func (c *execClient) nativeDelete(ctx context.Context, wc string, paths []string) (string, error) {
	rels, err := nativeRelatives(wc, paths)
	if err != nil {
		return "", err
	}
	for _, batch := range nativeBatches(rels) {
		args := append([]string{"delete", "--wc", wc, "--"}, batch...)
		if _, err := c.nativeRun(ctx, wc, args...); err != nil {
			return "", err
		}
	}
	return "", nil
}

func appendNativeStatus(out []StatusEntry, raw map[string]any) []StatusEntry {
	entries, _ := raw["entries"].([]any)
	for _, item := range entries {
		row, _ := item.(map[string]any)
		path, _ := row["path"].(string)
		if path == "." {
			continue
		}
		out = append(out, StatusEntry{
			Path:  filepath.FromSlash(path),
			Item:  fmt.Sprint(row["item"]),
			Props: fmt.Sprint(row["props"]),
		})
	}
	return out
}

func (c *execClient) nativeStatus(ctx context.Context, wc string, paths []string) ([]StatusEntry, error) {
	rels, err := nativeRelatives(wc, paths)
	if err != nil {
		return nil, err
	}
	if len(rels) == 0 {
		raw, err := c.nativeRun(ctx, wc, "status", "--wc", wc, "--depth", "infinity")
		if err != nil {
			return nil, err
		}
		return appendNativeStatus(nil, raw), nil
	}
	var out []StatusEntry
	for _, batch := range nativeBatches(rels) {
		args := append([]string{"status", "--wc", wc, "--depth", "empty", "--"}, batch...)
		raw, err := c.nativeRun(ctx, wc, args...)
		if err != nil {
			return nil, err
		}
		out = appendNativeStatus(out, raw)
	}
	return out, nil
}

func (c *execClient) nativePropset(ctx context.Context, wc, name, value string, paths []string) (string, error) {
	rels, err := nativeRelatives(wc, paths)
	if err != nil {
		return "", err
	}
	for _, batch := range nativeBatches(rels) {
		args := append([]string{"propset", "--wc", wc, name, value, "--"}, batch...)
		if _, err := c.nativeRun(ctx, wc, args...); err != nil {
			return "", err
		}
	}
	return "", nil
}

func (c *execClient) nativePropdel(ctx context.Context, wc, name string, paths []string) (string, error) {
	rels, err := nativeRelatives(wc, paths)
	if err != nil {
		return "", err
	}
	for _, batch := range nativeBatches(rels) {
		args := append([]string{"propdel", "--wc", wc, name, "--"}, batch...)
		if _, err := c.nativeRun(ctx, wc, args...); err != nil {
			return "", err
		}
	}
	return "", nil
}

func (c *execClient) nativePropget(ctx context.Context, wc, name string, paths []string) (string, error) {
	rels, err := nativeRelatives(wc, paths)
	if err != nil {
		return "", err
	}
	var targets []any
	for _, batch := range nativeBatches(rels) {
		args := append([]string{"propget", "--wc", wc, name, "--"}, batch...)
		raw, err := c.nativeRun(ctx, wc, args...)
		if err != nil {
			return "", err
		}
		part, _ := raw["targets"].([]any)
		targets = append(targets, part...)
	}
	if len(targets) == 0 {
		return "", nil
	}
	first, _ := targets[0].(map[string]any)
	value, _ := first["value"].(string)
	return value, nil
}

func (c *execClient) nativeProplist(ctx context.Context, wc, name string) (map[string]bool, error) {
	raw, err := c.nativeRun(ctx, wc, "propget", "--wc", wc, name, "--recursive")
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	targets, _ := raw["targets"].([]any)
	for _, item := range targets {
		row, _ := item.(map[string]any)
		path, _ := row["path"].(string)
		if path != "" && path != "." {
			out[path] = true
		}
	}
	return out, nil
}

func (c *execClient) nativeCleanup(ctx context.Context, wc string) (string, error) {
	_, err := c.nativeRun(ctx, wc, "cleanup", "--wc", wc)
	return "", err
}

func (c *execClient) nativeRevert(ctx context.Context, wc string, paths []string) (string, error) {
	rels, err := nativeRelatives(wc, paths)
	if err != nil {
		return "", err
	}
	for _, batch := range nativeBatches(rels) {
		args := append([]string{"revert", "--wc", wc, "--"}, batch...)
		if _, err := c.nativeRun(ctx, wc, args...); err != nil {
			return "", err
		}
	}
	return "", nil
}

func (c *execClient) nativeResolve(ctx context.Context, wc string, paths []string, accept string) (string, error) {
	rels, err := nativeRelatives(wc, paths)
	if err != nil {
		return "", err
	}
	for _, batch := range nativeBatches(rels) {
		args := append([]string{"resolve", "--wc", wc, "--accept", accept, "--"}, batch...)
		if _, err := c.nativeRun(ctx, wc, args...); err != nil {
			return "", err
		}
	}
	return "", nil
}
