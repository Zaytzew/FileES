package repoworker

import (
	"context"
	"errors"
	"filees/internal/durable"
	"fmt"
	"github.com/google/uuid"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type AuthorityPublisher interface {
	Publish(context.Context, string, string, string, string, string) error
	Delete(context.Context, string, string) error
}

type DeleteAuthority interface {
	AuthorizeDelete(context.Context, string, string) error
}

type AbandonedAuthority interface {
	PruneAbandoned(context.Context, string, string) error
}
type ServerEffects struct {
	SVNAdmin, SVNLook, RepositoriesRoot string
	DataAuthzFile                       string
	DeletionArchiveRoot                 string
	DeletionRetentionDays               int
	Authority                           AuthorityPublisher
	Now                                 func() time.Time
}

func (e ServerEffects) CreateFSFS(ctx context.Context, repoID, operationID string) error {
	if !filepath.IsAbs(e.SVNAdmin) || !filepath.IsAbs(e.RepositoriesRoot) {
		return errors.New("svnadmin and repositories root must be absolute")
	}
	if e.Authority == nil {
		return errors.New("authority publisher is required")
	}
	if err := os.MkdirAll(e.RepositoriesRoot, 0700); err != nil {
		return err
	}
	final := filepath.Join(e.RepositoriesRoot, repoID)
	if validRepo(final) {
		return nil
	}
	stage := filepath.Join(e.RepositoriesRoot, ".creating-"+operationID)
	if !validRepo(stage) {
		if _, err := os.Stat(stage); err == nil {
			return errors.New("invalid repository staging path exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		out, err := exec.CommandContext(ctx, e.SVNAdmin, "create", stage).CombinedOutput()
		if err != nil {
			return fmt.Errorf("svnadmin create: %w: %s", err, string(out))
		}
	}
	conf := []byte("[general]\nanon-access = none\nauth-access = write\nauthz-db = " + e.DataAuthzFile + "\n")
	if !filepath.IsAbs(e.DataAuthzFile) {
		return errors.New("data authz path must be absolute")
	}
	if err := os.WriteFile(filepath.Join(stage, "conf", "svnserve.conf"), conf, 0600); err != nil {
		return err
	}
	if err := os.Rename(stage, final); err != nil {
		if validRepo(final) {
			return nil
		}
		return err
	}
	return syncDirectory(e.RepositoriesRoot)
}
func (e ServerEffects) PublishAuthority(ctx context.Context, repoID, realmID, name, url, purpose string) error {
	return e.Authority.Publish(ctx, repoID, realmID, name, url, purpose)
}
func (e ServerEffects) RollbackCreate(ctx context.Context, repoID, realmID string) error {
	if !filepath.IsAbs(e.RepositoriesRoot) || e.Authority == nil {
		return errors.New("repository rollback is incomplete")
	}
	if _, err := uuid.Parse(repoID); err != nil {
		return errors.New("repository rollback repo ID is invalid")
	}
	if err := e.Authority.Delete(ctx, repoID, realmID); err != nil {
		return fmt.Errorf("withdraw failed repository authority: %w", err)
	}
	path := filepath.Join(e.RepositoriesRoot, repoID)
	if rel, err := filepath.Rel(e.RepositoriesRoot, path); err != nil || rel != repoID {
		return errors.New("repository rollback path escapes root")
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return durable.SyncDirectory(e.RepositoriesRoot)
}

// PruneAbandonedCreate is the administrative counterpart of RollbackCreate
// for a creation whose authority publication succeeded but whose initial
// import never did.  The administrative scan identifies an r0 candidate;
// this final boundary blocks commits and verifies r0 again before authority
// is withdrawn, closing the race with a late initial-import worker.
func (e ServerEffects) PruneAbandonedCreate(ctx context.Context, repoID, realmID, operationID string) error {
	if !filepath.IsAbs(e.RepositoriesRoot) || e.Authority == nil {
		return errors.New("abandoned repository cleanup is incomplete")
	}
	if _, err := uuid.Parse(repoID); err != nil {
		return errors.New("abandoned repository repo ID is invalid")
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return errors.New("abandoned repository operation ID is invalid")
	}
	authority, ok := e.Authority.(AbandonedAuthority)
	if !ok {
		return errors.New("authority publisher cannot prune abandoned repositories")
	}
	path := filepath.Join(e.RepositoriesRoot, repoID)
	if rel, err := filepath.Rel(e.RepositoriesRoot, path); err != nil || rel != repoID {
		return errors.New("abandoned repository path escapes root")
	}
	if err := e.PrepareDelete(ctx, repoID, operationID); err != nil {
		return fmt.Errorf("block abandoned repository commits: %w", err)
	}
	if validRepo(path) {
		if !filepath.IsAbs(e.SVNLook) {
			return errors.New("svnlook path must be absolute for abandoned repository cleanup")
		}
		raw, err := exec.CommandContext(ctx, e.SVNLook, "youngest", path).CombinedOutput()
		if err != nil {
			return fmt.Errorf("verify abandoned repository revision: %w: %s", err, strings.TrimSpace(string(raw)))
		}
		revision, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		if err != nil || revision < 0 {
			return errors.New("svnlook returned invalid youngest revision during abandoned cleanup")
		}
		if revision != 0 {
			verifyErr := fmt.Errorf("abandoned repository advanced to r%d; refusing cleanup", revision)
			if restoreErr := e.RestoreDelete(ctx, repoID, operationID); restoreErr != nil {
				return errors.Join(verifyErr, fmt.Errorf("restore repository commit hook: %w", restoreErr))
			}
			return verifyErr
		}
	}
	if err := authority.PruneAbandoned(ctx, repoID, realmID); err != nil {
		return fmt.Errorf("withdraw abandoned repository authority: %w", err)
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return durable.SyncDirectory(e.RepositoriesRoot)
}
func (e ServerEffects) WithdrawAuthority(ctx context.Context, repoID, realmID string) error {
	if e.Authority == nil {
		return errors.New("authority publisher is required")
	}
	return e.Authority.Delete(ctx, repoID, realmID)
}

func (e ServerEffects) AuthorizeDelete(ctx context.Context, repoID, realmID string) error {
	authority, ok := e.Authority.(DeleteAuthority)
	if !ok {
		return errors.New("authority publisher cannot authorize repository deletion")
	}
	return authority.AuthorizeDelete(ctx, repoID, realmID)
}
func (e ServerEffects) Activate(ctx context.Context, repoID, realmID string) error {
	activator, ok := e.Authority.(interface {
		Activate(context.Context, string, string) error
	})
	if !ok {
		return errors.New("authority publisher cannot activate repositories")
	}
	return activator.Activate(ctx, repoID, realmID)
}
func validRepo(path string) bool {
	for _, p := range []string{"format", "db"} {
		if info, err := os.Stat(filepath.Join(path, p)); err != nil || (p == "format" && !info.Mode().IsRegular()) || (p == "db" && !info.IsDir()) {
			return false
		}
	}
	return true
}
