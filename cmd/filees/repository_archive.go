package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"filees/pkg/clientprofile"
	control "filees/pkg/control/v1"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
	"github.com/google/uuid"
)

// The carrier is never attached to normal supervision. One durable creation
// owns transport, load and checkout; ordinary publication starts only after
// both journals record the final-generation boundary.
func (p *daemonProvisioner) finishArchiveCreation(ctx context.Context, op provisioning.Operation, profile clientprofile.Profile) error {
	return p.finishArchiveCreationWithLoad(ctx, op, profile, p.issueLoadRepositoryDump)
}

func (p *daemonProvisioner) finishArchiveCreationWithLoad(ctx context.Context, op provisioning.Operation, profile clientprofile.Profile, load func(context.Context, localrepo.Record, clientprofile.Profile) (control.LoadRepositoryDumpResult, error)) error {
	if op.Archive == nil || !op.ArchivePending() {
		return nil
	}
	if op.ClientID != profile.ClientID {
		return errors.New("archive creation belongs to another activation")
	}
	record, ok := p.local.Get(op.OperationID)
	if !ok || record.LocalPath != op.Archive.Target || record.RepoID != op.RepoID || record.RepoURL != op.RepoURL {
		return errors.New("archive creation no longer owns its target")
	}
	if op.Archive.LoadedUUID == "" {
		loadRecord := record
		loadRecord.ReconcileOperationID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(op.OperationID+":archive-load")).String()
		// Import history as archived; no implicit filtering of old project files.
		loadRecord.LoadDumpApplyIgnorePolicy = false
		loaded, err := load(ctx, loadRecord, profile)
		if err != nil {
			return fmt.Errorf("load selected archive: %w", err)
		}
		if loaded.RepoID != op.RepoID {
			return errors.New("loaded archive identifies another repository")
		}
		op, err = p.provisioning.MarkArchiveLoaded(op.OperationID, loaded.NewUUID)
		if err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if p.local.RemoteDeleted(record.ServerID, record.RepoID) {
		return errors.New("archive repository was deleted")
	}
	svn := p.newAttachmentSVN(profile, op.OperationID)
	if err := checkoutArchiveTarget(ctx, op, svn, p.otherRoots(record.OperationID)); err != nil {
		return err
	}
	if err := ensureWorkingCopyIdentity(record.LocalPath, expectedWorkingCopyIdentity(record.ServerID, record.RepoID, record.RepoURL)); err != nil {
		return err
	}
	_, err := p.provisioning.CompleteArchive(op.OperationID)
	return err
}

func checkoutArchiveTarget(ctx context.Context, op provisioning.Operation, svn attachmentSVN, otherRoots []string) error {
	root := op.Archive.Target
	mode := provisioning.LocalPathAttach
	if info, err := os.Stat(filepath.Join(root, ".svn")); err == nil && info.IsDir() {
		mode = provisioning.LocalPathAttachResume
	}
	check, err := provisioning.PreflightLocalPath(root, mode, otherRoots)
	if err != nil {
		return err
	}
	if check.CanonicalPath != root {
		return errors.New("archive target location changed")
	}
	verify := func() error {
		info, err := svn.GetInfo(ctx, root)
		if err != nil {
			return err
		}
		if !infoHasURL(info, op.RepoURL) || !infoHasUUID(info, op.Archive.LoadedUUID) {
			return errors.New("archive working copy has another repository generation")
		}
		entries, err := svn.Status(ctx, root, nil)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if isFileESControlPath(root, entry.Path) {
				continue
			}
			if (entry.Item != "normal" && entry.Item != "none") || (entry.Props != "" && entry.Props != "normal" && entry.Props != "none") {
				return errors.New("archive target contains local changes; original files preserved")
			}
		}
		return nil
	}
	if mode == provisioning.LocalPathAttachResume {
		// No re-checkout over changed data after a lost reply/restart.
		if err := verify(); err != nil {
			return err
		}
	}
	if _, err := svn.Checkout(ctx, op.RepoURL, root); err != nil {
		return fmt.Errorf("checkout imported archive: %w", err)
	}
	return verify()
}
