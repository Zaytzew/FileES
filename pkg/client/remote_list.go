package client

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RemoteEntry is one immediate child of a repository URL. Kind is "file" or "dir".
type RemoteEntry struct {
	Name     string
	Kind     string
	Size     int64
	Revision int64
}

// ListImmediate lists one directory at HEAD. It does not recurse and it does
// not require a working copy. A configured native helper answers through the
// history list at the current revision. A developer build without that helper
// uses svn list.
func (c *execClient) ListImmediate(ctx context.Context, repoURL string) ([]RemoteEntry, error) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return nil, fmt.Errorf("svn list requires a repository URL")
	}
	if c.HistoryEnabled() {
		revision, err := c.Revision(ctx, repoURL)
		if err != nil {
			return nil, err
		}
		entries, err := c.HistoryList(ctx, repoURL, revision)
		if err != nil {
			return nil, err
		}
		out := make([]RemoteEntry, 0, len(entries))
		for _, entry := range entries {
			out = append(out, RemoteEntry{Name: entry.Name, Kind: entry.Kind, Size: entry.Size, Revision: entry.LastChangedRevision})
		}
		return out, nil
	}
	raw, err := c.run(ctx, "", []string{"list", "--xml", "--depth", "immediates", repoURL})
	if err != nil {
		return nil, err
	}
	return parseListXML(raw)
}

// CatURL writes one file at HEAD to dest. Dest must not already exist.
// Native builds use the helper. Developer builds use svn cat.
func (c *execClient) CatURL(ctx context.Context, repoURL, dest string) error {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" || !filepath.IsAbs(dest) {
		return fmt.Errorf("svn cat requires a repository URL and an absolute destination")
	}
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("svn cat refuses to overwrite %s", dest)
	}
	if c.HistoryEnabled() {
		revision, err := c.Revision(ctx, repoURL)
		if err != nil {
			return err
		}
		_, err = c.HistoryFetchFile(ctx, repoURL, revision, dest)
		return err
	}
	out, err := c.run(ctx, "", []string{"cat", repoURL})
	if err != nil {
		return err
	}
	return os.WriteFile(dest, []byte(out), 0o600)
}

// UpdateSetDepth deepens one path inside an existing working copy.
// depth is "empty", "files", "immediates" or "infinity". An empty rel is the root.
func (c *execClient) UpdateSetDepth(ctx context.Context, wc, rel, depth string) (string, error) {
	switch depth {
	case "empty", "files", "immediates", "infinity":
	default:
		return "", fmt.Errorf("unsupported depth %q", depth)
	}
	if nativeWCOps(c) {
		if e := c.nativeRequireFeature(ctx, "sparse_set_depth_v1"); e != nil {
			return "", e
		}
		args := []string{"update", "--wc", wc, "--set-depth", depth}
		if rel != "" && rel != "." {
			// --parents brings in the chain above rel at depth empty, so a
			// path deep in an otherwise empty working copy can be chosen alone.
			// The helper takes canonical repository-relative paths: forward
			// slashes on every platform, as svn itself spells them.
			args = append(args, "--parents", "--", filepath.ToSlash(rel))
		}
		r, e := c.nativeRemote(ctx, wc, args...)
		if e != nil {
			return "", e
		}
		return nativeUpdateReceipt(r)
	}
	args := []string{"update", "--set-depth", depth}
	if rel != "" && rel != "." {
		// The CLI needs the parents too; --parents makes them depth-empty.
		args = append(args, "--parents")
	}
	if rel != "" && rel != "." {
		args = append(args, rel)
	}
	return c.run(ctx, wc, args)
}

func parseListXML(output string) ([]RemoteEntry, error) {
	var doc struct {
		Entries []struct {
			Kind   string `xml:"kind,attr"`
			Name   string `xml:"name"`
			Size   int64  `xml:"size"`
			Commit struct {
				Revision int64 `xml:"revision,attr"`
			} `xml:"commit"`
		} `xml:"list>entry"`
	}
	if err := xml.Unmarshal([]byte(output), &doc); err != nil {
		return nil, fmt.Errorf("parse list xml: %w", err)
	}
	out := make([]RemoteEntry, 0, len(doc.Entries))
	for _, entry := range doc.Entries {
		name := strings.TrimSpace(entry.Name)
		kind := strings.TrimSpace(entry.Kind)
		if name == "" || (kind != "file" && kind != "dir") || strings.ContainsAny(name, `/\`) {
			return nil, fmt.Errorf("svn list entry %q kind %q is not a single path segment", name, kind)
		}
		out = append(out, RemoteEntry{Name: name, Kind: kind, Size: entry.Size, Revision: entry.Commit.Revision})
	}
	return out, nil
}
