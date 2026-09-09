package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// UpdateConflicts decodes the already validated native update receipt. The
// historical Client string result remains an envelope, not simulated CLI text.
func UpdateConflicts(out string) ([]string, bool) {
	var r struct {
		Schema    string
		OK        bool
		Conflicts []string
	}
	if json.Unmarshal([]byte(out), &r) != nil || r.Schema != nativeSchema || !r.OK || r.Conflicts == nil {
		return nil, false
	}
	return r.Conflicts, true
}

func nativeRevisionValue(raw map[string]any, field string, nullable bool) (int64, error) {
	v, present := raw[field]
	if nullable && present && v == nil {
		return 0, nil
	}
	n, ok := v.(float64)
	if !ok || n < 0 || n > 9007199254740991 || math.Trunc(n) != n {
		return 0, fmt.Errorf("native SVN: invalid %s receipt", field)
	}
	return int64(n), nil
}
func nativeJSON(raw map[string]any) string { b, _ := json.Marshal(raw); return string(b) }

func (c *execClient) nativeRemote(ctx context.Context, wc string, args ...string) (map[string]any, error) {
	return c.nativeRemoteInput(ctx, wc, nil, args...)
}

func (c *execClient) nativeRemoteInput(ctx context.Context, wc string, input []byte, args ...string) (map[string]any, error) {
	// A native WC query is offline. Require the same explicit SSH pins as CLI,
	// including for WC-relative requests where no URL appears in argv.
	for _, arg := range args {
		if strings.HasPrefix(arg, "svn+ssh://") && c.sshCommand == "" {
			return nil, errors.New("svn+ssh transport requires an installation identity and pinned known_hosts")
		}
	}
	if wc != "" {
		info, err := c.nativeInfo(ctx, wc, "")
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(info.URL, "svn+ssh://") && c.sshCommand == "" {
			return nil, errors.New("svn+ssh transport requires an installation identity and pinned known_hosts")
		}
	}
	return c.nativeCommandInput(ctx, wc, c.timeout, input, args...)
}

type nativeInfoEntry struct {
	Path           string
	URL            string
	ReposRootURL   string `json:"repos_root_url"`
	ReposUUID      string `json:"repos_uuid"`
	Kind           string
	Revision       int64
	LastChangedRev int64 `json:"last_changed_rev"`
}

func (c *execClient) nativeInfo(ctx context.Context, wc, rel string) (nativeInfoEntry, error) {
	args := []string{"info", "--inspect-wc", wc}
	if rel != "" {
		args = append(args, "--", rel)
	}
	raw, err := c.nativeRun(ctx, wc, args...)
	if err != nil {
		return nativeInfoEntry{}, err
	}
	return parseNativeInfo(raw)
}

// Locate the enclosing WC for read-only inspection. The native guard validates
// its exact root and parent chain; FileES ownership is not claimed by reading.
func nativeInfoTarget(target string) (string, string, bool) {
	if !filepath.IsAbs(target) {
		return "", "", false
	}
	p := filepath.Clean(target)
	for {
		if st, e := os.Lstat(filepath.Join(p, ".svn")); e == nil && st.IsDir() {
			rel, e := filepath.Rel(p, target)
			if e != nil {
				return "", "", false
			}
			if rel == "." {
				rel = ""
			}
			return p, filepath.ToSlash(rel), true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", "", false
		}
		p = parent
	}
}

func (c *execClient) nativeUpdate(ctx context.Context, wc string, paths []string, targeted bool) (string, error) {
	if e := c.nativeRequireUpdateChanges(ctx); e != nil {
		return "", e
	}
	args := []string{"update", "--wc", wc}
	if targeted {
		rels, e := nativeRelatives(wc, paths)
		if e != nil {
			return "", e
		}
		if len(rels) == 0 {
			return "", nil
		}
		if len(rels) > nativePathBatch {
			return "", errors.New("native update exceeds path limit")
		}
		args = append(args, "--depth", "empty", "--")
		args = append(args, rels...)
	}
	r, e := c.nativeRemote(ctx, wc, args...)
	if e != nil {
		return "", e
	}
	return nativeUpdateReceipt(r)
}
func nativeUpdateReceipt(r map[string]any) (string, error) {
	if _, e := nativeRevisionValue(r, "revision", false); e != nil {
		return "", e
	}
	values, ok := r["conflicts"].([]any)
	if !ok {
		return "", errors.New("native SVN: missing conflict list")
	}
	for _, v := range values {
		p, ok := v.(string)
		if !ok || !validMovePath(p) {
			return "", errors.New("native SVN: invalid conflict path")
		}
	}
	if _, ok := UpdateChanges(nativeJSON(r)); !ok {
		return "", errors.New("native SVN: missing or invalid incoming changes")
	}
	return nativeJSON(r), nil
}
func (c *execClient) nativeCheckout(ctx context.Context, url, wc string) (string, error) {
	if e := c.nativeRequireUpdateChanges(ctx); e != nil {
		return "", e
	}
	if !filepath.IsAbs(wc) {
		return "", errors.New("native checkout requires absolute destination")
	}
	r, e := c.nativeRemote(ctx, "", "checkout", "--url", url, "--wc", wc, "--force")
	if e != nil {
		return "", e
	}
	return nativeUpdateReceipt(r)
}

// UpdateChanges returns only successful plain A/U/D notifications, never
// merged/conflicted local work. Shared by the incoming activity journal.
func UpdateChanges(out string) (map[string]string, bool) {
	var r struct {
		Schema  string
		OK      bool
		Changes []struct {
			Path   string
			Action string
		}
	}
	if json.Unmarshal([]byte(out), &r) != nil || r.Schema != nativeSchema || !r.OK || r.Changes == nil {
		return nil, false
	}
	result := map[string]string{}
	for _, v := range r.Changes {
		if !validMovePath(v.Path) || (v.Action != "A" && v.Action != "U" && v.Action != "D") {
			return nil, false
		}
		result[v.Path] = v.Action
	}
	return result, true
}
func (c *execClient) nativeRequireUpdateChanges(ctx context.Context) error {
	return c.nativeRequireFeature(ctx, "update_changes")
}

func (c *execClient) nativeRequireFeature(ctx context.Context, feature string) error {
	r, e := c.nativeCommand(ctx, "", c.timeout, "--version")
	if e != nil {
		return e
	}
	features, _ := r["features"].([]any)
	for _, v := range features {
		if v == feature {
			return nil
		}
	}
	return fmt.Errorf("native SVN helper lacks %s; upgrade helper before mutation", feature)
}

func (c *execClient) nativeCommit(ctx context.Context, wc string, paths []string, message, marker string, keep bool) (string, int64, error) {
	rels, e := nativeRelatives(wc, paths)
	if e != nil {
		return "", 0, e
	}
	if len(rels) != len(paths) {
		return "", 0, errors.New("native commit refuses WC root targets")
	}
	input, e := nativeCommitTargets(rels)
	if e != nil {
		return "", 0, e
	}
	if e := c.nativeRequireFeature(ctx, "commit_targets_stdin_v1"); e != nil {
		return "", 0, e
	}
	args := []string{"commit", "--wc", wc, "-m", message}
	if keep {
		args = append(args, "--keep-locks")
	}
	if marker != "" {
		args = append(args, "--revprop", "filees:commit-id="+marker)
	}
	args = append(args, "--targets-stdin")
	r, e := c.nativeRemoteInput(ctx, wc, input, args...)
	if e != nil {
		return "", 0, e
	}
	rev, e := nativeRevisionValue(r, "revision", true)
	if e != nil {
		return "", 0, e
	}
	return nativeJSON(r), rev, nil
}

// NativePathResult is a path's lock outcome; process success is not path success.
type NativePathResult struct {
	Path  string  `json:"path"`
	OK    bool    `json:"ok"`
	Error *string `json:"error"`
}
type NativePathFailure struct {
	Verb    string
	Results []NativePathResult
}

func (e *NativePathFailure) Error() string {
	var s []string
	for _, r := range e.Results {
		if !r.OK && r.Error != nil {
			s = append(s, r.Path+": "+*r.Error)
		}
	}
	return "native SVN " + e.Verb + ": " + strings.Join(s, "; ")
}
func nativeLockReceipt(raw map[string]any, verb string, rels []string) ([]NativePathResult, error) {
	list, ok := raw[verb+"ed"].([]any)
	if !ok || len(list) != len(rels) {
		return nil, errors.New("native SVN: incomplete lock receipt")
	}
	expected := map[string]bool{}
	for _, p := range rels {
		if expected[p] {
			return nil, errors.New("native SVN: duplicate requested path")
		}
		expected[p] = true
	}
	results := make([]NativePathResult, 0, len(list))
	failed := false
	for _, v := range list {
		row, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("native SVN: invalid lock row")
		}
		p, ok := row["path"].(string)
		if !ok || !expected[p] {
			return nil, errors.New("native SVN: unexpected or duplicate lock path")
		}
		delete(expected, p)
		success, ok := row["ok"].(bool)
		if !ok {
			return nil, errors.New("native SVN: missing lock outcome")
		}
		r := NativePathResult{Path: p, OK: success}
		if !success {
			msg, ok := row["error"].(string)
			if !ok || msg == "" {
				return nil, errors.New("native SVN: missing lock refusal")
			}
			r.Error = &msg
			failed = true
		}
		results = append(results, r)
	}
	if failed {
		return results, &NativePathFailure{Verb: verb, Results: results}
	}
	return results, nil
}
func (c *execClient) nativeLock(ctx context.Context, wc string, paths []string, verb, comment string) (string, error) {
	rels, e := nativeRelatives(wc, paths)
	if e != nil {
		return "", e
	}
	if len(rels) == 0 {
		return "", errors.New("native lock requires explicit paths")
	}
	seen := make(map[string]bool, len(rels))
	for _, rel := range rels {
		if seen[rel] {
			return "", errors.New("native lock refuses duplicate paths before mutation")
		}
		seen[rel] = true
	}
	var all []NativePathResult
	for _, batch := range nativeBatches(rels) {
		args := []string{verb, "--wc", wc}
		if verb == "lock" && comment != "" {
			args = append(args, "-m", comment)
		}
		args = append(args, "--")
		args = append(args, batch...)
		raw, e := c.nativeRemote(ctx, wc, args...)
		if e != nil {
			return nativeJSON(map[string]any{"results": all}), e
		}
		part, e := nativeLockReceipt(raw, verb, batch)
		all = append(all, part...)
		if e != nil {
			var f *NativePathFailure
			if errors.As(e, &f) {
				f.Results = all
			}
			return nativeJSON(map[string]any{"results": all}), e
		}
	}
	return nativeJSON(map[string]any{"results": all}), nil
}

type nativeLogEntry struct {
	Revision int64
	Message  string
	Revprops map[string]string
	Paths    []struct {
		Path         string
		Action       string
		CopyfromPath *string `json:"copyfrom_path"`
		CopyfromRev  *int64  `json:"copyfrom_rev"`
	}
}

func (c *execClient) nativeLog(ctx context.Context, target, revision string, extra ...string) ([]nativeLogEntry, error) {
	args := []string{"log", "--revision", revision}
	wc := ""
	if strings.Contains(target, "://") {
		args = append(args, "--url", target)
		args = append(args, extra...)
	} else {
		var rel string
		var ok bool
		wc, rel, ok = nativeInfoTarget(target)
		if !ok {
			return nil, errors.New("native log requires URL or managed WC target")
		}
		if rel == "" {
			info, err := c.nativeInfo(ctx, wc, "")
			if err != nil {
				return nil, err
			}
			args = append(args, "--url", info.URL)
			args = append(args, extra...)
		} else {
			args = append(args, "--wc", wc)
			args = append(args, extra...)
			args = append(args, "--", rel)
		}
	}
	raw, e := c.nativeRemote(ctx, wc, args...)
	if e != nil {
		return nil, e
	}
	if _, ok := raw["entries"].([]any); !ok {
		return nil, errors.New("native log missing entries")
	}
	var doc struct{ Entries []nativeLogEntry }
	if e = json.Unmarshal([]byte(nativeJSON(raw)), &doc); e != nil {
		return nil, e
	}
	for _, v := range doc.Entries {
		if v.Revision <= 0 {
			return nil, errors.New("native log invalid revision")
		}
	}
	return doc.Entries, nil
}

// CatTo uses the native streaming-to-file protocol. The caller owns the unique
// output path; neither this method nor the helper overwrites existing files.
func (c *execClient) CatTo(ctx context.Context, url, out string) error {
	if !nativeWCOps(c) {
		return errors.New("native cat is not enabled on this platform")
	}
	r, e := c.nativeRemote(ctx, "", "cat", "--url", url, "--out", out)
	if e != nil {
		return e
	}
	n, e := nativeRevisionValue(r, "bytes", false)
	if e != nil {
		return e
	}
	st, e := os.Stat(out)
	if e != nil {
		return e
	}
	if !st.Mode().IsRegular() || st.Size() != n {
		return errors.New("native cat size does not match receipt")
	}
	return nil
}

func (c *execClient) nativeVerifyCommittedMove(ctx context.Context, wc, old, dst string) (bool, error) {
	info, e := c.nativeInfo(ctx, wc, dst)
	if e != nil {
		return false, e
	}
	u, e := url.Parse(info.URL)
	if e != nil {
		return false, e
	}
	r, e := url.Parse(info.ReposRootURL)
	if e != nil {
		return false, e
	}
	prefix := strings.TrimSuffix(r.Path, "/") + "/"
	if u.Scheme != r.Scheme || u.Host != r.Host || !strings.HasPrefix(u.Path, prefix) {
		return false, errors.New("invalid move receipt repository URL")
	}
	dstRepo := strings.TrimPrefix(u.Path, strings.TrimSuffix(r.Path, "/"))
	if !strings.HasSuffix(dstRepo, "/"+dst) || info.LastChangedRev <= 0 {
		return false, nil
	}
	oldRepo := strings.TrimSuffix(dstRepo, "/"+dst) + "/" + old
	entries, e := c.nativeLog(ctx, filepath.Join(wc, filepath.FromSlash(dst)), fmt.Sprint(info.LastChangedRev), "--changed-paths", "--peg-base")
	if e != nil {
		return false, e
	}
	if len(entries) != 1 {
		return false, nil
	}
	deleted, copied, successors := false, false, 0
	for _, p := range entries[0].Paths {
		if p.Path == oldRepo && p.Action == "D" {
			deleted = true
		}
		if p.CopyfromPath != nil && *p.CopyfromPath == oldRepo {
			successors++
			if p.Path == dstRepo && p.Action == "A" && p.CopyfromRev != nil && *p.CopyfromRev >= 0 {
				copied = true
			}
		}
	}
	return deleted && copied && successors == 1, nil
}
