package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filees/pkg/clientprofile"
	"filees/pkg/clientview"
	contract "filees/pkg/contract/v1"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
	"filees/pkg/talk"
)

func (service repositoryLifecycleService) BeginShelfFetch(serverID, repoID, repoURL, localPath string, item contract.ShelfItem) (contract.RepoLifecycleResult, error) {
	if service.onCreate == nil || !localrepo.ValidShelfPath(item.RepoPath) || len(item.SHA256) != 64 || item.Size < 0 || item.UploadID == "" {
		return contract.RepoLifecycleResult{}, errors.New("invalid shelf selection or unavailable executor")
	}
	var record localrepo.Record
	for _, candidate := range service.store.List() {
		if candidate.ServerID == serverID && candidate.RepoID == repoID && candidate.Purpose == clientview.PurposeUploadShelf && !candidate.RemoteDeletionObserved {
			record = candidate
			break
		}
	}
	if record.OperationID == "" {
		check, err := provisioning.PreflightLocalPath(localPath, provisioning.LocalPathAttach, service.allRoots())
		if err != nil {
			return contract.RepoLifecycleResult{}, err
		}
		var errAttach error
		record, errAttach = service.store.BeginShelfAttach(serverID, repoID, repoURL, check.CanonicalPath)
		if errAttach != nil {
			return contract.RepoLifecycleResult{}, errAttach
		}
	} else if record.RepoURL != repoURL || (localPath != "" && filepath.Clean(localPath) != record.LocalPath) {
		return contract.RepoLifecycleResult{}, errors.New("shelf attachment identity changed")
	}
	record, err := service.store.QueueShelfFetch(record.OperationID, item.UploadID, item.RepoPath, item.SHA256, item.Size)
	if err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	if service.onCreate != nil {
		service.onCreate(record.OperationID)
	}
	return lifecycleResult(record), nil
}

func (service repositoryLifecycleService) InspectShelf(serverID, repoID string) contract.RepoLifecycleResult {
	for _, record := range service.store.List() {
		if record.ServerID == serverID && record.RepoID == repoID && record.Purpose == clientview.PurposeUploadShelf && !record.RemoteDeletionObserved {
			return lifecycleResult(record)
		}
	}
	return contract.RepoLifecycleResult{ServerID: serverID, RepoID: repoID, State: "unattached"}
}

func (p *daemonProvisioner) runShelfFetch(ctx context.Context, record localrepo.Record, profile clientprofile.Profile) {
	fetch := record.ShelfFetch
	if _, err := p.local.SetShelfFetchState(record.OperationID, fetch.ID, "running", nil); err != nil {
		return
	}
	fail := func(err error) {
		_, _ = p.local.SetShelfFetchState(record.OperationID, fetch.ID, "failed", err)
		talk.With("shelf:"+record.OperationID).Warnf("selected download failed: %v", err)
	}
	if record.State == localrepo.StateAttaching {
		p.runAttach(ctx, record, profile)
		var ok bool
		record, ok = p.local.Get(record.OperationID)
		if !ok || record.State != localrepo.StateAttached {
			fail(errors.New("sparse shelf attachment failed"))
			return
		}
	}
	if record.State != localrepo.StateAttached || !localrepo.ValidShelfPath(fetch.RepoPath) {
		fail(errors.New("shelf is not attached"))
		return
	}
	target := filepath.Join(record.LocalPath, filepath.FromSlash(fetch.RepoPath))
	if err := shelfSafeTarget(record.LocalPath, fetch.RepoPath); err != nil {
		fail(err)
		return
	}
	svn := p.newAttachmentSVN(profile, record.OperationID)
	// Recheck WC identity after restart before allowing SVN to change its files.
	if err := prepareLifecycleWC(ctx, svn, record.LocalPath, expectedWorkingCopyIdentity(record.ServerID, record.RepoID, record.RepoURL)); err != nil {
		fail(err)
		return
	}
	entries, err := svn.Status(ctx, record.LocalPath, nil)
	if err != nil {
		fail(err)
		return
	}
	for _, entry := range entries {
		if entry.Item == "unversioned" && filepath.Clean(entry.Path) == filepath.Join(record.LocalPath, ".filees") {
			continue
		}
		if entry.Item != "normal" && entry.Item != "none" {
			fail(errors.New("local shelf changes must be resolved before download"))
			return
		}
	}
	fetcher, ok := svn.(interface {
		FetchSparsePath(context.Context, string, string) (string, error)
	})
	if !ok {
		fail(errors.New("SVN adapter does not support selected shelf download"))
		return
	}
	if _, err := fetcher.FetchSparsePath(ctx, record.LocalPath, target); err != nil {
		fail(err)
		return
	}
	if err := shelfSafeTarget(record.LocalPath, fetch.RepoPath); err != nil {
		fail(err)
		return
	}
	f, err := os.Open(target)
	if err != nil {
		fail(err)
		return
	}
	hash := sha256.New()
	n, err := io.Copy(hash, f)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil && (n != fetch.Size || hex.EncodeToString(hash.Sum(nil)) != fetch.SHA256) {
		err = errors.New("download does not match shelf receipt; local bytes preserved")
	}
	if err != nil {
		fail(err)
		return
	}
	_, _ = p.local.SetShelfFetchState(record.OperationID, fetch.ID, "complete", nil)
}

func shelfSafeTarget(root, relative string) error {
	current := root
	parts := append([]string{""}, strings.Split(relative, "/")...)
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !info.IsDir()) || (i == len(parts)-1 && !info.Mode().IsRegular()) {
			return fmt.Errorf("unsafe shelf path: %s", current)
		}
	}
	return nil
}
