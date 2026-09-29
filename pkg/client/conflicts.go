package client

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"path/filepath"
)

// ConflictReader observes the WC database without mutation or a CLI fallback
// from the native adapter. Missing evidence must prevent automatic resolution.
type ConflictReader interface {
	ConflictDetails(context.Context, string, string) ([]ConflictDetail, error)
}

// ConflictDetail contains WC-relative artifact paths reported by SVN. Mine may
// be absent for binary conflicts: in that case the working file is the local
// version. An empty result means that SVN reports no conflict, not unsupported.
type ConflictDetail struct {
	Type   string `json:"type" xml:"type,attr"`
	Base   string `json:"base" xml:"prev-base-file"`
	Mine   string `json:"mine" xml:"prev-wc-file"`
	Theirs string `json:"theirs" xml:"cur-base-file"`
}

func (c *execClient) ConflictDetails(ctx context.Context, wc, path string) ([]ConflictDetail, error) {
	rels, err := nativeRelatives(wc, []string{path})
	if err != nil || len(rels) != 1 {
		return nil, errors.New("conflict inspection requires one WC file")
	}
	var details []ConflictDetail
	if nativeWCOps(c) {
		raw, err := c.nativeRun(ctx, wc, "info", "--inspect-wc", wc, "--", rels[0])
		if err != nil {
			return nil, err
		}
		details, err = parseNativeConflicts(raw, rels[0])
		if err != nil {
			return nil, err
		}
	} else {
		target := filepath.Join(wc, filepath.FromSlash(rels[0]))
		out, err := c.run(ctx, wc, []string{"info", "--xml", "--depth", "empty", "--", target + "@"})
		if err != nil {
			return nil, err
		}
		var doc struct {
			Entries []struct {
				Path      string           `xml:"path,attr"`
				Conflicts []ConflictDetail `xml:"conflict"`
				Tree      []struct{}       `xml:"tree-conflict"`
			} `xml:"entry"`
		}
		if err := xml.Unmarshal([]byte(out), &doc); err != nil {
			return nil, err
		}
		if len(doc.Entries) != 1 {
			return nil, errors.New("conflict inspection requires exactly one info entry")
		}
		observed := doc.Entries[0].Path
		if !filepath.IsAbs(observed) {
			observed = filepath.Join(wc, observed)
		}
		if filepath.Clean(observed) != filepath.Clean(target) {
			return nil, errors.New("conflict inspection returned a different target")
		}
		details = doc.Entries[0].Conflicts
		for range doc.Entries[0].Tree {
			details = append(details, ConflictDetail{Type: "tree"})
		}
	}
	return normalizeConflictDetails(wc, rels[0], details)
}

func parseNativeConflicts(raw map[string]any, target string) ([]ConflictDetail, error) {
	rows, ok := raw["entries"].([]any)
	if !ok || len(rows) != 1 {
		return nil, errors.New("conflict inspection requires exactly one info entry")
	}
	row, ok := rows[0].(map[string]any)
	if !ok || row["path"] != target {
		return nil, errors.New("conflict inspection returned a different target")
	}
	if _, ok := row["conflicts"].([]any); !ok {
		return nil, errors.New("native helper omitted conflict metadata")
	}
	encoded, err := json.Marshal(row["conflicts"])
	if err != nil {
		return nil, err
	}
	var details []ConflictDetail
	if err := json.Unmarshal(encoded, &details); err != nil {
		return nil, err
	}
	return details, nil
}

func normalizeConflictDetails(wc, target string, details []ConflictDetail) ([]ConflictDetail, error) {
	for i := range details {
		d := &details[i]
		switch d.Type {
		case "tree", "property":
			continue
		case "text":
		default:
			return nil, errors.New("unknown SVN conflict type")
		}
		for _, path := range []*string{&d.Base, &d.Mine, &d.Theirs} {
			if *path == "" {
				continue
			}
			if !filepath.IsAbs(*path) {
				return nil, errors.New("SVN conflict artifact is not absolute")
			}
			rel, err := filepath.Rel(wc, *path)
			if err != nil || !validMovePath(filepath.ToSlash(rel)) || filepath.Dir(rel) != filepath.Dir(filepath.FromSlash(target)) || rel == filepath.FromSlash(target) {
				return nil, errors.New("SVN conflict artifact outside target directory")
			}
			*path = filepath.ToSlash(rel)
		}
	}
	return details, nil
}
