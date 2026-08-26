// Package passport manages durable, renewable edit passports backed by
// authoritative repository lock tokens.
package passport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"filees/internal/durable"

	"github.com/google/uuid"
)

const CommentPrefix = "filees.edit-passport/v1 "

const (
	StateActive  = "active"
	StateLost    = "lost"
	StateExpired = "expired"
)

var (
	ErrHeldByOther  = errors.New("edit passport held by another owner")
	ErrNoPassport   = errors.New("no active edit passport")
	ErrPassportLost = errors.New("edit passport fencing token was lost")
)

// HeldByOther reports a refusal caused by somebody else's live passport, and
// keeps who and until when rather than formatting them into a sentence. That
// identity is exactly what a user needs in order to act - go ask that person -
// and it is known here, at the only place that inspects the holder's metadata.
// Callers that only care about the category keep working through
// errors.Is(err, ErrHeldByOther).
type HeldByOther struct {
	Path string
	// Holder is the SVN lock owner, i.e. the holding client's activation
	// identity - deliberately not the passport's InstanceUID, which is scoped
	// to one working copy and which the server's label resolver knows nothing
	// about. Instance keeps that per-working-copy UID for diagnostics only.
	// Both are opaque here; turning Holder into something a human recognises
	// is the presentation layer's job, since only it knows the aliases.
	Holder   string
	Instance string
	Realm    string
	Until    time.Time
}

func (e *HeldByOther) Error() string {
	if e.Until.IsZero() {
		return fmt.Sprintf("%s: %s", ErrHeldByOther, e.Path)
	}
	return fmt.Sprintf("%s: %s until %s", ErrHeldByOther, e.Path, e.Until.Format(time.RFC3339))
}

func (e *HeldByOther) Is(target error) bool { return target == ErrHeldByOther }
func (e *HeldByOther) Unwrap() error        { return ErrHeldByOther }

type Metadata struct {
	PassportID    string `json:"passport_id"`
	InstanceUID   string `json:"instance_uid"`
	PreviousToken string `json:"previous_token,omitempty"`
	// RealmID identifies the owning realm for autolock priority
	// (AUTOLOCK_CREATOR_OWNERSHIP_CONCEPT_V2.md). Empty for passports
	// acquired without realm awareness (manual borrow by a non-owner).
	RealmID       string    `json:"realm_id,omitempty"`
	IssuedAt      time.Time `json:"issued_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	HardExpiresAt time.Time `json:"hard_expires_at"`
}

type Passport struct {
	Path            string    `json:"path"`
	PassportID      string    `json:"passport_id"`
	InstanceUID     string    `json:"instance_uid"`
	RealmID         string    `json:"realm_id,omitempty"`
	FencingToken    string    `json:"fencing_token"`
	IssuedAt        time.Time `json:"issued_at"`
	LastHeartbeatAt time.Time `json:"last_heartbeat_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	HardExpiresAt   time.Time `json:"hard_expires_at"`
	CloseAfter      time.Time `json:"close_after,omitempty"`
	State           string    `json:"state"`
}

type Lock struct{ Token, Owner, Comment string }

// Backend is the authoritative lock boundary. force=true is allowed only
// after Manager has verified that the current FileES passport expired.
// Implementations must not call back into Manager methods: backend operations
// run under Manager's serialized operation boundary by design.
type Backend interface {
	Inspect(context.Context, string) (*Lock, error)
	Lock(context.Context, string, string, bool) (*Lock, string, error)
	Unlock(context.Context, string) (string, error)
}

type Config struct {
	TTL               time.Duration
	HeartbeatInterval time.Duration
	MaxSession        time.Duration
	CloseGrace        time.Duration
	Now               func() time.Time
	// WorkingCopy, when set by the daemon, makes passport persistence fail
	// closed after that SVN working copy is moved.
	WorkingCopy string
}

func (c Config) withDefaults() Config {
	if c.TTL <= 0 {
		c.TTL = 15 * time.Minute
	}
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = 5 * time.Minute
	}
	if c.MaxSession <= 0 {
		c.MaxSession = 24 * time.Hour
	}
	if c.CloseGrace <= 0 {
		c.CloseGrace = 5 * time.Minute
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

type Manager struct {
	opMu                   sync.Mutex
	mu                     sync.Mutex
	storePath, instanceUID string
	backend                Backend
	cfg                    Config
	passports              map[string]Passport
}

func Open(storePath, instanceUID string, backend Backend, cfg Config) (*Manager, error) {
	if strings.TrimSpace(storePath) == "" || strings.TrimSpace(instanceUID) == "" || backend == nil {
		return nil, errors.New("passport: store path, instance UID and backend are required")
	}
	cfg = cfg.withDefaults()
	if cfg.HeartbeatInterval >= cfg.TTL {
		return nil, errors.New("passport: heartbeat interval must be shorter than TTL")
	}
	if cfg.MaxSession < cfg.TTL {
		return nil, errors.New("passport: max session must be at least TTL")
	}
	m := &Manager{storePath: storePath, instanceUID: instanceUID, backend: backend, cfg: cfg, passports: map[string]Passport{}}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

// Acquire acquires an edit passport for each path. realmID, when non-empty,
// is the caller's own realm: it is stamped onto newly-issued passports for
// autolock priority, and - critically - it is the ONLY thing that allows a
// silent takeover of an unexpired lock. A lock is force-taken over without
// waiting for expiry only when its own metadata already carries the SAME
// non-empty realm (AUTOLOCK_CREATOR_OWNERSHIP_CONCEPT_V2.md §4: migration
// between one realm's own instances, never a steal from another realm).
// Passing an empty realmID preserves the original behaviour exactly: no
// silent takeover, only FileES's own already-expired locks are stolen.
func (m *Manager) Acquire(ctx context.Context, paths []string, realmID string) ([]Passport, string, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	paths = cleanPaths(paths)
	if len(paths) == 0 {
		return nil, "", errors.New("passport: no paths")
	}
	now := m.cfg.Now().UTC()
	var acquired, newlyAcquired []Passport
	var outputs []string
	for _, path := range paths {
		info, err := m.backend.Inspect(ctx, path)
		if err != nil {
			m.rollback(ctx, newlyAcquired)
			return nil, strings.Join(outputs, "\n"), err
		}
		force := false
		if info != nil {
			meta, ok := ParseComment(info.Comment)
			if !ok {
				m.rollback(ctx, newlyAcquired)
				// No parseable passport comment means a raw SVN lock, so the
				// holder is genuinely unknown: every client authenticates as
				// the same account, so the lock's owner field names nobody.
				return nil, strings.Join(outputs, "\n"), &HeldByOther{Path: path, Holder: info.Owner}
			}
			if local, ok := m.passports[path]; ok && local.State == StateActive && local.PassportID == meta.PassportID && local.FencingToken == info.Token && now.Before(meta.ExpiresAt) && now.Before(meta.HardExpiresAt) {
				acquired = append(acquired, local)
				continue
			}
			sameRealm := realmID != "" && meta.RealmID == realmID
			if !sameRealm && now.Before(meta.ExpiresAt) {
				m.rollback(ctx, newlyAcquired)
				return nil, strings.Join(outputs, "\n"), &HeldByOther{Path: path, Holder: info.Owner, Instance: meta.InstanceUID, Realm: meta.RealmID, Until: meta.ExpiresAt}
			}
			force = true
		}
		hard := now.Add(m.cfg.MaxSession)
		meta := Metadata{PassportID: uuid.NewString(), InstanceUID: m.instanceUID, RealmID: realmID, IssuedAt: now, ExpiresAt: minTime(now.Add(m.cfg.TTL), hard), HardExpiresAt: hard}
		if info != nil {
			meta.PreviousToken = info.Token
		}
		lock, out, err := m.backend.Lock(ctx, path, FormatComment(meta), force)
		outputs = append(outputs, out)
		if err != nil {
			m.rollback(ctx, newlyAcquired)
			return nil, strings.Join(outputs, "\n"), err
		}
		if lock == nil || lock.Token == "" {
			m.rollback(ctx, newlyAcquired)
			return nil, strings.Join(outputs, "\n"), errors.New("passport: backend returned no fencing token")
		}
		p := Passport{Path: path, PassportID: meta.PassportID, InstanceUID: meta.InstanceUID, RealmID: realmID, FencingToken: lock.Token, IssuedAt: now, LastHeartbeatAt: now, ExpiresAt: meta.ExpiresAt, HardExpiresAt: hard, State: StateActive}
		m.passports[path] = p
		acquired = append(acquired, p)
		newlyAcquired = append(newlyAcquired, p)
	}
	if err := m.saveLocked(); err != nil {
		m.rollback(ctx, newlyAcquired)
		return nil, strings.Join(outputs, "\n"), err
	}
	return acquired, strings.Join(outputs, "\n"), nil
}

func (m *Manager) Release(ctx context.Context, paths []string) (string, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	var outputs []string
	changed := false
	fail := func(cause error) (string, error) {
		if changed {
			cause = errors.Join(cause, m.saveLocked())
		}
		return strings.Join(outputs, "\n"), cause
	}
	for _, path := range cleanPaths(paths) {
		p, ok := m.passports[path]
		if !ok || p.State != StateActive {
			return fail(fmt.Errorf("%w: %s", ErrNoPassport, path))
		}
		info, err := m.backend.Inspect(ctx, path)
		if err != nil {
			return fail(err)
		}
		if !owns(p, info) {
			p.State = StateLost
			p.CloseAfter = time.Time{}
			m.passports[path] = p
			changed = true
			return fail(fmt.Errorf("%w: %s", ErrPassportLost, path))
		}
		out, err := m.backend.Unlock(ctx, path)
		outputs = append(outputs, out)
		if err != nil {
			return fail(err)
		}
		delete(m.passports, path)
		changed = true
	}
	return strings.Join(outputs, "\n"), m.saveLocked()
}

// Heartbeat renews due passports and performs safe close-grace releases.
func (m *Manager) Heartbeat(ctx context.Context) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.cfg.Now().UTC()
	var first error
	for path, p := range m.passports {
		if p.State != StateActive {
			continue
		}
		info, err := m.backend.Inspect(ctx, path)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if !owns(p, info) {
			p.State = StateLost
			p.CloseAfter = time.Time{}
			m.passports[path] = p
			if first == nil {
				first = fmt.Errorf("%w: %s", ErrPassportLost, path)
			}
			continue
		}
		if !p.CloseAfter.IsZero() && !now.Before(p.CloseAfter) {
			if _, err := m.backend.Unlock(ctx, path); err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			delete(m.passports, path)
			continue
		}
		if !now.Before(p.HardExpiresAt) {
			if _, err := m.backend.Unlock(ctx, path); err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			p.State = StateExpired
			p.CloseAfter = time.Time{}
			m.passports[path] = p
			continue
		}
		if now.Add(m.cfg.HeartbeatInterval).Before(p.ExpiresAt) {
			continue
		}
		expires := minTime(now.Add(m.cfg.TTL), p.HardExpiresAt)
		meta := Metadata{PassportID: p.PassportID, InstanceUID: p.InstanceUID, RealmID: p.RealmID, IssuedAt: p.IssuedAt, ExpiresAt: expires, HardExpiresAt: p.HardExpiresAt}
		meta.PreviousToken = p.FencingToken
		lock, _, err := m.backend.Lock(ctx, path, FormatComment(meta), true)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if lock == nil || lock.Token == "" {
			if first == nil {
				first = errors.New("passport: heartbeat returned no fencing token")
			}
			continue
		}
		p.FencingToken = lock.Token
		p.LastHeartbeatAt = now
		p.ExpiresAt = expires
		m.passports[path] = p
	}
	if err := m.saveLocked(); err != nil && first == nil {
		first = err
	}
	return first
}

func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = m.Heartbeat(ctx)
		}
	}
}

func (m *Manager) ReleaseAll(ctx context.Context) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	var first error
	for path, p := range m.passports {
		if p.State != StateActive {
			continue
		}
		info, err := m.backend.Inspect(ctx, path)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if !owns(p, info) {
			p.State = StateLost
			m.passports[path] = p
			continue
		}
		if _, err := m.backend.Unlock(ctx, path); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		delete(m.passports, path)
	}
	if err := m.saveLocked(); err != nil && first == nil {
		first = err
	}
	return first
}

// Authorize verifies the authoritative token immediately before publication.
// A local active record alone is never sufficient after a network partition.
func (m *Manager) Authorize(ctx context.Context, paths []string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.authorize(ctx, paths)
}

// BeginPublish verifies every fencing token and prevents heartbeat, acquire or
// release from mutating repository locks until the returned function is called.
// The caller must call the returned function after the publication attempt.
func (m *Manager) BeginPublish(ctx context.Context, paths []string) (func(), error) {
	m.opMu.Lock()
	if err := m.authorize(ctx, paths); err != nil {
		m.opMu.Unlock()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(m.opMu.Unlock) }, nil
}

func (m *Manager) authorize(ctx context.Context, paths []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.cfg.Now().UTC()
	for _, path := range cleanPaths(paths) {
		p, ok := m.passports[path]
		if !ok || p.State != StateActive || !now.Before(p.ExpiresAt) {
			return fmt.Errorf("%w: %s", ErrNoPassport, path)
		}
		info, err := m.backend.Inspect(ctx, path)
		if err != nil {
			return err
		}
		if !owns(p, info) {
			p.State = StateLost
			p.CloseAfter = time.Time{}
			m.passports[path] = p
			_ = m.saveLocked()
			return fmt.Errorf("%w: %s", ErrPassportLost, path)
		}
	}
	return nil
}

// AutoUnlockOwned walks wc for regular, currently read-only files (SVN's own
// svn:needs-lock behaviour after checkout/update) and makes each one locally
// writable, without acquiring the real SVN lock yet, when either nobody
// holds it or the current holder's metadata already carries the SAME
// realmID. A path held by anyone else - a foreign realm, or an
// unrecognized/non-FileES lock - is left untouched: the existing manual
// "Wypożycz do edycji" flow remains the only way to get RW on it.
//
// This is the local-RW half of AUTOLOCK_CREATOR_OWNERSHIP_CONCEPT_V2.md §3:
// the real Acquire is deferred to the actual publish attempt (BeginPublish),
// so between an `svn up` and the next commit a realm's own file is writable
// without yet being covered by a real server-side lock - a deliberate,
// accepted optimism, never a claim of exclusivity.
func (m *Manager) AutoUnlockOwned(ctx context.Context, wc, realmID string) error {
	if strings.TrimSpace(realmID) == "" {
		return nil
	}
	var first error
	walkErr := filepath.WalkDir(wc, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".svn" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Mode().Perm()&0o200 != 0 {
			return nil // already writable, or unreadable - nothing to do
		}
		lock, err := m.backend.Inspect(ctx, path)
		if err != nil {
			if first == nil {
				first = err
			}
			return nil
		}
		if lock != nil {
			meta, ok := ParseComment(lock.Comment)
			if !ok || meta.RealmID != realmID {
				return nil // foreign or unrecognized hold - never touched
			}
		}
		_ = os.Chmod(path, info.Mode().Perm()|0o200)
		return nil
	})
	if walkErr != nil && first == nil {
		first = walkErr
	}
	return first
}

// ForgetRemoved drops local state after a central commit confirmed that the
// repository path was removed. Subversion removes that path's server lock as
// part of the deletion, so issuing a later unlock would be both stale and noisy.
func (m *Manager) ForgetRemoved(paths []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	for _, path := range cleanPaths(paths) {
		if _, ok := m.passports[path]; ok {
			delete(m.passports, path)
			changed = true
		}
	}
	if changed {
		_ = m.saveLocked()
	}
}

// Touch cancels pending auto-release after any further filesystem activity.
func (m *Manager) Touch(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path = filepath.Clean(path)
	if p, ok := m.passports[path]; ok && p.State == StateActive && !p.CloseAfter.IsZero() {
		p.CloseAfter = time.Time{}
		m.passports[path] = p
		_ = m.saveLocked()
	}
}

// MarkPublished starts close-grace only after the central commit succeeded.
func (m *Manager) MarkPublished(paths []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	deadline := m.cfg.Now().UTC().Add(m.cfg.CloseGrace)
	changed := false
	for _, path := range cleanPaths(paths) {
		if p, ok := m.passports[path]; ok && p.State == StateActive {
			p.CloseAfter = deadline
			m.passports[path] = p
			changed = true
		}
	}
	if changed {
		_ = m.saveLocked()
	}
}

func (m *Manager) Snapshot() []Passport {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Passport, 0, len(m.passports))
	for _, p := range m.passports {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func FormatComment(meta Metadata) string {
	b, _ := json.Marshal(meta)
	return CommentPrefix + string(b)
}
func ParseComment(comment string) (Metadata, bool) {
	if !strings.HasPrefix(comment, CommentPrefix) {
		return Metadata{}, false
	}
	var meta Metadata
	if json.Unmarshal([]byte(strings.TrimPrefix(comment, CommentPrefix)), &meta) != nil || meta.PassportID == "" || meta.InstanceUID == "" || meta.ExpiresAt.IsZero() || meta.HardExpiresAt.IsZero() {
		return Metadata{}, false
	}
	return meta, true
}
func owns(p Passport, lock *Lock) bool {
	if lock == nil || lock.Token != p.FencingToken {
		return false
	}
	meta, ok := ParseComment(lock.Comment)
	return ok && meta.PassportID == p.PassportID && meta.InstanceUID == p.InstanceUID
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func cleanPaths(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		p = filepath.Clean(strings.TrimSpace(p))
		if p != "." && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
func (m *Manager) rollback(ctx context.Context, acquired []Passport) {
	for _, p := range acquired {
		_, _ = m.backend.Unlock(ctx, p.Path)
		delete(m.passports, p.Path)
	}
	_ = m.saveLocked()
}

func (m *Manager) load() error {
	b, err := os.ReadFile(m.storePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("passport store read: %w", err)
	}
	var list []Passport
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("passport store corrupt: %w", err)
	}
	for _, p := range list {
		if p.Path == "" || p.PassportID == "" || p.FencingToken == "" {
			return errors.New("passport store contains invalid entry")
		}
		m.passports[filepath.Clean(p.Path)] = p
	}
	return nil
}

func (m *Manager) saveLocked() error {
	dir := filepath.Dir(m.storePath)
	if m.cfg.WorkingCopy != "" {
		info, err := os.Stat(filepath.Join(m.cfg.WorkingCopy, ".svn"))
		if err != nil || !info.IsDir() {
			return errors.New("passport: working copy metadata is missing")
		}
		// The daemon creates the store directory before opening Manager. Do not
		// recreate it if the WC moves between the check above and CreateTemp.
		if _, err := os.Stat(dir); err != nil {
			return err
		}
	} else if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	list := make([]Passport, 0, len(m.passports))
	for _, p := range m.passports {
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(dir, ".passports-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, m.storePath); err != nil {
		return err
	}
	return durable.SyncDirectory(dir)
}
