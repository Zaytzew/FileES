package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	control "filees/pkg/control/v1"
	"filees/pkg/errcat"
	"filees/pkg/passport"
	"github.com/google/uuid"
)

// PassportExecutions shares one durable gate between control-v1 and pre-lock.
// Caller of Handle/Reap holds worker/service-WC locks. The hook holds only the
// per-acquisition lock; it never calls control or touches canonical authority.
type PassportExecutions struct {
	Authority       PassportReplacementAuthority
	GuardExecutable string
}

type passportExecution struct {
	Schema         string    `json:"schema"`
	RepoID         string    `json:"repo_id"`
	RepositoryUUID string    `json:"repository_uuid"`
	ClientID       string    `json:"client_id"`
	RealmID        string    `json:"realm_id"`
	Path           string    `json:"path"`
	Comment        string    `json:"comment"`
	State          string    `json:"state"`        // armed, started, closing, closed
	ExecutorPID    int       `json:"executor_pid"` // kernel parent, never client input
	ObservedToken  string    `json:"observed_token,omitempty"`
	ExpiresAt      time.Time `json:"expires_at"`
}

const executionSchema = "filees.passport-execution/v1"

// The installed v1 force guard does not provide an execution fence.
func checkExecutionGuardExecutable(executable string) error {
	if !filepath.IsAbs(executable) {
		return errors.New("absolute execution guard required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, executable, "--lock-guard-version").Output()
	if err != nil || string(out) != "filees.lock-force-guard/v2\n" {
		return errors.New("execution guard v2 is not installed")
	}
	return nil
}

func executionDirectory(repository string) string {
	return filepath.Join(filepath.Dir(repository), ".filees-passport-executions", filepath.Base(repository))
}

// The UUID file is read-only FSFS identity, not a writable shortcut into wc.db
// or lock internals. FSFS's optional second line is the instance identifier.
func executionRepositoryUUID(repository string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(repository, "db", "uuid"))
	if err != nil {
		return "", err
	}
	id := strings.SplitN(string(raw), "\n", 2)[0]
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return "", errors.New("invalid FSFS UUID")
	}
	return id, nil
}

func executionMetadata(comment string) (passport.Metadata, error) {
	m, ok := passport.ParseComment(comment)
	if !ok || passport.FormatComment(m) != comment || len(comment) > 4096 {
		return m, errors.New("invalid acquisition metadata")
	}
	ids := []string{m.AcquisitionID, m.PassportID, m.InstanceUID}
	// Manual Acquire deliberately omits the realm comment. Its authority is
	// the authenticated session, never a comment-based ownership fallback.
	if m.RealmID != "" {
		ids = append(ids, m.RealmID)
	}
	for _, id := range ids {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed.String() != id {
			return m, errors.New("invalid acquisition identity")
		}
	}
	if m.IssuedAt.IsZero() || !m.ExpiresAt.After(m.IssuedAt) || m.HardExpiresAt.Before(m.ExpiresAt) || m.HardExpiresAt.Sub(m.IssuedAt) > 24*time.Hour {
		return m, errors.New("invalid acquisition lifetime")
	}
	return m, nil
}

func withExecution(repository, id string, fn func(string) error) error {
	if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id {
		return errors.New("invalid acquisition ID")
	}
	dir := executionDirectory(repository)
	// Both levels are durable, private state outside the replaceable FSFS.
	for _, d := range []string{filepath.Dir(dir), dir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			return err
		}
		info, err := os.Lstat(d)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return errors.New("unsafe acquisition directory")
		}
		if err := syncDirectory(filepath.Dir(d)); err != nil {
			return err
		}
	}
	for _, suffix := range []string{".lock", ".json"} {
		if err := privateExecutionFile(filepath.Join(dir, id+suffix)); err != nil {
			return err
		}
	}
	return WithFileLock(filepath.Join(dir, id+".lock"), func() error { return fn(filepath.Join(dir, id+".json")) })
}

func privateExecutionFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("unsafe acquisition file")
	}
	return nil
}

func readExecution(file string) (passportExecution, error) {
	var r passportExecution
	if err := privateExecutionFile(file); err != nil {
		return r, err
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return r, err
	}
	if len(raw) > 16384 {
		return r, errors.New("oversized acquisition record")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return r, err
	}
	if value, ok := fields["executor_pid"]; !ok || string(value) == "null" {
		return r, errors.New("missing acquisition executor field")
	}
	if err = json.Unmarshal(raw, &r); err != nil {
		return r, err
	}
	m, err := executionMetadata(r.Comment)
	if err != nil || r.Schema != executionSchema || (m.RealmID != "" && r.RealmID != m.RealmID) || !r.ExpiresAt.Equal(m.ExpiresAt) {
		return r, errors.New("invalid acquisition record")
	}
	if r.State != "armed" && r.State != "started" && r.State != "closing" && r.State != "closed" {
		return r, errors.New("unknown acquisition state")
	}
	if r.ExecutorPID < 0 || r.ExecutorPID == 1 || (r.State == "started" && r.ExecutorPID <= 1) || (r.State == "armed" && r.ExecutorPID != 0) || (r.ExecutorPID == 0 && r.ObservedToken != "") {
		return r, errors.New("invalid acquisition executor")
	}
	for _, id := range []string{r.ClientID, r.RealmID, r.RepoID, r.RepositoryUUID} {
		if _, err := uuid.Parse(id); err != nil {
			return r, err
		}
	}
	if err := validateLockReleasePath(r.Path); err != nil {
		return r, err
	}
	if r.ObservedToken != "" {
		if err := validateObservedLockID(r.ObservedToken); err != nil {
			return r, err
		}
	}
	return r, nil
}

func (s PassportExecutions) now() time.Time {
	if s.Authority.Now != nil {
		return s.Authority.Now().UTC()
	}
	return time.Now().UTC()
}

// Handle never interprets a client-supplied clock or PID as completion proof.
func (s PassportExecutions) Handle(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	if err := session.Validate(); err != nil {
		return control.Result{}, err
	}
	if err := ticket.Validate(); err != nil {
		return control.Result{}, err
	}
	if ticket.ClientID != session.ClientID {
		return control.Result{}, errors.New("acquisition actor mismatch")
	}
	if ticket.Type != control.TicketArmPassportAcquisition && ticket.Type != control.TicketSettlePassportAcquisition && ticket.Type != control.TicketExpirePassportPath {
		return control.Result{}, errors.New("invalid acquisition request type")
	}
	var p control.PassportExecutionPayload
	if err := control.DecodePayload(ticket.Payload, &p); err != nil {
		return control.Result{}, err
	}
	failure := func(key errcat.Key) (control.Result, error) { return preparationError(ticket, key, s.now()) }
	if ticket.Type != control.TicketSettlePassportAcquisition {
		if err := s.Authority.authorizeRequester(session, p.RepoID); err != nil {
			return failure(errcat.KeyPassportDenied)
		}
	} else {
		// A revoked grant does not prohibit closing this actor's OWN attempt.
		// The forced-command still requires activation and realm admission.
		if err := s.Authority.authorizePassportCleanup(session); err != nil {
			return failure(errcat.KeyPassportDenied)
		}
	}
	repo, err := s.Authority.Locks.repositoryPath(p.RepoID, p.Path)
	if err != nil {
		return control.Result{}, err
	}
	if ticket.Type == control.TicketExpirePassportPath {
		if err := s.ExpirePath(ctx, p.RepoID, p.Path); err != nil {
			return control.Result{}, err
		}
		return control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.PassportExecutionResult{RepoID: p.RepoID, Path: p.Path, State: "checked"}, s.now())
	}
	meta, err := executionMetadata(p.Comment)
	if err != nil || (meta.RealmID != "" && meta.RealmID != session.RealmID) {
		return failure(errcat.KeyPassportDenied)
	}
	if ticket.Type == control.TicketArmPassportAcquisition {
		// Link presence alone is insufficient: reject an old executable too.
		states, err := InspectLockGuards(repo, s.GuardExecutable)
		if err != nil {
			return failure(errcat.KeyPassportUnavailable)
		}
		for _, state := range states {
			if state.State != "installed" {
				return failure(errcat.KeyPassportUnavailable)
			}
		}
		if err := checkExecutionGuardExecutable(s.GuardExecutable); err != nil {
			return failure(errcat.KeyPassportUnavailable)
		}
	}
	generation, err := executionRepositoryUUID(repo)
	if err != nil {
		return control.Result{}, err
	}
	state := "armed"
	err = withExecution(repo, meta.AcquisitionID, func(file string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		record, err := readExecution(file)
		if errors.Is(err, os.ErrNotExist) {
			record = passportExecution{Schema: executionSchema, RepoID: p.RepoID, RepositoryUUID: generation, ClientID: session.ClientID, RealmID: session.RealmID, Path: p.Path, Comment: p.Comment, State: "armed", ExpiresAt: meta.ExpiresAt}
			if ticket.Type == control.TicketSettlePassportAcquisition {
				record.State = "closed"
			}
			// A server-rejected expired arm is also a permanent tombstone.
			if !s.now().Before(meta.ExpiresAt) {
				record.State = "closed"
			}
			if ticket.Type == control.TicketArmPassportAcquisition && (meta.IssuedAt.After(s.now().Add(time.Minute)) || meta.ExpiresAt.After(s.now().Add(24*time.Hour))) {
				return ErrPassportReplacementDenied
			}
			if err = atomicJSON(file, record); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if record.RepoID != p.RepoID || record.ClientID != session.ClientID || record.RealmID != session.RealmID || record.Path != p.Path || record.Comment != p.Comment {
			return ErrPassportReplacementDenied
		}
		if ticket.Type == control.TicketArmPassportAcquisition {
			if record.State == "armed" && !s.now().Before(record.ExpiresAt) {
				record.State = "closed"
				if err := atomicJSON(file, record); err != nil {
					return err
				}
			}
			if record.RepositoryUUID != generation || record.State != "armed" || !s.now().Before(record.ExpiresAt) {
				return ErrPassportReplacementStale
			}
			return nil
		}
		state = "closed"
		return s.closeExecution(ctx, repo, file, &record, generation)
	})
	if errors.Is(err, ErrPassportReplacementDenied) {
		return failure(errcat.KeyPassportDenied)
	}
	if errors.Is(err, ErrPassportReplacementStale) {
		return failure(errcat.KeyPassportAborted)
	}
	if errors.Is(err, errExecutionRunning) {
		return failure(errcat.KeyPassportUncertain)
	}
	if err != nil {
		return control.Result{}, err
	}
	return control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.PassportExecutionResult{RepoID: p.RepoID, Path: p.Path, Comment: p.Comment, State: state}, s.now())
}

var errExecutionRunning = errors.New("acquisition executor has not finished")

func (s PassportExecutions) closeExecution(ctx context.Context, repo, file string, r *passportExecution, generation string) error {
	if r.State == "closed" {
		return nil
	}
	r.State = "closing"
	if err := atomicJSON(file, r); err != nil {
		return err
	} // fence all future hook admission
	if r.RepositoryUUID != generation {
		// A pathname may have been swapped while the old executor was alive.
		// Do not assume that an open FSFS handle pins every subsequent IO.
		if r.ExecutorPID > 1 {
			dead, err := passportExecutorDead(r.ExecutorPID)
			if err != nil {
				return err
			}
			if !dead {
				return errExecutionRunning
			}
		}
		// Never release any token in a different generation.
		r.State = "closed"
		return atomicJSON(file, r)
	}
	lock, err := s.Authority.Locks.inspectSVNLock(ctx, r.RepoID, r.Path)
	if err != nil {
		return err
	}
	matches := lock != nil && lock.Owner == r.ClientID && lock.Comment == r.Comment
	if r.ObservedToken == "" && matches && r.ExecutorPID > 1 {
		// Exactly one mutation was admitted. Seeing its token proves it happened;
		// its parent may still be delivering the response but cannot acquire twice.
		r.ObservedToken = lock.Token
		if err := atomicJSON(file, r); err != nil {
			return err
		}
	}
	if r.ObservedToken == "" && r.ExecutorPID > 1 {
		dead, err := passportExecutorDead(r.ExecutorPID)
		if err != nil {
			return err
		}
		if !dead {
			return errExecutionRunning
		}
	}
	if matches && r.ObservedToken != "" && r.ObservedToken == lock.Token {
		if err := s.Authority.Locks.unlockIfCurrent(ctx, r.RepoID, r.Path, r.ClientID, r.ObservedToken); err != nil {
			return err
		}
	}
	r.State = "closed"
	return atomicJSON(file, r)
}

// AdmitPassportExecution is called only by the direct pre-lock worker image.
// Ordinary (non-passport and legacy) locks retain their previous semantics.
func AdmitPassportExecution(args []string, parent int, now time.Time) error {
	if len(args) != 5 || args[4] != "0" {
		return errors.New("force lock is forbidden")
	}
	meta, ok := passport.ParseComment(args[3])
	if !ok || meta.AcquisitionID == "" {
		return nil
	}
	meta, err := executionMetadata(args[3])
	if err != nil {
		return err
	}
	if parent <= 1 || !filepath.IsAbs(args[0]) || !strings.HasPrefix(args[1], "/") {
		return errors.New("invalid SVN executor")
	}
	repo := filepath.Clean(args[0])
	if _, err := uuid.Parse(filepath.Base(repo)); err != nil {
		return err
	}
	generation, err := executionRepositoryUUID(repo)
	if err != nil {
		return err
	}
	return withExecution(repo, meta.AcquisitionID, func(file string) error {
		r, err := readExecution(file)
		if err != nil {
			return err
		}
		if r.State != "armed" || r.RepositoryUUID != generation || r.ClientID != args[2] || r.Path != strings.TrimPrefix(args[1], "/") || r.Comment != args[3] || r.RepoID != filepath.Base(repo) {
			return errors.New("acquisition is not armed for this SVN request")
		}
		if !now.Before(r.ExpiresAt) {
			r.State = "closed"
			if err := atomicJSON(file, r); err != nil {
				return err
			}
			return errors.New("acquisition expired")
		}
		r.State, r.ExecutorPID = "started", parent
		return atomicJSON(file, r)
	})
}

// ExpirePath also covers an existing legacy passport, never an ordinary lock.
// Expiration uses only the server clock; unlock remains exact-token conditional.
func (s PassportExecutions) ExpirePath(ctx context.Context, repoID, path string) error {
	lock, err := s.Authority.Locks.inspectSVNLock(ctx, repoID, path)
	if err != nil || lock == nil {
		return err
	}
	meta, ok := passport.ParseComment(lock.Comment)
	if !ok || meta.IssuedAt.IsZero() || !meta.ExpiresAt.After(meta.IssuedAt) || meta.HardExpiresAt.Before(meta.ExpiresAt) {
		return nil
	}
	for _, id := range []string{meta.PassportID, meta.InstanceUID} {
		if _, err := uuid.Parse(id); err != nil {
			return nil
		}
	}
	if meta.RealmID != "" {
		if _, err := uuid.Parse(meta.RealmID); err != nil {
			return nil
		}
	}
	if s.now().Before(meta.ExpiresAt) && s.now().Before(meta.HardExpiresAt) {
		return nil
	}
	if meta.AcquisitionID == "" {
		return s.Authority.Locks.unlockIfCurrent(ctx, repoID, path, lock.Owner, lock.Token)
	}
	repo, err := s.Authority.Locks.repositoryPath(repoID, path)
	if err != nil {
		return err
	}
	generation, err := executionRepositoryUUID(repo)
	if err != nil {
		return err
	}
	return withExecution(repo, meta.AcquisitionID, func(file string) error {
		r, err := readExecution(file)
		if err != nil {
			return err
		}
		if r.ClientID != lock.Owner || r.Path != path || r.Comment != lock.Comment || r.RepoID != repoID {
			return errors.New("expiry acquisition mismatch")
		}
		if s.now().Before(r.ExpiresAt) {
			return nil
		}
		return s.closeExecution(ctx, repo, file, &r, generation)
	})
}

// Reap scans journaled attempts without requiring a connected client.
// It is an explicit maintenance operation, never an unbounded startup scan.
func (s PassportExecutions) Reap(ctx context.Context) error {
	root := filepath.Join(s.Authority.Locks.RepositoriesRoot, ".filees-passport-executions")
	dirs, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var failures []error
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if !dir.IsDir() {
			continue
		}
		if err := s.ReapRepository(ctx, dir.Name()); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", dir.Name(), err))
		}
	}
	return errors.Join(failures...)
}

// ReapRepository retains closed records and lock files: deleting either may
// reopen an old acquisition or split the lock domain. Busy executors and corrupt
// records are reported, but do not prevent cleanup of independent attempts.
func (s PassportExecutions) ReapRepository(ctx context.Context, repoID string) error {
	repo, err := s.Authority.Locks.repositoryPath(repoID, "validation")
	if err != nil {
		return err
	}
	files, err := os.ReadDir(executionDirectory(repo))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range files {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		err := withExecution(repo, id, func(file string) error {
			r, err := readExecution(file)
			if err != nil {
				return err
			}
			meta, _ := executionMetadata(r.Comment)
			if r.RepoID != repoID || meta.AcquisitionID != id {
				return errors.New("acquisition filename/scope mismatch")
			}
			if r.State == "closed" || (r.State != "closing" && s.now().Before(r.ExpiresAt)) {
				return nil
			}
			generation, err := executionRepositoryUUID(repo)
			if err != nil {
				// Only an absent entire repository counts as a retired generation.
				// A damaged uuid file in an existing repository remains an error.
				if _, statErr := os.Lstat(repo); !errors.Is(statErr, os.ErrNotExist) {
					return err
				}
				generation = ""
			}
			return s.closeExecution(ctx, repo, file, &r, generation)
		})
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", id, err))
		}
	}
	return errors.Join(failures...)
}
