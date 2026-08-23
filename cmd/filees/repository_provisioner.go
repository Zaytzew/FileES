package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"filees/internal/durable"
	"filees/pkg/client"
	"filees/pkg/clientprofile"
	"filees/pkg/config"
	control "filees/pkg/control/v1"
	"filees/pkg/controlclient"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
	"filees/pkg/recoverykit"
	"filees/pkg/talk"
)

type daemonProvisioner struct {
	local            *localrepo.Store
	provisioning     *provisioning.Store
	mu               sync.RWMutex
	detachMu         sync.Mutex
	profiles         map[string]clientprofile.Profile
	queue            chan string
	attachments      chan<- provisionedAttachment
	newAttachmentSVN func(clientprofile.Profile, string) attachmentSVN
	recoveryRegistry recoverykit.Registry
}

type attachmentSVN interface {
	Checkout(context.Context, string, string) (string, error)
	GetInfo(context.Context, string) (string, error)
	Status(context.Context, string, []string) ([]client.StatusEntry, error)
}

type provisionedAttachment struct {
	Repo    config.Repo
	Quiesce bool
	Detach  bool
	Result  chan error
}

func newDaemonProvisioner(local *localrepo.Store, store *provisioning.Store, profiles []clientprofile.Profile) *daemonProvisioner {
	p := &daemonProvisioner{local: local, provisioning: store, profiles: make(map[string]clientprofile.Profile), queue: make(chan string, 32)}
	p.newAttachmentSVN = func(profile clientprofile.Profile, operationID string) attachmentSVN {
		return client.New(client.Options{SvnPath: "svn", Timeout: profile.SVNTimeout(), LogScope: "svn:attachment:" + operationID, SSHIdentityFile: profile.IdentityFile, SSHKnownHosts: profile.KnownHosts, SSHPort: profile.SSHPort, SSHHostName: profile.Address})
	}
	for _, profile := range profiles {
		p.profiles[profile.ServerID] = profile
	}
	return p
}

func (p *daemonProvisioner) AddProfile(profile clientprofile.Profile) {
	p.mu.Lock()
	p.profiles[profile.ServerID] = profile
	p.mu.Unlock()
}

func (p *daemonProvisioner) RemoveProfile(serverID string) {
	p.mu.Lock()
	delete(p.profiles, serverID)
	p.mu.Unlock()
}

func (p *daemonProvisioner) ClientID(serverID string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.profiles[serverID].ClientID
}

func (p *daemonProvisioner) Profile(serverID string) (clientprofile.Profile, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	profile, ok := p.profiles[serverID]
	return profile, ok
}

func (p *daemonProvisioner) Enqueue(operationID string) {
	select {
	case p.queue <- operationID:
	default:
		talk.With("provisioning").Warnf("queue full; operation %s remains durable for restart", operationID)
	}
}

func (p *daemonProvisioner) Run(ctx context.Context) {
	cleanupRetry := time.NewTicker(30 * time.Second)
	defer cleanupRetry.Stop()
	provisionedActive := make(map[string]struct{})
	if operations, err := p.provisioning.List(); err != nil {
		talk.With("provisioning").Errorf("restore operations: %v", err)
	} else {
		for _, operation := range operations {
			if err := p.reconcileLocalBoundary(operation); err != nil {
				talk.With("provisioning:"+operation.OperationID).Errorf("reconcile local lifecycle: %v", err)
				continue
			}
			if operation.State == provisioning.StateActive {
				if record, ok := p.local.Get(operation.OperationID); ok {
					switch record.State {
					case localrepo.StateRelocating, localrepo.StateReconciling, localrepo.StateDetaching, localrepo.StateDeleting, localrepo.StateDetached, localrepo.StateDeleted:
						// The provisioning journal describes how this repository was
						// created, while the local lifecycle contains a newer intent.
						// Let the second pass resume or honour that intent instead of
						// publishing and then suppressing the stale active attachment.
						continue
					}
				}
				provisionedActive[operation.OperationID] = struct{}{}
				p.publishAttachment(ctx, operation)
			} else {
				p.Enqueue(operation.OperationID)
			}
		}
	}
	for _, record := range p.local.List() {
		if _, published := provisionedActive[record.OperationID]; published {
			continue
		}
		if record.State == localrepo.StateAttaching {
			p.Enqueue(record.OperationID)
		} else if record.State == localrepo.StateRelocating || record.State == localrepo.StateReconciling {
			p.mu.RLock()
			profile, ok := p.profiles[record.ServerID]
			p.mu.RUnlock()
			if ok {
				p.publishLocalRecord(ctx, record, profile)
			}
			p.Enqueue(record.OperationID)
		} else if record.State == localrepo.StateDetaching || record.State == localrepo.StateDeleting {
			p.Enqueue(record.OperationID)
		} else if record.State == localrepo.StateAttached && record.RepoURL != "" {
			p.mu.RLock()
			profile, ok := p.profiles[record.ServerID]
			p.mu.RUnlock()
			if ok {
				p.publishLocalRecord(ctx, record, profile)
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-cleanupRetry.C:
			for _, record := range p.local.List() {
				if record.State == localrepo.StateDeleting && record.ServerDeleteCompleted && !record.LocalCleanupCompleted {
					p.retryLocalCleanup(ctx, record.OperationID)
				}
			}
		case operationID := <-p.queue:
			p.runOne(ctx, operationID)
		}
	}
}

func (p *daemonProvisioner) runOne(ctx context.Context, operationID string) {
	record, ok := p.local.Get(operationID)
	if !ok {
		talk.With("provisioning").Errorf("local lifecycle record %s is missing", operationID)
		return
	}
	p.mu.RLock()
	profile, ok := p.profiles[record.ServerID]
	p.mu.RUnlock()
	if record.State == localrepo.StateDetaching || record.State == localrepo.StateDeleting {
		if record.State == localrepo.StateDeleting && !ok {
			cause := errors.New("activated client profile is unavailable")
			_, _ = p.local.RecordDetachError(operationID, cause)
			return
		}
		if _, err := p.runDetach(ctx, record, profile); err != nil {
			_, _ = p.local.RecordDetachError(operationID, err)
			talk.With("detach:"+operationID).Warnf("detach failed: %v", err)
		}
		return
	}
	if !ok {
		cause := errors.New("activated client profile is unavailable")
		switch record.State {
		case localrepo.StateRelocating:
			_, _ = p.local.FailRelocation(operationID, cause)
		case localrepo.StateReconciling:
			_, _ = p.local.FailReconcile(operationID, cause)
		default:
			_, _ = p.local.MarkError(operationID, cause)
		}
		return
	}
	if record.State == localrepo.StateRelocating {
		p.runRelocate(ctx, record, profile)
		return
	}
	if record.State == localrepo.StateReconciling {
		p.runReconcile(ctx, record, profile)
		return
	}
	if record.State == localrepo.StateRepositoryCreated && record.LastError != "" {
		// The server repository is durable, but the last import attempt ended
		// with a concrete error. Do not replay it blindly at every daemon
		// startup: a user-initiated retry through BeginCreate resumes this exact
		// operation, while the idle client stays truthful instead of appearing
		// permanently busy.
		talk.With("provisioning:"+operationID).Warnf("creation requires explicit retry: %s", record.LastError)
		return
	}
	if record.State == localrepo.StateRepositoryCreated {
		if resumed, err := p.local.ResumeCreate(operationID); err == nil {
			record = resumed
		}
	}
	if record.RepoID != "" && record.State != localrepo.StateRepositoryCreated {
		p.runAttach(ctx, record, profile)
		return
	}
	controlTransport, err := controlclient.New(controlclient.Config{Address: profile.Address, Port: profile.SSHPort, IdentityFile: profile.IdentityFile, KnownHosts: profile.KnownHosts, Timeout: 30 * time.Second})
	if err != nil {
		_, _ = p.local.MarkError(operationID, err)
		return
	}
	svn := client.New(client.Options{SvnPath: "svn", Timeout: profile.SVNTimeout(), LogScope: "svn:provisioning:" + operationID, SSHIdentityFile: profile.IdentityFile, SSHKnownHosts: profile.KnownHosts, SSHPort: profile.SSHPort, SSHHostName: profile.Address})
	orchestrator := provisioning.Orchestrator{
		Store: p.provisioning, Control: controlTransport, SVN: svn,
		Limits: provisioning.ImportLimits{MaxBatchFiles: 100, MaxBatchBytes: 512 << 20},
		OnRepositoryReady: func(operation provisioning.Operation) error {
			_, err := p.local.MarkRepositoryCreated(operation.OperationID, operation.RepoID, operation.RepoURL)
			return err
		},
	}
	operation, err := orchestrator.RunCreate(ctx, operationID)
	if err != nil {
		if durable, getErr := p.provisioning.Get(operationID); getErr == nil && durable.RepoID != "" {
			if _, boundaryErr := p.local.MarkRepositoryCreated(operationID, durable.RepoID, durable.RepoURL); boundaryErr != nil {
				// Never downgrade this record to generic StateError: that state
				// deliberately releases its path for unrelated failed requests.
				// Keeping request_pending claims the path until startup
				// reconciliation can persist the server-created boundary.
				talk.With("provisioning:"+operationID).Errorf("persist repository-created boundary after failure: %v", boundaryErr)
				return
			}
		}
		_, _ = p.local.MarkError(operationID, err)
		talk.With("provisioning:"+operationID).Warnf("create failed: %v", err)
		return
	}
	if _, err := p.local.MarkAttached(operationID, operation.RepoID); err != nil {
		talk.With("provisioning:"+operationID).Errorf("publish local attachment: %v", err)
		return
	}
	p.publishAttachment(ctx, operation)
}

func (p *daemonProvisioner) reconcileLocalBoundary(operation provisioning.Operation) error {
	if operation.RepoID == "" {
		return nil
	}
	if record, ok := p.local.Get(operation.OperationID); ok && record.RepoID == operation.RepoID {
		switch record.State {
		case localrepo.StateRelocating, localrepo.StateReconciling, localrepo.StateDetaching, localrepo.StateDeleting, localrepo.StateDetached, localrepo.StateDeleted:
			// The provisioning journal proves the older create/initial-import
			// boundary. A later local lifecycle must never be rolled backward to
			// repository_created/attached during startup reconciliation.
			return nil
		}
	}
	if _, err := p.local.MarkRepositoryCreated(operation.OperationID, operation.RepoID, operation.RepoURL); err != nil {
		record, ok := p.local.Get(operation.OperationID)
		if !ok || record.State != localrepo.StateAttached || record.RepoID != operation.RepoID {
			return err
		}
	}
	if operation.State == provisioning.StateActive {
		if _, err := p.local.MarkAttached(operation.OperationID, operation.RepoID); err != nil {
			return err
		}
	}
	return nil
}

func (p *daemonProvisioner) runRelocate(ctx context.Context, record localrepo.Record, profile clientprofile.Profile) {
	mode := provisioning.LocalPathAttach
	if record.RelocationAdoptExisting {
		mode = provisioning.LocalPathAttachResume
	}
	check, err := provisioning.PreflightLocalPath(record.PendingLocalPath, mode, p.otherRoots(record.OperationID))
	if err != nil {
		p.rollbackRelocation(ctx, record, profile, err)
		return
	}
	if err := p.quiesceAttachment(ctx, record); err != nil {
		p.rollbackRelocation(ctx, record, profile, err)
		return
	}
	svn := p.newAttachmentSVN(profile, record.OperationID)
	if !record.RelocationAdoptExisting {
		if _, err := svn.Checkout(ctx, record.RepoURL, check.CanonicalPath); err != nil {
			p.rollbackRelocation(ctx, record, profile, fmt.Errorf("checkout relocated repository: %w", err))
			return
		}
	}
	info, err := svn.GetInfo(ctx, check.CanonicalPath)
	if err != nil || !infoHasURL(info, record.RepoURL) {
		if err == nil {
			err = errors.New("relocated working copy URL does not match projected repository")
		}
		p.rollbackRelocation(ctx, record, profile, err)
		return
	}
	entries, err := svn.Status(ctx, check.CanonicalPath, nil)
	if err != nil {
		p.rollbackRelocation(ctx, record, profile, err)
		return
	}
	if !record.RelocationAdoptExisting {
		for _, entry := range entries {
			if entry.Item != "normal" && entry.Item != "none" && entry.Item != "external" {
				p.rollbackRelocation(ctx, record, profile, fmt.Errorf("relocated checkout is incomplete or modified at %s (%s)", entry.Path, entry.Item))
				return
			}
		}
	}
	if err := ensureWorkingCopyIdentity(check.CanonicalPath, expectedWorkingCopyIdentity(record.ServerID, record.RepoID, record.RepoURL)); err != nil {
		p.rollbackRelocation(ctx, record, profile, fmt.Errorf("validate working-copy identity: %w", err))
		return
	}
	updated, err := p.local.CompleteRelocation(record.OperationID)
	if err != nil {
		p.rollbackRelocation(ctx, record, profile, err)
		return
	}
	p.publishLocalRecord(ctx, updated, profile)
}

func (p *daemonProvisioner) quiesceAttachment(ctx context.Context, record localrepo.Record) error {
	if p.attachments == nil {
		return errors.New("repository supervisor attachment channel is unavailable")
	}
	result := make(chan error, 1)
	repo := config.Repo{ID: record.RepoID, ServerID: record.ServerID}
	select {
	case p.attachments <- provisionedAttachment{Repo: repo, Quiesce: true, Detach: record.State == localrepo.StateDetaching || record.State == localrepo.StateDeleting, Result: result}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *daemonProvisioner) Detach(ctx context.Context, operationID string) (localrepo.Record, error) {
	record, ok := p.local.Get(operationID)
	if !ok {
		return localrepo.Record{}, os.ErrNotExist
	}
	profile, ok := p.Profile(record.ServerID)
	if record.State == localrepo.StateDeleting && !ok {
		return record, errors.New("activated client profile is unavailable")
	}
	return p.runDetach(ctx, record, profile)
}

func (p *daemonProvisioner) runDetach(ctx context.Context, record localrepo.Record, profile clientprofile.Profile) (localrepo.Record, error) {
	p.detachMu.Lock()
	defer p.detachMu.Unlock()

	current, ok := p.local.Get(record.OperationID)
	if !ok {
		return localrepo.Record{}, os.ErrNotExist
	}
	if current.State == localrepo.StateDetached || current.State == localrepo.StateDeleted {
		return current, nil
	}
	if current.State != localrepo.StateDetaching && current.State != localrepo.StateDeleting {
		return current, errors.New("repository detach is not in progress")
	}
	if err := p.quiesceAttachment(ctx, current); err != nil {
		return current, err
	}
	if current.State == localrepo.StateDeleting && !current.ServerDeleteCompleted {
		deleted, err := p.deleteServerRepository(ctx, current, profile)
		if err != nil {
			return current, err
		}
		current, err = p.local.MarkServerDeleted(current.OperationID, deleted.RetainUntil)
		if err != nil {
			return current, err
		}
	}
	if current.State == localrepo.StateDetaching {
		if err := stripWorkingCopyMetadataWithRetry(ctx, current.LocalPath, current.DetachOperationID); err != nil {
			return current, err
		}
		return p.local.CompleteDetach(current.OperationID)
	}

	// Server deletion, archive issuance and local WC cleanup are separate
	// durable boundaries. A server that has not yet learned the recovery
	// ticket must not prevent FileES from removing an already-deleted WC's
	// administrative metadata, and a locked wc.db must not replay either
	// server-side operation.
	var pending []error
	if !current.LocalCleanupCompleted {
		if err := stripWorkingCopyMetadataWithRetry(ctx, current.LocalPath, current.DetachOperationID); err != nil {
			pending = append(pending, err)
		} else {
			var err error
			current, err = p.local.MarkLocalCleanupCompleted(current.OperationID)
			if err != nil {
				pending = append(pending, err)
			}
		}
	}
	if !current.RecoveryPrepared {
		retainUntil, err := time.Parse(time.RFC3339Nano, current.RetainUntil)
		if err == nil && !time.Now().UTC().Before(retainUntil) {
			current, err = p.local.MarkRecoveryPrepared(current.OperationID, "")
			if err != nil {
				pending = append(pending, err)
			}
		} else {
			kitPath, prepareErr := p.prepareRepositoryRecovery(ctx, current, profile)
			if prepareErr != nil {
				pending = append(pending, fmt.Errorf("prepare repository archive download: %w", prepareErr))
			} else {
				current, err = p.local.MarkRecoveryPrepared(current.OperationID, kitPath)
				if err != nil {
					pending = append(pending, err)
				}
			}
		}
	}
	if current.RecoveryPrepared && current.LocalCleanupCompleted {
		completed, err := p.local.CompleteDetach(current.OperationID)
		if err != nil {
			pending = append(pending, err)
		} else {
			current = completed
		}
	}
	return current, errors.Join(pending...)
}

// retryLocalCleanup advances only the client-side deletion boundary. It is
// deliberately network-free: periodic retries must not spam an older worker
// with an unsupported recovery ticket or replay DELETE_REPOSITORY.
func (p *daemonProvisioner) retryLocalCleanup(ctx context.Context, operationID string) {
	p.detachMu.Lock()
	defer p.detachMu.Unlock()

	current, ok := p.local.Get(operationID)
	if !ok || current.State != localrepo.StateDeleting || !current.ServerDeleteCompleted || current.LocalCleanupCompleted {
		return
	}
	if err := stripWorkingCopyMetadataWithRetry(ctx, current.LocalPath, current.DetachOperationID); err != nil {
		_, _ = p.local.RecordDetachError(operationID, err)
		talk.With("detach:"+operationID).Warnf("local cleanup retry failed: %v", err)
		return
	}
	updated, err := p.local.MarkLocalCleanupCompleted(operationID)
	if err != nil {
		talk.With("detach:"+operationID).Warnf("persist local cleanup receipt: %v", err)
		return
	}
	if updated.RecoveryPrepared {
		if _, err := p.local.CompleteDetach(operationID); err != nil {
			talk.With("detach:"+operationID).Warnf("complete repository deletion: %v", err)
		}
	}
}

func (p *daemonProvisioner) deleteServerRepository(ctx context.Context, record localrepo.Record, profile clientprofile.Profile) (control.DeleteRepositoryResult, error) {
	transport, err := controlclient.New(controlclient.Config{
		Address: profile.Address, Port: profile.SSHPort, IdentityFile: profile.IdentityFile,
		KnownHosts: profile.KnownHosts, Timeout: 45 * time.Minute,
	})
	if err != nil {
		return control.DeleteRepositoryResult{}, err
	}
	requestID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(record.DetachOperationID+":delete-repository")).String()
	ticket, err := control.NewTicket(
		record.DetachOperationID, requestID, control.TicketDeleteRepository,
		profile.ClientID, control.DeleteRepositoryPayload{RepoID: record.RepoID}, time.Now(),
	)
	if err != nil {
		return control.DeleteRepositoryResult{}, err
	}
	result, err := transport.Exchange(ctx, ticket)
	if err != nil {
		return control.DeleteRepositoryResult{}, err
	}
	if result.Status != control.ResultOK {
		if result.Error == nil {
			return control.DeleteRepositoryResult{}, errors.New("server rejected repository deletion")
		}
		return control.DeleteRepositoryResult{}, fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message)
	}
	var deleted control.DeleteRepositoryResult
	if err := control.DecodeResultPayload(result.Result, &deleted); err != nil {
		return control.DeleteRepositoryResult{}, err
	}
	return deleted, nil
}

func (p *daemonProvisioner) prepareRepositoryRecovery(ctx context.Context, record localrepo.Record, profile clientprofile.Profile) (string, error) {
	knownHostRaw, err := os.ReadFile(profile.KnownHosts)
	if err != nil || len(knownHostRaw) > 16<<10 {
		return "", errors.New("pinned server host key cannot be read")
	}
	if !filepath.IsAbs(p.recoveryRegistry.Root) {
		return "", errors.New("repository recovery registry is unavailable")
	}
	kitPath := filepath.Join(p.recoveryRegistry.Root, "filees-recovery-"+record.ServerID+"-"+record.DetachOperationID+".fkr")
	draft, err := recoverykit.LoadDraft(kitPath)
	if errors.Is(err, os.ErrNotExist) {
		var publicKey string
		draft, publicKey, err = recoverykit.CreateUnboundDraft(recoveryProfileAddress(profile), strings.TrimSpace(string(knownHostRaw)), record.DetachOperationID)
		if err == nil {
			draft.PublicKey = strings.TrimSpace(publicKey)
			err = recoverykit.Store(kitPath, draft)
		}
	} else if err == nil && (draft.OperationID != record.DetachOperationID || draft.ServerAddress != recoveryProfileAddress(profile)) {
		err = errors.New("pending repository recovery kit does not match deletion operation")
	}
	if err != nil {
		return "", err
	}
	transport, err := realmRemovalTransport(profile)
	if err != nil {
		return "", err
	}
	requestID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(record.DetachOperationID+":prepare-repository-recovery")).String()
	ticket, err := control.NewTicket(record.DetachOperationID, requestID, control.TicketPrepareRepositoryRecovery, profile.ClientID, control.PrepareRepositoryRecoveryPayload{RepoID: record.RepoID, RecoveryPublicKey: strings.TrimSpace(draft.PublicKey)}, time.Now())
	if err != nil {
		return "", err
	}
	response, err := transport.Exchange(ctx, ticket)
	if err != nil {
		return "", err
	}
	if response.Status != control.ResultOK {
		return "", controlResultError(response)
	}
	var result control.PrepareRepositoryRecoveryResult
	if err := control.DecodeResultPayload(response.Result, &result); err != nil {
		return "", err
	}
	manifest := recoveryManifestFromControl(result.Manifest)
	if len(manifest.Archives) == 0 {
		_ = os.Remove(kitPath)
		return "", nil
	}
	finalKit, err := recoverykit.Finalize(draft, manifest)
	if err != nil {
		return "", err
	}
	if err := recoverykit.Store(kitPath, finalKit); err != nil {
		return "", err
	}
	if err := p.recoveryRegistry.Put(recoverykit.RegistryEntry{
		Schema: recoverykit.RegistrySchema, OperationID: record.DetachOperationID,
		ServerID: record.ServerID, ServerName: profile.DisplayName, KitPath: kitPath,
		ArchiveCount: len(manifest.Archives), DownloadUntil: manifest.DownloadUntil,
		AdminGraceUntil: manifest.AdminGraceUntil,
	}); err != nil {
		return "", err
	}
	return kitPath, nil
}

func stripWorkingCopyMetadataWithRetry(ctx context.Context, root, operationID string) error {
	var err error
	for _, delay := range []time.Duration{0, 250 * time.Millisecond, 750 * time.Millisecond} {
		if delay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		err = stripWorkingCopyMetadata(root, operationID)
		if err == nil {
			return nil
		}
	}
	detail := err.Error()
	lower := strings.ToLower(detail)
	if strings.Contains(lower, "used by another process") || strings.Contains(lower, "being used by another process") {
		detail += "; close Explorer/TortoiseSVN windows using this folder — FileES will retry automatically"
	}
	return fmt.Errorf("server repository deleted; local working-copy metadata cleanup is pending: %s", detail)
}

func stripWorkingCopyMetadata(root, operationID string) error {
	root = filepath.Clean(root)
	if _, err := uuid.Parse(operationID); err != nil {
		return errors.New("repository detach operation ID must be UUID")
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		// The user may have removed or moved the local folder while the daemon
		// was offline. There is no metadata left at the path FileES owns, so the
		// local detach boundary is already satisfied.
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !filepath.IsAbs(root) || root == string(filepath.Separator) {
		return errors.New("working copy root must be an absolute real directory")
	}
	entries := []string{".svn", ".filees"}
	// Validate the complete mutation set before removing either metadata tree.
	// A hostile replacement of .filees must not leave a half-detached WC where
	// .svn was already removed.
	for _, entry := range entries {
		source := filepath.Join(root, entry)
		sourceInfo, err := os.Lstat(source)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !sourceInfo.IsDir() || sourceInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is not a real metadata directory", source)
		}
	}
	for _, entry := range entries {
		source := filepath.Join(root, entry)
		sourceInfo, err := os.Lstat(source)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !sourceInfo.IsDir() || sourceInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is not a real metadata directory", source)
		}
		if err := os.RemoveAll(source); err != nil {
			return err
		}
	}
	return durable.SyncDirectory(root)
}

// runReconcile handles StateReconciling: issue LOAD_REPOSITORY_DUMP, then
// replace the WC at record.LocalPath in place. Unlike runRelocate the target
// path is the same as the source, so this cannot use "build elsewhere, leave
// the old one alone until the very end" the same way relocate does for free
// - it builds the replacement in a temp sibling directory, verifies it, then
// does the same rename-aside/rename-in/remove-aside swap that
// internal/svnrotate uses server-side (LOAD_REPOSITORY_DUMP_CONCEPT.md §7).
func (p *daemonProvisioner) runReconcile(ctx context.Context, record localrepo.Record, profile clientprofile.Profile) {
	if err := p.quiesceAttachment(ctx, record); err != nil {
		p.rollbackReconcile(ctx, record, profile, err)
		return
	}
	loaded, err := p.issueLoadRepositoryDump(ctx, record, profile)
	if err != nil {
		p.rollbackReconcile(ctx, record, profile, err)
		return
	}

	svn := p.newAttachmentSVN(profile, record.ReconcileOperationID)

	// A crash between a successful swap and CompleteReconcile resumes here
	// with the same ReconcileOperationID: the ticket exchange above already
	// returned the same cached LoadRepositoryDumpResult from the server
	// (operation_id-keyed), and if LocalPath's own UUID already matches
	// NewUUID, the swap already happened - finish the bookkeeping instead
	// of checking out and swapping a second time.
	if info, err := svn.GetInfo(ctx, record.LocalPath); err == nil && infoHasUUID(info, loaded.NewUUID) {
		p.completeReconcile(ctx, record, profile)
		return
	}

	tempNew := record.LocalPath + ".filees-reconcile-" + record.ReconcileOperationID + "-new"
	if err := os.RemoveAll(tempNew); err != nil { // clean slate for a resumed attempt
		p.rollbackReconcile(ctx, record, profile, err)
		return
	}
	if _, err := svn.Checkout(ctx, record.RepoURL, tempNew); err != nil {
		os.RemoveAll(tempNew)
		p.rollbackReconcile(ctx, record, profile, fmt.Errorf("checkout reconciled repository: %w", err))
		return
	}
	info, err := svn.GetInfo(ctx, tempNew)
	if err != nil || !infoHasURL(info, record.RepoURL) || !infoHasUUID(info, loaded.NewUUID) {
		if err == nil {
			err = errors.New("reconciled working copy does not match the projected repository")
		}
		os.RemoveAll(tempNew)
		p.rollbackReconcile(ctx, record, profile, err)
		return
	}
	entries, err := svn.Status(ctx, tempNew, nil)
	if err != nil {
		os.RemoveAll(tempNew)
		p.rollbackReconcile(ctx, record, profile, err)
		return
	}
	for _, entry := range entries {
		if entry.Item != "normal" && entry.Item != "none" && entry.Item != "external" {
			os.RemoveAll(tempNew)
			p.rollbackReconcile(ctx, record, profile, fmt.Errorf("reconciled checkout is incomplete or modified at %s (%s)", entry.Path, entry.Item))
			return
		}
	}
	if err := swapReconciledWorkingCopy(record.LocalPath, tempNew, record.ReconcileOperationID); err != nil {
		p.rollbackReconcile(ctx, record, profile, err)
		return
	}
	p.completeReconcile(ctx, record, profile)
}

func (p *daemonProvisioner) completeReconcile(ctx context.Context, record localrepo.Record, profile clientprofile.Profile) {
	updated, err := p.local.CompleteReconcile(record.OperationID)
	if err != nil {
		talk.With("reconcile:"+record.OperationID).Errorf("persist reconcile completion: %v", err)
		return
	}
	p.publishLocalRecord(ctx, updated, profile)
}

// issueLoadRepositoryDump sends the LOAD_REPOSITORY_DUMP ticket, keyed on
// ReconcileOperationID so a resumed attempt after a restart replays the same
// operation instead of asking the server to do it again
// (LOAD_REPOSITORY_DUMP_CONCEPT.md §8).
func (p *daemonProvisioner) issueLoadRepositoryDump(ctx context.Context, record localrepo.Record, profile clientprofile.Profile) (control.LoadRepositoryDumpResult, error) {
	transport, err := controlclient.New(controlclient.Config{
		Address: profile.Address, Port: profile.SSHPort, IdentityFile: profile.IdentityFile,
		KnownHosts: profile.KnownHosts, Timeout: 45 * time.Minute,
	})
	if err != nil {
		return control.LoadRepositoryDumpResult{}, err
	}
	requestID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(record.ReconcileOperationID+":load-repository-dump")).String()
	ticket, err := control.NewTicket(
		record.ReconcileOperationID, requestID, control.TicketLoadRepositoryDump,
		profile.ClientID, control.LoadRepositoryDumpPayload{
			RepoID:                   record.RepoID,
			ApplyCurrentIgnorePolicy: record.LoadDumpApplyIgnorePolicy,
			KeepLastRevisions:        record.LoadDumpKeepLastRevisions,
		}, time.Now(),
	)
	if err != nil {
		return control.LoadRepositoryDumpResult{}, err
	}
	result, err := transport.Exchange(ctx, ticket)
	if err != nil {
		return control.LoadRepositoryDumpResult{}, err
	}
	if result.Status != control.ResultOK {
		if result.Error == nil {
			return control.LoadRepositoryDumpResult{}, errors.New("server rejected repository dump load")
		}
		return control.LoadRepositoryDumpResult{}, fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message)
	}
	var loaded control.LoadRepositoryDumpResult
	if err := control.DecodeResultPayload(result.Result, &loaded); err != nil {
		return control.LoadRepositoryDumpResult{}, err
	}
	return loaded, nil
}

// swapReconciledWorkingCopy replaces root's content with newWC's in place,
// mirroring the build-elsewhere-then-swap discipline used everywhere else in
// FileES (ServerEffects.CreateFSFS, internal/svnrotate.LoadGeneration): move
// the stale content aside, install the new content, only then discard the
// old - so a failure of the second rename can still roll back to a working
// state instead of leaving root empty.
func swapReconciledWorkingCopy(root, newWC, operationID string) error {
	if !filepath.IsAbs(root) || !filepath.IsAbs(newWC) {
		return errors.New("reconcile swap paths must be absolute")
	}
	aside := root + ".filees-reconcile-" + operationID + "-old"
	if err := os.RemoveAll(aside); err != nil { // clean slate for a resumed attempt
		return err
	}
	if err := os.Rename(root, aside); err != nil {
		return fmt.Errorf("move stale working copy aside: %w", err)
	}
	if err := os.Rename(newWC, root); err != nil {
		if rerr := os.Rename(aside, root); rerr != nil {
			return fmt.Errorf("install reconciled working copy: %w (ROLLBACK FAILED, original working copy is at %s: %v)", err, aside, rerr)
		}
		return fmt.Errorf("install reconciled working copy: %w", err)
	}
	return os.RemoveAll(aside)
}

// infoHasUUID reports whether svn info output info carries "Repository
// UUID: want", mirroring infoHasURL's line-scan style.
func infoHasUUID(info, want string) bool {
	if want == "" {
		return false
	}
	for _, line := range strings.Split(strings.ReplaceAll(info, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(trimmed, "Repository UUID:"); ok && strings.TrimSpace(rest) == want {
			return true
		}
	}
	return false
}

// rollbackReconcile is only safe to call before swapReconciledWorkingCopy
// has run: up to that point record.LocalPath is untouched, so returning to
// StateAttached and republishing the existing attachment is exactly as safe
// as rollbackRelocation's equivalent call.
func (p *daemonProvisioner) rollbackReconcile(ctx context.Context, record localrepo.Record, profile clientprofile.Profile, cause error) {
	if _, err := p.local.FailReconcile(record.OperationID, cause); err != nil {
		talk.With("reconcile:"+record.OperationID).Errorf("persist rollback: %v", err)
	}
	p.publishLocalRecord(ctx, record, profile)
	talk.With("reconcile:"+record.OperationID).Warnf("reconcile failed; old working copy left in place: %v", cause)
}

func (p *daemonProvisioner) rollbackRelocation(ctx context.Context, record localrepo.Record, profile clientprofile.Profile, cause error) {
	if _, err := p.local.FailRelocation(record.OperationID, cause); err != nil {
		talk.With("relocation:"+record.OperationID).Errorf("persist rollback: %v", err)
	}
	p.publishLocalRecord(ctx, record, profile)
	talk.With("relocation:"+record.OperationID).Warnf("relocation failed; old runtime restored: %v", cause)
}

func (p *daemonProvisioner) runAttach(ctx context.Context, record localrepo.Record, profile clientprofile.Profile) {
	mode := provisioning.LocalPathAttach
	hadSVN := false
	if info, statErr := os.Stat(filepath.Join(record.LocalPath, ".svn")); statErr == nil && info.IsDir() {
		hadSVN = true
		mode = provisioning.LocalPathAttachResume
	}
	check, err := provisioning.PreflightLocalPath(record.LocalPath, mode, p.otherRoots(record.OperationID))
	if err != nil {
		p.failAttach(record.OperationID, err)
		return
	}
	svn := p.newAttachmentSVN(profile, record.OperationID)
	if _, err := svn.Checkout(ctx, record.RepoURL, check.CanonicalPath); err != nil {
		// A failed first checkout of an existing realm share leaves an
		// incomplete .svn admin area in an otherwise empty folder. Resume,
		// create, relocate and mobile pairing never take this path.
		if !hadSVN {
			discardFailedAttachMetadata(check.CanonicalPath)
		}
		p.failAttach(record.OperationID, fmt.Errorf("checkout shared repository: %w", err))
		return
	}
	info, err := svn.GetInfo(ctx, check.CanonicalPath)
	if err != nil || !infoHasURL(info, record.RepoURL) {
		if err == nil {
			err = errors.New("working copy URL does not match projected repository")
		}
		p.failAttach(record.OperationID, err)
		return
	}
	entries, err := svn.Status(ctx, check.CanonicalPath, nil)
	if err != nil {
		p.failAttach(record.OperationID, err)
		return
	}
	for _, entry := range entries {
		if entry.Item != "normal" && entry.Item != "none" && entry.Item != "external" {
			p.failAttach(record.OperationID, fmt.Errorf("checkout is incomplete or modified at %s (%s)", entry.Path, entry.Item))
			return
		}
	}
	if _, err := p.local.MarkAttached(record.OperationID, record.RepoID); err != nil {
		p.failAttach(record.OperationID, err)
		return
	}
	p.publishLocalRecord(ctx, record, profile)
}

func (p *daemonProvisioner) failAttach(operationID string, err error) {
	_, _ = p.local.MarkError(operationID, err)
	talk.With("attachment:"+operationID).Warnf("attachment failed: %v", err)
}

// discardFailedAttachMetadata removes only the .svn admin area left by a
// first checkout that failed while pairing a local folder onto an existing
// realm share. It never deletes user files and is never used on resume.
func discardFailedAttachMetadata(root string) {
	if root == "" {
		return
	}
	_ = os.RemoveAll(filepath.Join(root, ".svn"))
}

func (p *daemonProvisioner) otherRoots(operationID string) []string {
	var roots []string
	for _, record := range p.local.List() {
		if record.OperationID != operationID && record.State != localrepo.StateDetached && record.State != localrepo.StateDeleted && record.State != localrepo.StateError {
			roots = append(roots, record.LocalPath)
		}
	}
	return roots
}

func infoHasURL(info, want string) bool {
	for _, line := range strings.Split(strings.ReplaceAll(info, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(strings.TrimPrefix(line, "URL:")) == want && strings.HasPrefix(strings.TrimSpace(line), "URL:") {
			return true
		}
	}
	return false
}

func (p *daemonProvisioner) publishAttachment(ctx context.Context, operation provisioning.Operation) {
	if p.attachments == nil {
		return
	}
	record, ok := p.local.Get(operation.OperationID)
	if !ok || record.State != localrepo.StateAttached || record.RepoID != operation.RepoID {
		return
	}
	p.mu.RLock()
	profile, ok := p.profiles[record.ServerID]
	p.mu.RUnlock()
	if ok {
		// The lifecycle record is newer after locate/relocate. The provisioning
		// journal deliberately retains the path used when the repo was created.
		localPath := record.LocalPath
		if localPath == "" {
			localPath = operation.LocalPath
		}
		repo := config.Repo{ID: operation.RepoID, RepoURL: operation.RepoURL, LocalPath: localPath, SSHIdentityFile: profile.IdentityFile, SSHKnownHosts: profile.KnownHosts, SSHHostName: profile.Address, SSHPort: profile.SSHPort, SessionTimeout: profile.SVNTimeout(), ServerID: profile.ServerID, ServerDisplayName: profile.DisplayName, ClientRole: "normal", Access: "rw"}
		select {
		case p.attachments <- provisionedAttachment{Repo: repo}:
		case <-ctx.Done():
		}
	}
}

func (p *daemonProvisioner) publishLocalRecord(ctx context.Context, record localrepo.Record, profile clientprofile.Profile) {
	if p.attachments == nil {
		return
	}
	repo := config.Repo{ID: record.RepoID, RepoURL: record.RepoURL, LocalPath: record.LocalPath, SSHIdentityFile: profile.IdentityFile, SSHKnownHosts: profile.KnownHosts, SSHHostName: profile.Address, SSHPort: profile.SSHPort, SessionTimeout: profile.SVNTimeout(), ServerID: profile.ServerID, ServerDisplayName: profile.DisplayName, ClientRole: "normal", Access: record.Access}
	select {
	case p.attachments <- provisionedAttachment{Repo: repo}:
	case <-ctx.Done():
	}
}
