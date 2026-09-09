package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
	"unicode/utf16"
)

func (c *execClient) nativeRun(ctx context.Context, wc string, args ...string) (map[string]any, error) {
	if !filepath.IsAbs(c.nativeSVNPath) || !filepath.IsAbs(wc) {
		return nil, errors.New("native SVN: explicit absolute executable and WC required")
	}
	return c.nativeCommand(ctx, wc, min(c.timeout, 30*time.Second), args...)
}

// nativeCommand shares serialization and bounded receipts with WC-local calls.
// Remote operations retain the configured transfer timeout, not the 30s WC cap.
func (c *execClient) nativeCommand(ctx context.Context, dir string, timeout time.Duration, args ...string) (map[string]any, error) {
	return c.nativeCommandInput(ctx, dir, timeout, nil, args...)
}

func (c *execClient) nativeCommandInput(ctx context.Context, dir string, timeout time.Duration, input []byte, args ...string) (map[string]any, error) {
	if !filepath.IsAbs(c.nativeSVNPath) || (dir != "" && !filepath.IsAbs(dir)) || len(args) == 0 {
		return nil, errors.New("native SVN: invalid executable, directory or command")
	}
	if runtime.GOOS == "windows" {
		units := nativeArgumentUnits(c.nativeSVNPath)
		for _, arg := range args {
			units += nativeArgumentUnits(arg)
		}
		if units > 30000 {
			return nil, errors.New("native SVN: command exceeds Windows argument budget before execution")
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.nativeSVNPath, args...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	cmd.Dir = dir
	cmd.Env = svnProcessEnvironment(os.Environ(), c.sshCommand)
	stdout := nativeOutput{max: nativeListingLimit}
	stderr := nativeOutput{max: nativeReceiptLimit}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil || stdout.truncated || stderr.truncated {
		// errors.Join keeps the deadline reachable by errors.Is: a killed
		// process reports only "signal: killed", and losing the difference
		// between a timeout and a refusal is how a retryable fault gets shown
		// as a permanent one.
		return nil, nativeFault(args[0], errors.Join(err, ctx.Err()),
			stdout.truncated || stderr.truncated, stdout.buffer.String(), stderr.buffer.String())
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.buffer.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("native SVN %q returned invalid JSON: %v\n%s", args[0], err, stdout.buffer.String())
	}
	if result["schema"] != nativeSchema || result["ok"] != true {
		return nil, nativeFault(args[0], nil, false, stdout.buffer.String(), stderr.buffer.String())
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
	for start := 0; start < len(paths); {
		end, units := start, 0
		for end < len(paths) && end-start < nativePathBatch {
			next := nativeArgumentUnits(paths[end])
			if end > start && units+next > 12000 {
				break
			}
			units += next
			end++
		}
		out = append(out, paths[start:end])
		start = end
	}
	return out
}

// Conservative upper bound after Windows quoting (including separators).
// Counting paths alone lets 512 long Unicode names overflow CreateProcess.
func nativeArgumentUnits(value string) int { return 2*len(utf16.Encode([]rune(value))) + 3 }

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
		raw, err := c.nativeRun(ctx, wc, "status", "--inspect-wc", wc, "--depth", "infinity")
		if err != nil {
			return nil, err
		}
		return appendNativeStatus(nil, raw), nil
	}
	var out []StatusEntry
	for _, batch := range nativeBatches(rels) {
		args := append([]string{"status", "--inspect-wc", wc, "--depth", "empty", "--"}, batch...)
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
