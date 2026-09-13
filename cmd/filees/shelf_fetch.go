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
	"filees/pkg/portablepath"
	"filees/pkg/provisioning"
	"filees/pkg/talk"
)

func (service repositoryLifecycleService) BeginShelfFetch(serverID, repoID, repoURL, localPath string, item contract.ShelfItem) (contract.RepoLifecycleResult, error) {
	return service.beginShelfFetch(serverID, repoID, repoURL, localPath, item)
}

func (service repositoryLifecycleService) BeginShelfImport(serverID, repoID, repoURL, localPath string, item contract.ShelfItem, parent contract.RepoSummary, destination string) (contract.RepoLifecycleResult, error) {
	if !filepath.IsAbs(destination) || !filepath.IsAbs(parent.LocalPath) || !localrepo.ValidShelfPath(item.OriginalName) || strings.Contains(item.OriginalName, "/") {
		return contract.RepoLifecycleResult{}, errors.New("invalid placement path")
	}
	rel, err := filepath.Rel(parent.LocalPath, filepath.Join(destination, item.OriginalName))
	if err != nil || !localrepo.ValidShelfPath(filepath.ToSlash(rel)) {
		return contract.RepoLifecycleResult{}, errors.New("destination must be inside parent working copy")
	}
	if err := validateShelfDestination(parent.LocalPath, filepath.ToSlash(rel)); err != nil {
		return contract.RepoLifecycleResult{}, err
	}
	if _, err := os.Lstat(filepath.Join(parent.LocalPath, rel)); !errors.Is(err, os.ErrNotExist) {
		return contract.RepoLifecycleResult{}, errors.New("destination already exists or cannot be inspected")
	}
	return service.beginShelfFetch(serverID, repoID, repoURL, localPath, item, localrepo.ShelfPlacement{ParentRepoID: parent.ID, ParentRoot: parent.LocalPath, ParentURL: parent.URL, RelativePath: filepath.ToSlash(rel)})
}

func (service repositoryLifecycleService) beginShelfFetch(serverID, repoID, repoURL, localPath string, item contract.ShelfItem, placement ...localrepo.ShelfPlacement) (contract.RepoLifecycleResult, error) {
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
		if !filepath.IsAbs(localPath) || portablepath.SegmentProblem(filepath.Base(localPath)) != nil {
			return contract.RepoLifecycleResult{}, errors.New("invalid new shelf folder name")
		}
		// First use creates a new named shelf, never adopts an existing directory.
		if _, err := os.Lstat(localPath); err == nil {
			return contract.RepoLifecycleResult{}, os.ErrExist
		} else if !errors.Is(err, os.ErrNotExist) {
			return contract.RepoLifecycleResult{}, err
		}
		check, err := provisioning.PreflightLocalPath(localPath, provisioning.LocalPathAttach, service.allRoots())
		if err != nil {
			return contract.RepoLifecycleResult{}, err
		}
		var errAttach error
		// Mkdir (not MkdirAll) is the no-replace reservation. The parent is chosen
		// by the user; a racing creator is rejected rather than adopted.
		if err := os.Mkdir(check.CanonicalPath, 0700); err != nil {
			return contract.RepoLifecycleResult{}, err
		}
		record, errAttach = service.store.BeginShelfAttach(serverID, repoID, repoURL, check.CanonicalPath)
		if errAttach != nil {
			return contract.RepoLifecycleResult{}, errAttach
		}
	} else if record.RepoURL != repoURL || (localPath != "" && filepath.Clean(localPath) != record.LocalPath) {
		return contract.RepoLifecycleResult{}, errors.New("shelf attachment identity changed")
	}
	record, err := service.store.QueueShelfFetch(record.OperationID, item.UploadID, item.RepoPath, item.SHA256, item.Size, placement...)
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
		cleanupShelfStage(fetch)
	}
	complete := func() {
		if iconPath, err := shelfFolderIconPath(); err != nil {
			talk.With("shelf:"+record.OperationID).Warnf("prepare shelf icon: %v", err)
		} else if err := markManagedFolder(record.LocalPath, iconPath); err != nil {
			talk.With("shelf:"+record.OperationID).Warnf("decorate shelf: %v", err)
		}
		if _, err := p.local.SetShelfFetchState(record.OperationID, fetch.ID, "complete", nil); err == nil {
			cleanupShelfStage(fetch)
		}
	}
	// Reconcile a crash after local publication before fetching again. The
	// remote shelf may have changed meanwhile; our own linked receipt suffices.
	if fetch.Placement.ParentRepoID != "" {
		parent := fetch.Placement
		svn := p.newAttachmentSVN(profile, record.OperationID)
		if err := prepareLifecycleWC(ctx, svn, parent.ParentRoot, expectedWorkingCopyIdentity(record.ServerID, parent.ParentRepoID, parent.ParentURL)); err != nil {
			fail(err)
			return
		}
		if published, err := shelfPlacementPublished(fetch); err != nil {
			fail(err)
			return
		} else if published {
			complete()
			return
		}
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
		entryPath := filepath.Clean(entry.Path)
		if !filepath.IsAbs(entryPath) {
			entryPath = filepath.Join(record.LocalPath, entryPath)
		}
		if entry.Item == "unversioned" && entryPath == filepath.Join(record.LocalPath, "desktop.ini") {
			continue // Local Explorer decoration is never a shelf upload.
		}
		if entry.Item == "unversioned" && entryPath == filepath.Join(record.LocalPath, ".filees") {
			continue
		}
		if entry.Item != "normal" && entry.Item != "none" {
			fail(fmt.Errorf("local shelf changes must be resolved before download: %s (%s)", entry.Path, entry.Item))
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
	if fetch.Placement.ParentRepoID != "" {
		parent := fetch.Placement
		if err := prepareLifecycleWC(ctx, svn, parent.ParentRoot, expectedWorkingCopyIdentity(record.ServerID, parent.ParentRepoID, parent.ParentURL)); err != nil {
			fail(err)
			return
		}
		if err := placeShelfFile(target, fetch); err != nil {
			fail(err)
			return
		}
	}
	complete()
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
