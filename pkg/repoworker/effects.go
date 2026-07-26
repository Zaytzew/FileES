package repoworker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type AuthorityPublisher interface {
	Publish(context.Context, string, string, string, string) error
	Delete(context.Context, string, string) error
}
type ServerEffects struct {
	SVNAdmin, RepositoriesRoot string
	DataAuthzFile              string
	DeletionArchiveRoot        string
	DeletionRetentionDays      int
	Authority                  AuthorityPublisher
	Now                        func() time.Time
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
	d, err := os.Open(e.RepositoriesRoot)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (e ServerEffects) PublishAuthority(ctx context.Context, repoID, realmID, name, url string) error {
	return e.Authority.Publish(ctx, repoID, realmID, name, url)
}
func (e ServerEffects) WithdrawAuthority(ctx context.Context, repoID, realmID string) error {
	if e.Authority == nil {
		return errors.New("authority publisher is required")
	}
	return e.Authority.Delete(ctx, repoID, realmID)
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
