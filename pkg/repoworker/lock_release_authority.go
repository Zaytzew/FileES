package repoworker

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"filees/pkg/passport"
	"github.com/google/uuid"
)

type SVNAdminLockAuthority struct {
	SVNAdmin         string
	RepositoriesRoot string
	Run              func(context.Context, string, ...string) ([]byte, error)
}

func (a SVNAdminLockAuthority) InspectLock(ctx context.Context, repoID, relativePath string) (*LockReleaseObservation, error) {
	if !filepath.IsAbs(a.SVNAdmin) || !filepath.IsAbs(a.RepositoriesRoot) {
		return nil, errors.New("lock authority paths must be absolute")
	}
	if _, err := uuid.Parse(repoID); err != nil {
		return nil, errors.New("lock authority repository ID must be UUID")
	}
	if err := validateLockReleasePath(relativePath); err != nil {
		return nil, err
	}
	repositoryPath := filepath.Join(filepath.Clean(a.RepositoriesRoot), repoID)
	if rel, err := filepath.Rel(filepath.Clean(a.RepositoriesRoot), repositoryPath); err != nil || rel != repoID {
		return nil, errors.New("lock authority repository path escapes root")
	}
	run := a.Run
	if run == nil {
		run = runLockAuthorityCommand
	}
	out, err := run(ctx, a.SVNAdmin, "lslocks", repositoryPath, "/"+relativePath)
	if err != nil {
		return nil, fmt.Errorf("inspect repository lock: %w", err)
	}
	entry, err := parseSVNAdminLock(out)
	if err != nil || entry == nil {
		return nil, err
	}
	if entry.Path != "/"+relativePath {
		return nil, errors.New("lock authority returned a different repository path")
	}
	if _, err := uuid.Parse(entry.Owner); err != nil {
		return nil, errors.New("lock authority returned a non-client owner")
	}
	realmID := ""
	if metadata, ok := passport.ParseComment(entry.Comment); ok {
		realmID = metadata.RealmID
		if realmID != "" {
			if _, err := uuid.Parse(realmID); err != nil {
				return nil, errors.New("lock authority returned invalid passport realm")
			}
		}
	}
	return &LockReleaseObservation{ObservedLockID: entry.Token, HolderClientID: entry.Owner, HolderRealmID: realmID}, nil
}

func runLockAuthorityCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

type svnAdminLock struct {
	Path, Token, Owner, Comment string
}

func parseSVNAdminLock(raw []byte) (*svnAdminLock, error) {
	text := strings.TrimSpace(strings.ReplaceAll(string(raw), "\r\n", "\n"))
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	entry := &svnAdminLock{}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "Path: "):
			if entry.Path != "" {
				return nil, errors.New("svnadmin returned more than one lock")
			}
			entry.Path = strings.TrimSpace(strings.TrimPrefix(line, "Path: "))
		case strings.HasPrefix(line, "UUID Token: "):
			entry.Token = strings.TrimSpace(strings.TrimPrefix(line, "UUID Token: "))
		case strings.HasPrefix(line, "Owner: "):
			entry.Owner = strings.TrimSpace(strings.TrimPrefix(line, "Owner: "))
		case strings.HasPrefix(line, "Comment ("):
			count, err := parseSVNAdminCommentLineCount(line)
			if err != nil {
				return nil, err
			}
			if i+count >= len(lines) {
				return nil, errors.New("svnadmin lock comment is truncated")
			}
			entry.Comment = strings.Join(lines[i+1:i+1+count], "\n")
			i += count
		}
	}
	if entry.Path == "" || entry.Token == "" || entry.Owner == "" {
		return nil, errors.New("svnadmin returned an incomplete lock")
	}
	if err := validateObservedLockID(entry.Token); err != nil {
		return nil, err
	}
	return entry, nil
}

func parseSVNAdminCommentLineCount(line string) (int, error) {
	prefix := "Comment ("
	if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, "):") {
		return 0, errors.New("svnadmin lock comment header is invalid")
	}
	inside := strings.TrimSuffix(strings.TrimPrefix(line, prefix), "):")
	fields := strings.Fields(inside)
	if len(fields) != 2 || (fields[1] != "line" && fields[1] != "lines") {
		return 0, errors.New("svnadmin lock comment header is invalid")
	}
	count, err := strconv.Atoi(fields[0])
	if err != nil || count < 0 || count > 1024 {
		return 0, errors.New("svnadmin lock comment line count is invalid")
	}
	return count, nil
}
