package historyexport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"filees/pkg/client"
	"filees/pkg/provisioning"
	"filees/pkg/runtime"
)

// Operation states. planned waits for the user's confirmation; complete is
// claimed only after every planned file was fetched or reported skipped and the
// result sits under its final name.
const (
	StatePlanning    = "planning"
	StatePlanned     = "planned"
	StateFetching    = "fetching"
	StateFinalizing  = "finalizing"
	StateComplete    = "complete"
	StateFailed      = "failed"
	StateCancelled   = "cancelled"
	StateInterrupted = "interrupted"
)

const (
	// ReportName is written at the root of every export and reserved there.
	ReportName = "filees-export.json"
	// ReasonSpecial marks a symbolic link the helper declined to write as data.
	ReasonSpecial = "special"

	stagePrefix       = ".filees-export-"
	spaceMargin       = 64 << 20
	defaultBatchFiles = 200
	defaultBatchBytes = 64 << 20
)

var (
	ErrInvalidRequest    = errors.New("history export: invalid request")
	ErrDestination       = errors.New("history export: destination refused")
	ErrInsufficientSpace = errors.New("history export: not enough free space")
	ErrUnknownOperation  = errors.New("history export: unknown operation")
	ErrState             = errors.New("history export: operation is not in a state for that")
)

// Reader is the part of the SVN client an export uses.
type Reader interface {
	HistoryListTree(ctx context.Context, dirURL string, revision int64, planFile string) (client.HistoryTreeSummary, error)
	HistoryFetchTree(ctx context.Context, rootURL string, revision int64, dest string, pairs []client.HistoryTreePair) (client.HistoryTreeReceipt, error)
}

// Request is pinned when the export begins: later commits, a moved slider or a
// new HEAD do not change what it copies.
type Request struct {
	ID             string `json:"id"`
	ServerID       string `json:"server_id"`
	RepoID         string `json:"repo_id"`
	RepoName       string `json:"repo_name"`
	RepoURL        string `json:"repo_url"`
	RepositoryUUID string `json:"repository_uuid"`
	Revision       int64  `json:"revision"`
	// Moment is the historical moment in the zone the user saw; it names the folder.
	Moment time.Time `json:"moment"`
	// Subtree limits the export to one repository folder; "" is the whole state.
	Subtree string `json:"subtree,omitempty"`
	// Files is an explicit selection of files and wins over Subtree.
	Files  []Node `json:"files,omitempty"`
	Parent string `json:"parent"`
}

type Record struct {
	Request
	State          string    `json:"state"`
	Stage          string    `json:"stage"`
	Final          string    `json:"final,omitempty"`
	FilesTotal     int64     `json:"files_total"`
	FilesDone      int64     `json:"files_done"`
	BytesTotal     int64     `json:"bytes_total"`
	BytesDone      int64     `json:"bytes_done"`
	SpaceAvailable int64     `json:"space_available"`
	Renamed        []Rename  `json:"renamed,omitempty"`
	Skipped        []Skip    `json:"skipped,omitempty"`
	WhaleExcluded  bool      `json:"whale_excluded,omitempty"`
	WhaleFiles     int64     `json:"whale_files,omitempty"`
	WhaleBytes     int64     `json:"whale_bytes,omitempty"`
	Error          string    `json:"error,omitempty"`
	CleanupError   string    `json:"cleanup_error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Runner owns export operations and their journal. Records are files, one per
// operation, so a restart can tell a half-moved result from a staging area to
// remove.
type Runner struct {
	Journal   string
	Reader    func(serverID string) (Reader, error)
	Roots     func() []string
	FoldCase  bool
	Admission *runtime.Admission
	// Available and Now default to the filesystem and the clock.
	Available  func(path string) (int64, error)
	Now        func() time.Time
	BatchFiles int
	BatchBytes int64

	store  sync.Mutex
	mu     sync.Mutex
	active map[string]*activeOperation
	plans  map[string]Plan
}

type activeOperation struct {
	cancel    context.CancelFunc
	done      chan struct{}
	cancelled bool
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) available(path string) (int64, error) {
	if r.Available != nil {
		return r.Available(path)
	}
	return filesystemAvailable(path)
}

func (r *Runner) recordPath(id string) string { return filepath.Join(r.Journal, id+".json") }

func (r *Runner) load(id string) (Record, error) {
	if !validID(id) {
		return Record{}, ErrUnknownOperation
	}
	data, err := os.ReadFile(r.recordPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, ErrUnknownOperation
	}
	if err != nil {
		return Record{}, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, fmt.Errorf("history export: journal %s: %w", id, err)
	}
	return rec, nil
}

func (r *Runner) save(rec Record) error {
	rec.UpdatedAt = r.now().UTC()
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(r.Journal, 0700); err != nil {
		return err
	}
	tmp := r.recordPath(rec.ID) + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, r.recordPath(rec.ID))
}

// update applies change to the stored record under the journal lock.
func (r *Runner) update(id string, change func(*Record)) (Record, error) {
	r.store.Lock()
	defer r.store.Unlock()
	rec, err := r.load(id)
	if err != nil {
		return Record{}, err
	}
	change(&rec)
	return rec, r.save(rec)
}

func (r *Runner) Get(id string) (Record, error) {
	r.store.Lock()
	defer r.store.Unlock()
	return r.load(id)
}

func (r *Runner) List() ([]Record, error) {
	entries, err := os.ReadDir(r.Journal)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !validID(id) {
			continue
		}
		rec, err := r.Get(id)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// insideWorkingCopy walks every ancestor of path for Subversion metadata. The
// managed roots are checked separately; this also catches a copy FileES does
// not manage, because a download inside one would still become a local change.
func insideWorkingCopy(path string) bool {
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if info, err := os.Stat(filepath.Join(dir, ".svn")); err == nil && info.IsDir() {
			return true
		}
		if filepath.Dir(dir) == dir {
			return false
		}
	}
}

// Begin validates the destination, creates the operation's own staging folder
// and starts planning in the background.
func (r *Runner) Begin(req Request) (Record, error) {
	if !validID(req.ID) || req.RepoURL == "" || !strings.Contains(req.RepoURL, "://") || req.Revision < 1 ||
		req.Moment.IsZero() || !filepath.IsAbs(req.Parent) || (req.Subtree != "" && !validRepoPath(req.Subtree)) {
		return Record{}, ErrInvalidRequest
	}
	for _, f := range req.Files {
		if f.Kind != "file" || !validRepoPath(f.Path) || f.Size < 0 {
			return Record{}, ErrInvalidRequest
		}
	}
	if _, err := r.Get(req.ID); !errors.Is(err, ErrUnknownOperation) {
		return Record{}, ErrState
	}
	if info, err := os.Stat(req.Parent); err != nil || !info.IsDir() {
		return Record{}, fmt.Errorf("%w: parent is not an existing folder", ErrDestination)
	}
	var roots []string
	if r.Roots != nil {
		roots = r.Roots()
	}
	check, err := provisioning.PreflightLocalPath(filepath.Join(req.Parent, stagePrefix+req.ID), provisioning.LocalPathAttach, roots)
	if err != nil {
		return Record{}, fmt.Errorf("%w: %v", ErrDestination, err)
	}
	if insideWorkingCopy(check.CanonicalPath) {
		return Record{}, fmt.Errorf("%w: inside a Subversion working copy", ErrDestination)
	}
	if err := os.Mkdir(check.CanonicalPath, 0700); err != nil {
		return Record{}, fmt.Errorf("%w: %v", ErrDestination, err)
	}
	if err := os.Mkdir(filepath.Join(check.CanonicalPath, "tree"), 0700); err != nil {
		_ = os.Remove(check.CanonicalPath)
		return Record{}, fmt.Errorf("%w: %v", ErrDestination, err)
	}
	rec := Record{Request: req, State: StatePlanning, Stage: check.CanonicalPath, CreatedAt: r.now().UTC()}
	r.store.Lock()
	err = r.save(rec)
	r.store.Unlock()
	if err != nil {
		_ = os.RemoveAll(check.CanonicalPath)
		return Record{}, err
	}
	r.start(rec.ID, func(ctx context.Context) { r.plan(ctx, rec) })
	return rec, nil
}

func (r *Runner) start(id string, run func(context.Context)) {
	ctx, cancel := context.WithCancel(context.Background())
	op := &activeOperation{cancel: cancel, done: make(chan struct{})}
	r.mu.Lock()
	if r.active == nil {
		r.active = make(map[string]*activeOperation)
	}
	r.active[id] = op
	r.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			r.mu.Lock()
			delete(r.active, id)
			r.mu.Unlock()
			close(op.done)
		}()
		run(ctx)
	}()
}

func (r *Runner) wasCancelled(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	op := r.active[id]
	return op != nil && op.cancelled
}

func withAncestors(nodes []Node) []Node {
	seen := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		seen[node.Path] = true
	}
	out := append([]Node(nil), nodes...)
	for _, node := range nodes {
		for dir, _ := splitPath(node.Path); dir != ""; dir, _ = splitPath(dir) {
			if !seen[dir] {
				seen[dir] = true
				out = append(out, Node{Path: dir, Kind: "dir", Size: -1})
			}
		}
	}
	return out
}

func repoURL(root, path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.TrimSuffix(root, "/") + "/" + strings.Join(segments, "/")
}

func (r *Runner) plan(ctx context.Context, rec Record) {
	reader, err := r.Reader(rec.ServerID)
	if err != nil {
		r.finish(rec, err)
		return
	}
	var nodes []Node
	if len(rec.Files) > 0 {
		nodes = withAncestors(rec.Files)
	} else {
		target := rec.RepoURL
		if rec.Subtree != "" {
			target = repoURL(rec.RepoURL, rec.Subtree)
		}
		planFile := filepath.Join(rec.Stage, "plan.ndjson")
		if _, err := reader.HistoryListTree(ctx, target, rec.Revision, planFile); err != nil {
			r.finish(rec, err)
			return
		}
		err = client.EachHistoryTreeNode(planFile, func(node client.HistoryTreeNode) error {
			path := node.Path
			if rec.Subtree != "" {
				path = rec.Subtree + "/" + path
			}
			nodes = append(nodes, Node{Path: path, Kind: node.Kind, Size: node.Size})
			return nil
		})
		if err != nil {
			r.finish(rec, err)
			return
		}
		if rec.Subtree != "" {
			nodes = withAncestors(append(nodes, Node{Path: rec.Subtree, Kind: "dir", Size: -1}))
		}
	}
	plan, err := Build(nodes, Options{FoldCase: r.FoldCase, Reserved: []string{ReportName}})
	if err != nil {
		r.finish(rec, err)
		return
	}
	space, err := r.available(rec.Parent)
	if err != nil {
		r.finish(rec, err)
		return
	}
	if ctx.Err() != nil {
		r.finish(rec, ctx.Err())
		return
	}
	r.mu.Lock()
	if r.plans == nil {
		r.plans = make(map[string]Plan)
	}
	r.plans[rec.ID] = plan
	r.mu.Unlock()
	if _, err := r.update(rec.ID, func(stored *Record) {
		stored.State = StatePlanned
		stored.FilesTotal = int64(len(plan.Files))
		stored.BytesTotal = plan.Bytes
		stored.SpaceAvailable = space
		stored.Renamed, stored.Skipped = plan.Renamed, plan.Skipped
		stored.WhaleExcluded, stored.WhaleFiles, stored.WhaleBytes = plan.WhaleExcluded, plan.WhaleFiles, plan.WhaleBytes
	}); err != nil {
		r.finish(rec, err)
	}
}

// Confirm starts the transfer of a planned export. Free space is measured
// again, because the confirmation dialog may have stayed open for a while.
func (r *Runner) Confirm(id string) (Record, error) {
	rec, err := r.Get(id)
	if err != nil {
		return Record{}, err
	}
	r.mu.Lock()
	plan, known := r.plans[id]
	_, running := r.active[id]
	r.mu.Unlock()
	if rec.State != StatePlanned || !known || running {
		return rec, ErrState
	}
	space, err := r.available(rec.Parent)
	if err != nil {
		return rec, err
	}
	if rec.BytesTotal+spaceMargin > space {
		rec, _ = r.update(id, func(stored *Record) { stored.SpaceAvailable = space })
		return rec, ErrInsufficientSpace
	}
	rec, err = r.update(id, func(stored *Record) { stored.State, stored.SpaceAvailable = StateFetching, space })
	if err != nil {
		return rec, err
	}
	r.start(id, func(ctx context.Context) { r.fetch(ctx, rec, plan) })
	return rec, nil
}

// Cancel stops an export that has not reached its final name and removes its
// staging folder. It waits for a running step to stop, so the answer is final.
func (r *Runner) Cancel(id string) (Record, error) {
	rec, err := r.Get(id)
	if err != nil {
		return Record{}, err
	}
	switch rec.State {
	case StateComplete, StateFailed, StateCancelled, StateInterrupted:
		return rec, nil
	case StateFinalizing:
		return rec, ErrState
	}
	r.mu.Lock()
	op := r.active[id]
	if op != nil {
		op.cancelled = true
		op.cancel()
	}
	r.mu.Unlock()
	if op != nil {
		<-op.done
	}
	rec, err = r.Get(id)
	if err != nil {
		return Record{}, err
	}
	if rec.State == StatePlanning || rec.State == StatePlanned || rec.State == StateFetching {
		rec = r.close(rec, StateCancelled, nil)
	}
	return rec, nil
}

// finish ends a step that did not reach its goal: cancelled if the user asked,
// failed otherwise. Either way only the operation's own staging is removed.
func (r *Runner) finish(rec Record, cause error) {
	state := StateFailed
	if r.wasCancelled(rec.ID) {
		state, cause = StateCancelled, nil
	}
	r.close(rec, state, cause)
}

func (r *Runner) close(rec Record, state string, cause error) Record {
	cleanup := r.removeStage(rec)
	r.mu.Lock()
	delete(r.plans, rec.ID)
	r.mu.Unlock()
	stored, err := r.update(rec.ID, func(stored *Record) {
		stored.State = state
		if cause != nil {
			stored.Error = cause.Error()
		}
		if cleanup != nil {
			stored.CleanupError = cleanup.Error()
		}
	})
	if err != nil {
		return rec
	}
	return stored
}

// removeStage deletes only a folder this operation created: the name carries
// the operation ID, and nothing else is ever removed.
func (r *Runner) removeStage(rec Record) error {
	if !filepath.IsAbs(rec.Stage) || filepath.Base(rec.Stage) != stagePrefix+rec.ID {
		return fmt.Errorf("history export: refusing to remove unexpected staging path %q", rec.Stage)
	}
	if err := os.RemoveAll(rec.Stage); err != nil {
		return err
	}
	return nil
}

func (r *Runner) fetch(ctx context.Context, rec Record, plan Plan) {
	release, err := r.Admission.EnterContext(ctx)
	if err != nil {
		r.finish(rec, err)
		return
	}
	defer release()
	reader, err := r.Reader(rec.ServerID)
	if err != nil {
		r.finish(rec, err)
		return
	}
	tree := filepath.Join(rec.Stage, "tree")
	for _, dir := range plan.Dirs {
		if err := os.Mkdir(filepath.Join(tree, filepath.FromSlash(dir)), 0700); err != nil {
			r.finish(rec, err)
			return
		}
	}
	batchFiles, batchBytes := r.BatchFiles, r.BatchBytes
	if batchFiles <= 0 {
		batchFiles = defaultBatchFiles
	}
	if batchBytes <= 0 {
		batchBytes = defaultBatchBytes
	}
	byLocal := make(map[string]File, len(plan.Files))
	var specials []Skip
	var batch []client.HistoryTreePair
	var pending int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		receipt, err := reader.HistoryFetchTree(ctx, rec.RepoURL, rec.Revision, tree, batch)
		if err != nil {
			return err
		}
		var bytes int64
		for _, file := range receipt.Files {
			bytes += file.Bytes
		}
		for _, skip := range receipt.Skipped {
			planned := byLocal[skip.LocalPath]
			specials = append(specials, Skip{RepoPath: planned.RepoPath, Reason: ReasonSpecial, Files: 1, Bytes: planned.Size})
		}
		accounted := int64(len(receipt.Files) + len(receipt.Skipped))
		batch, pending = batch[:0], 0
		_, err = r.update(rec.ID, func(stored *Record) {
			stored.FilesDone += accounted
			stored.BytesDone += bytes
		})
		return err
	}
	for _, file := range plan.Files {
		byLocal[file.LocalPath] = file
		batch = append(batch, client.HistoryTreePair{RepoPath: file.RepoPath, LocalPath: file.LocalPath})
		pending += file.Size
		if len(batch) >= batchFiles || pending >= batchBytes {
			if err := flush(); err != nil {
				r.finish(rec, err)
				return
			}
		}
	}
	if err := flush(); err != nil {
		r.finish(rec, err)
		return
	}
	current, err := r.Get(rec.ID)
	if err == nil && current.FilesDone != int64(len(plan.Files)) {
		err = fmt.Errorf("history export: %d of %d files accounted for", current.FilesDone, len(plan.Files))
	}
	if err == nil {
		err = r.writeReport(tree, current, specials)
	}
	var final string
	if err == nil {
		final, err = r.reserveFinal(current)
	}
	if err != nil {
		r.finish(rec, err)
		return
	}
	current, err = r.update(rec.ID, func(stored *Record) {
		stored.State, stored.Final = StateFinalizing, final
		stored.Skipped = append(stored.Skipped, specials...)
	})
	if err != nil {
		return
	}
	r.mu.Lock()
	delete(r.plans, rec.ID)
	r.mu.Unlock()
	r.finalize(current)
}

type exportReport struct {
	Schema         string   `json:"schema"`
	ServerID       string   `json:"server_id"`
	RepoID         string   `json:"repo_id"`
	RepositoryName string   `json:"repository_name"`
	RepositoryUUID string   `json:"repository_uuid"`
	Revision       int64    `json:"revision"`
	Moment         string   `json:"moment"`
	ExportedAt     string   `json:"exported_at"`
	Subtree        string   `json:"subtree,omitempty"`
	Selected       []string `json:"selected,omitempty"`
	Files          int64    `json:"files"`
	Bytes          int64    `json:"bytes"`
	Renamed        []Rename `json:"renamed"`
	Skipped        []Skip   `json:"skipped"`
	WhaleExcluded  bool     `json:"whale_excluded"`
	WhaleFiles     int64    `json:"whale_files"`
	WhaleBytes     int64    `json:"whale_bytes"`
}

// writeReport leaves the explanation next to the data: what was renamed, what
// was left out and why, and that Whale payloads are not part of this copy.
func (r *Runner) writeReport(tree string, rec Record, specials []Skip) error {
	report := exportReport{
		Schema: "filees.history-export/v1", ServerID: rec.ServerID, RepoID: rec.RepoID,
		RepositoryName: rec.RepoName, RepositoryUUID: rec.RepositoryUUID, Revision: rec.Revision,
		Moment: rec.Moment.Format(time.RFC3339), ExportedAt: r.now().UTC().Format(time.RFC3339),
		Subtree: rec.Subtree, Files: rec.FilesDone - int64(len(specials)), Bytes: rec.BytesDone,
		Renamed: append([]Rename{}, rec.Renamed...), Skipped: append(append([]Skip{}, rec.Skipped...), specials...),
		WhaleExcluded: rec.WhaleExcluded, WhaleFiles: rec.WhaleFiles, WhaleBytes: rec.WhaleBytes,
	}
	for _, file := range rec.Files {
		report.Selected = append(report.Selected, file.Path)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(tree, ReportName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// reserveFinal takes a new folder name with Mkdir, which never adopts an
// existing folder, not even an empty one; a taken name moves on to _2.
func (r *Runner) reserveFinal(rec Record) (string, error) {
	name, err := FolderName(rec.RepoName, rec.Moment, rec.Revision)
	if err != nil {
		return "", err
	}
	for n := 1; n <= 1000; n++ {
		candidate := name
		if n > 1 {
			candidate = fmt.Sprintf("%s_%d", name, n)
		}
		path := filepath.Join(filepath.Dir(rec.Stage), candidate)
		err := os.Mkdir(path, 0700)
		if err == nil {
			return path, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("history export: no free folder name")
}

// finalize moves the staged tree under the reserved name. It is also the
// restart path, so a child already moved is simply no longer in staging.
func (r *Runner) finalize(rec Record) {
	tree := filepath.Join(rec.Stage, "tree")
	entries, err := os.ReadDir(tree)
	if errors.Is(err, os.ErrNotExist) {
		entries, err = nil, nil
	}
	for _, entry := range entries {
		if err != nil {
			break
		}
		destination := filepath.Join(rec.Final, entry.Name())
		if _, statErr := os.Lstat(destination); statErr == nil {
			err = fmt.Errorf("history export: %s already exists in the result", destination)
			break
		}
		err = os.Rename(filepath.Join(tree, entry.Name()), destination)
	}
	if err != nil {
		// Half moved: both folders stay, and the record names them.
		_, _ = r.update(rec.ID, func(stored *Record) {
			stored.State = StateFailed
			stored.Error = fmt.Sprintf("result incomplete in %s, remainder in %s: %v", rec.Final, rec.Stage, err)
		})
		return
	}
	cleanup := r.removeStage(rec)
	_, _ = r.update(rec.ID, func(stored *Record) {
		stored.State = StateComplete
		if cleanup != nil {
			stored.CleanupError = cleanup.Error()
		}
	})
}

// Recover runs once at daemon start. Work that had not reached its final name
// is interrupted and its staging removed; work that had is moved into place.
func (r *Runner) Recover() error {
	records, err := r.List()
	if err != nil {
		return err
	}
	for _, rec := range records {
		switch rec.State {
		case StatePlanning, StatePlanned, StateFetching:
			r.close(rec, StateInterrupted, errors.New("daemon restarted before the export finished"))
		case StateFinalizing:
			r.finalize(rec)
		}
	}
	return nil
}
