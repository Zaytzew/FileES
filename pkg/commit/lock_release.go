package commit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filees/pkg/client"
	contract "filees/pkg/contract/v1"
	"filees/pkg/errcat"
	"filees/pkg/errmap"
	"github.com/google/uuid"
)

type lockReleaseClaim struct {
	Schema    string `json:"schema"`
	RepoURL   string `json:"repo_url"`
	ServerID  string `json:"server_id"`
	RequestID string `json:"request_id"`
	Path      string `json:"path"`
	Token     string `json:"observed_token"`
	State     string `json:"state"` // attempting, acquired, superseded, uncertain
}

// RunLockReleaseClaims consumes only explicit requester consent. The local
// poll does not contact the server unless an unprocessed accepted request is
// present. The normal commit/poll mutex, gates and admission cover each claim.
func (s *Service) RunLockReleaseClaims(ctx context.Context, repoID, wc string, source func() []contract.LockReleaseRequest, acquire func(context.Context, []string) (string, error)) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	next := map[string]time.Time{}
	reported := map[string]string{}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			for _, request := range source() {
				if request.Role != "requester" || request.State != "accepted" || request.RepoID != repoID || now.Before(next[request.RequestID]) {
					continue
				}
				done, err := s.ClaimReleasedLock(ctx, repoID, wc, request, acquire)
				if done {
					next[request.RequestID] = now.Add(time.Minute)
				} else {
					next[request.RequestID] = now.Add(10 * time.Second)
				}
				if err != nil && err.Error() != reported[request.RequestID] {
					reported[request.RequestID] = err.Error()
					s.ErrSink.Emit(errmap.Classify(err))
					s.Logger.Warnf("accepted reservation claim: %v", err)
				}
			}
		}
	}
}

// ClaimReleasedLock attempts an ordinary (never forced) acquisition at most
// once per consent. A durable attempt precedes acquisition. If the process
// dies or loses its reply, recovery observes the lock; it never reacquires a
// lock that may already have been acquired and subsequently released.
func (s *Service) ClaimReleasedLock(ctx context.Context, repoID, wc string, request contract.LockReleaseRequest, acquire func(context.Context, []string) (string, error)) (bool, error) {
	if request.Role != "requester" || request.State != "accepted" || request.RepoID != repoID {
		return true, nil
	}
	if parsed, err := uuid.Parse(request.RequestID); err != nil || parsed.String() != request.RequestID || request.ObservedLockID == "" || request.ServerID == "" {
		return false, errors.New("invalid accepted lock request")
	}
	rel := request.Path
	if !filepath.IsAbs(wc) || rel == "" || strings.ContainsAny(rel, "\\\x00\r\n") || filepath.IsAbs(rel) || filepath.ToSlash(filepath.Clean(rel)) != rel || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return false, errors.New("invalid accepted lock path")
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".filees" || part == ".svn" {
			return false, errors.New("accepted lock targets administrative data")
		}
	}
	release, err := s.Admission.EnterContext(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	s.wcOpMu.Lock()
	defer s.wcOpMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	file := filepath.Join(wc, ".filees", "state", "lock-release-"+request.RequestID+".json")
	wanted := lockReleaseClaim{Schema: "filees.lock-release-claim/v1", RepoURL: s.RepoURL, ServerID: request.ServerID, RequestID: request.RequestID, Path: rel, Token: request.ObservedLockID}
	current := wanted
	raw, err := os.ReadFile(file)
	if err == nil {
		if json.Unmarshal(raw, &current) != nil {
			return false, errors.New("invalid lock claim receipt")
		}
		state := current.State
		current.State = ""
		if current != wanted {
			return false, errors.New("lock claim receipt identity mismatch")
		}
		current.State = state
		switch state {
		case "acquired", "superseded", "uncertain":
			return true, nil
		case "attempting":
		default:
			return false, errors.New("unknown lock claim state")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	save := func(state string) error {
		current.State = state
		return atomicWriteJSONSliceInExistingDir(file, current)
	}
	if s.HostGate != nil {
		release, err := s.HostGate.Acquire(ctx)
		if err != nil {
			return false, err
		}
		defer release()
	}
	if s.RepoMtx != nil {
		unlock, err := s.RepoMtx.Lock(ctx, s.RepoURL)
		if err != nil {
			return false, err
		}
		defer unlock()
	}
	if HasUnresolvedCommit(wc) {
		return false, errors.New("publication recovery must finish before reservation claim")
	}
	observer, ok := s.Cli.(client.LockObservationReader)
	if !ok || acquire == nil {
		return false, errors.New("reservation claim requires fresh lock observation")
	}
	absolute := filepath.Join(wc, filepath.FromSlash(rel))
	// Refuse symlinks in any component before SVN updates a target.
	probe := wc
	for _, part := range strings.Split(rel, "/") {
		probe = filepath.Join(probe, part)
		st, err := os.Lstat(probe)
		if err != nil {
			return false, err
		}
		if st.Mode()&os.ModeSymlink != 0 || (probe == absolute && !st.Mode().IsRegular()) {
			return false, errors.New("reservation claim requires a regular file without symlinks")
		}
	}
	observation, err := observer.ReadLockObservation(ctx, wc, absolute)
	if err != nil {
		return false, err
	}
	owns := func(o client.LockObservation) bool {
		return o.Local != nil && o.Remote != nil && o.Local.Token != "" && o.Local.Token == o.Remote.Token
	}
	uncertain := func() (bool, error) {
		if err := save("uncertain"); err != nil {
			return false, err
		}
		return true, errcat.New(errcat.KeyLockOperation, map[string]string{"detail": "automatic reservation claim has an unconfirmed result; inspect the current reservation before borrowing manually"}, nil)
	}
	if current.State == "attempting" {
		if owns(observation) {
			return true, save("acquired")
		}
		return uncertain()
	}
	if observation.Remote != nil {
		if observation.Remote.Token == request.ObservedLockID {
			return false, nil
		} // holder has accepted but not unlocked yet
		return true, save("superseded") // never pursue a later lock instance
	}
	clean := func() error {
		entries, err := s.Cli.Status(ctx, wc, []string{absolute})
		if err != nil {
			return err
		}
		if len(entries) != 1 || entries[0].Item != "normal" || (entries[0].Props != "normal" && entries[0].Props != "none" && entries[0].Props != "") {
			return errors.New("local changes or conflict defer reservation claim")
		}
		return nil
	}
	if err := clean(); err != nil {
		return false, err
	}
	if _, err := s.Cli.UpdateDepthEmpty(ctx, wc, []string{absolute}); err != nil {
		return false, err
	}
	if err := clean(); err != nil {
		return false, err
	}
	observation, err = observer.ReadLockObservation(ctx, wc, absolute)
	if err != nil {
		return false, err
	}
	if observation.Remote != nil {
		return true, save("superseded")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := save("attempting"); err != nil {
		return false, err
	}
	if _, err := acquire(ctx, []string{absolute}); err != nil {
		return false, fmt.Errorf("reservation acquisition outcome requires observation: %w", err)
	}
	observation, err = observer.ReadLockObservation(ctx, wc, absolute)
	if err != nil {
		return false, err
	}
	if !owns(observation) {
		return uncertain()
	}
	return true, save("acquired")
}
