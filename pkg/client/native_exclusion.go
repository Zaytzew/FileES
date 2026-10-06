package client

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// PathExclusion is a local SVN metadata snapshot, NOT a filesystem observation
// or permission to overwrite a path. A release coordinator must compare the
// repository UUID, URL and revision to its retained evidence and separately
// inspect/fence the current disk object before any recovery action.
type PathExclusion struct {
	Excluded          bool
	Path              string
	WC                string
	URL               string
	RepositoryRootURL string
	RepositoryUUID    string
	Revision          int64
}

// InspectPathExclusion does not contact the repository or read file bytes.
// Missing metadata/errors/older helpers never mean "excluded". No CLI
// fallback: a missing status row cannot prove a sparse exclusion.
func (c *execClient) InspectPathExclusion(ctx context.Context, wc, rel string) (PathExclusion, error) {
	if !filepath.IsAbs(wc) || !validMovePath(rel) || filepath.ToSlash(rel) != rel {
		return PathExclusion{}, errors.New("exclusion inspection requires an absolute WC and canonical data path")
	}
	if !nativeWCOps(c) {
		return PathExclusion{}, errors.ErrUnsupported
	}
	if err := c.nativeRequireFeature(ctx, "sparse_inspect_excluded_v1"); err != nil {
		return PathExclusion{}, errors.Join(errors.ErrUnsupported, err)
	}
	wc = filepath.Clean(wc)
	raw, err := c.nativeRun(ctx, wc, "inspect-excluded", "--wc", wc, "--", rel)
	if err != nil {
		return PathExclusion{}, err
	}
	return parsePathExclusion(raw, wc, rel)
}

func parsePathExclusion(raw map[string]any, wc, rel string) (PathExclusion, error) {
	var out PathExclusion
	if raw["schema"] != nativeSchema || raw["ok"] != true {
		return out, errors.New("invalid exclusion inspection envelope")
	}
	state, _ := raw["state"].(string)
	if state != "excluded" && state != "not_excluded" {
		return out, errors.New("unknown exclusion inspection state")
	}
	field := func(name string) (string, error) {
		s, ok := raw[name].(string)
		if !ok || s == "" || strings.ContainsAny(s, "\x00\r\n") {
			return "", fmt.Errorf("invalid exclusion inspection %s", name)
		}
		return s, nil
	}
	values := []*string{&out.Path, &out.WC, &out.URL, &out.RepositoryRootURL, &out.RepositoryUUID}
	for i, name := range []string{"path", "wc", "url", "repos_root_url", "repos_uuid"} {
		s, err := field(name)
		if err != nil {
			return PathExclusion{}, err
		}
		*values[i] = s
	}
	if out.Path != rel || out.WC != filepath.ToSlash(filepath.Clean(wc)) {
		return PathExclusion{}, errors.New("exclusion inspection names another WC or path")
	}
	for _, s := range []string{out.URL, out.RepositoryRootURL} {
		u, err := url.Parse(s)
		if err != nil || !u.IsAbs() || u.RawQuery != "" || u.Fragment != "" {
			return PathExclusion{}, errors.New("invalid exclusion inspection repository URL")
		}
	}
	if !strings.HasPrefix(out.URL, strings.TrimSuffix(out.RepositoryRootURL, "/")+"/") {
		return PathExclusion{}, errors.New("exclusion inspection URL is outside its repository")
	}
	revision, err := nativeRevisionValue(raw, "revision", false)
	if err != nil || revision < 1 {
		return PathExclusion{}, errors.New("exclusion inspection lacks a committed revision")
	}
	out.Revision, out.Excluded = revision, state == "excluded"
	return out, nil
}
