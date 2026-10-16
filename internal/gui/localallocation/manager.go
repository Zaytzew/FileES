package localallocation

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"filees/pkg/privatefile"
)

// Root is supplied by the existing GUI projection, never by a browser path.
// Hints contains relative directories and change tokens from journal events.
type Root struct {
	Server, Repo, Path string
	Hints              map[string]string
}
type Value struct {
	Bytes      int64  `json:"bytes"`
	Known      bool   `json:"known"`
	State      string `json:"state"`
	MeasuredAt string `json:"measured_at,omitempty"`
}
type diskCache struct {
	Version int               `json:"version"`
	Records map[string]record `json:"records"`
}

const maxCachedRoots = 1024

// Manager owns exactly one worker. Update and Values touch memory only.
// The disk cache stores directory totals, not a second index of document data.
type Manager struct {
	mu      sync.Mutex
	path    string
	roots   []Root
	busy    bool
	results map[string]Value
	wake    chan struct{}
}

func New(path string) *Manager {
	return &Manager{path: path, results: map[string]Value{}, wake: make(chan struct{}, 1)}
}
func rootKey(r Root) string { return r.Server + "\x00" + r.Repo + "\x00" + r.Path }
func normalized(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}
func validRoot(path string) bool {
	return filepath.IsAbs(path) && filepath.Dir(path) != path
}
func overlaps(a, b string) bool {
	a, b = normalized(a), normalized(b)
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}

func (m *Manager) Update(roots []Root, busy bool) {
	next := make([]Root, 0, len(roots))
	for _, r := range roots {
		r.Path = filepath.Clean(r.Path)
		hints := map[string]string{}
		paths := make([]string, 0, len(r.Hints))
		for p := range r.Hints {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			v := r.Hints[p]
			if len(hints) < 512 && safeRel(p) {
				hints[p] = v
			}
		}
		r.Hints = hints
		next = append(next, r)
	}
	sort.Slice(next, func(i, j int) bool { return rootKey(next[i]) < rootKey(next[j]) })
	m.mu.Lock()
	active := make(map[string]bool, len(next))
	for _, r := range next {
		active[rootKey(r)] = true
	}
	for key, value := range m.results {
		if !active[key] && value.State == "ready" {
			value.State = "stale"
			m.results[key] = value
		}
	}
	m.roots = next
	m.busy = busy
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) input() ([]Root, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Root(nil), m.roots...), m.busy
}

// Values aggregates only the current path bindings. An old path can never
// contribute after detach/move. Empty known server sets are handled by the GUI.
func (m *Manager) Values() map[string]Value {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]Value{}
	missing := map[string]bool{}
	for _, r := range m.roots {
		v, ok := m.results[rootKey(r)]
		if !ok {
			v = Value{State: "waiting"}
		}
		sum, exists := out[r.Server]
		if !exists {
			sum = Value{State: "ready"}
		}
		if v.Known {
			sum.Bytes += v.Bytes
			sum.Known = true
		} else {
			missing[r.Server] = true
		}
		priority := map[string]int{"ready": 0, "stale": 1, "waiting": 2, "partial": 3}
		if priority[v.State] > priority[sum.State] {
			sum.State = v.State
		}
		if v.MeasuredAt != "" && (sum.MeasuredAt == "" || v.MeasuredAt < sum.MeasuredAt) {
			sum.MeasuredAt = v.MeasuredAt
		}
		out[r.Server] = sum
	}
	for server, sum := range out {
		if missing[server] && sum.Known {
			sum.State = "partial" // measured subtotal, never a complete total
			out[server] = sum
		}
	}
	return out
}

func (m *Manager) publish(key string, v Value, changed func()) {
	m.mu.Lock()
	m.results[key] = v
	m.mu.Unlock()
	if changed != nil {
		changed()
	}
}

func (m *Manager) wait(ctx context.Context, key string) error {
	for {
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		roots, busy := m.input()
		present := false
		for _, r := range roots {
			if rootKey(r) == key {
				present = true
				break
			}
		}
		if !present {
			return context.Canceled
		}
		if !busy {
			return nil
		}
		timer = time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func hintsChanged(a, b map[string]string) bool {
	for p, v := range a {
		if b[p] != v {
			return true
		}
	}
	return false
}

// Evict oldest dormant bindings only under cache pressure. A partial startup
// list must not evict useful records merely because they are not visible yet.
func (m *Manager) trimDormant(records map[string]record, roots []Root) bool {
	if len(records) <= maxCachedRoots {
		return false
	}
	active := make(map[string]bool, len(roots))
	for _, root := range roots {
		active[rootKey(root)] = true
	}
	var candidates []string
	for key := range records {
		if !active[key] {
			candidates = append(candidates, key)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := records[candidates[i]].Value.MeasuredAt, records[candidates[j]].Value.MeasuredAt
		if a == b {
			return candidates[i] < candidates[j]
		}
		return a < b
	})
	changed := false
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, key := range candidates {
		if len(records) <= maxCachedRoots {
			break
		}
		delete(records, key)
		delete(m.results, key)
		changed = true
	}
	return changed
}

// Run stops with the GUI context. A cached directory is listed again only
// after mtime/hint changes or a slow 30-minute audit (mtime alone misses writes
// into existing files, hydration and dehydration). No new daemon watcher.
func (m *Manager) Run(ctx context.Context, changed func()) {
	records := m.load()
	// Restore all bindings before starting any paced scan. A later projection
	// may add another root while the worker is paused inside a large first WC.
	// Values still filters by the exact current server/repo/path binding.
	for key, old := range records {
		if old.Value.Known {
			v := old.Value
			if v.State != "partial" {
				v.State = "stale"
			}
			m.publish(key, v, changed)
		}
	}
	attempts := map[string]time.Time{}
	dirtyCache := false
	defer func() {
		if dirtyCache {
			m.save(records)
		}
	}()
	lastSave := time.Time{}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		roots, busy := m.input()
		if m.trimDormant(records, roots) {
			dirtyCache = true
		}
		// Absence from a progressive GUI projection is not detach confirmation.
		// Keep dormant records; only Values decides which current bindings count.
		for i, r := range roots {
			if ctx.Err() != nil {
				return
			}
			key := rootKey(r)
			old := records[key]
			conflict := !validRoot(r.Path)
			for j, other := range roots {
				if i != j && overlaps(r.Path, other.Path) {
					conflict = true
				}
			}
			if conflict {
				m.publish(key, Value{State: "partial"}, changed)
				continue
			}
			now := time.Now()
			due := attempts[key].IsZero() || now.Sub(attempts[key]) >= 5*time.Minute || (now.Sub(attempts[key]) >= 30*time.Second && hintsChanged(r.Hints, old.Hints))
			if busy || !due {
				continue
			}
			attempts[key] = now
			scanner := scanner{probe: probe, wait: func(ctx context.Context) error { return m.wait(ctx, key) }}
			next, err := scanner.scan(ctx, r, old, now)
			attempts[key] = time.Now() // cool down after a long pass, not after its start
			if err != nil {
				continue
			}
			records[key] = next
			m.publish(key, next.Value, changed)
			dirtyCache = true
			if time.Since(lastSave) >= time.Minute {
				m.save(records)
				lastSave = time.Now()
				dirtyCache = false
			}
		}
		if dirtyCache && time.Since(lastSave) >= time.Minute {
			m.save(records)
			lastSave = time.Now()
			dirtyCache = false
		}
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		case <-ticker.C:
		}
	}
}

func (m *Manager) load() map[string]record {
	result := map[string]record{}
	f, err := os.Open(m.path)
	if err != nil {
		return result
	}
	defer f.Close()
	var cache diskCache
	if json.NewDecoder(io.LimitReader(f, 32<<20)).Decode(&cache) != nil || cache.Version != 1 {
		return result
	}
	for key, r := range cache.Records {
		if len(result) >= maxCachedRoots || len(r.Directories) > maxDirectories {
			break
		}
		if r.Value.Bytes < 0 {
			continue
		}
		valid := true
		for p, d := range r.Directories {
			if !safeRel(p) || d.Bytes < 0 {
				valid = false
				break
			}
			for _, child := range d.Children {
				if !safeRel(child) {
					valid = false
				}
			}
		}
		if valid {
			result[key] = r
		}
	}
	return result
}

func (m *Manager) save(records map[string]record) {
	if m.path == "" {
		return
	}
	raw, err := json.Marshal(diskCache{Version: 1, Records: records})
	if err != nil || len(raw) > 32<<20 {
		return
	}
	dir := filepath.Dir(m.path)
	if privatefile.EnsureDir(dir) != nil {
		return
	}
	f, err := os.CreateTemp(dir, ".allocation-*.tmp")
	if err != nil {
		return
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return
	}
	if f.Close() != nil || privatefile.Harden(name) != nil {
		return
	}
	// Disposable cache: no fsync and no retries competing with product writes.
	_ = os.Rename(name, m.path)
}
