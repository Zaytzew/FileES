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

	"filees/pkg/client"
	"filees/pkg/clientprofile"
	"filees/pkg/config"
	control "filees/pkg/control/v1"
	"filees/pkg/controlclient"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
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
		return client.New(client.Options{SvnPath: "svn", Timeout: 30 * time.Minute, LogScope: "svn:attachment:" + operationID, SSHIdentityFile: profile.IdentityFile, SSHKnownHosts: profile.KnownHosts, SSHPort: profile.SSHPort, SSHHostName: profile.Address})
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
		} else if record.State == localrepo.StateRelocating {
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
		if record.State == localrepo.StateRelocating {
			_, _ = p.local.FailRelocation(operationID, cause)
		} else {
			_, _ = p.local.MarkError(operationID, cause)
		}
		return
	}
	if record.State == localrepo.StateRelocating {
		p.runRelocate(ctx, record, profile)
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
	svn := client.New(client.Options{SvnPath: "svn", Timeout: 30 * time.Minute, LogScope: "svn:provisioning:" + operationID, SSHIdentityFile: profile.IdentityFile, SSHKnownHosts: profile.KnownHosts, SSHPort: profile.SSHPort, SSHHostName: profile.Address})
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
	check, err := provisioning.PreflightLocalPath(record.PendingLocalPath, provisioning.LocalPathAttach, p.otherRoots(record.OperationID))
	if err != nil {
		p.rollbackRelocation(ctx, record, profile, err)
		return
	}
	if err := p.quiesceAttachment(ctx, record); err != nil {
		p.rollbackRelocation(ctx, record, profile, err)
		return
	}
	svn := p.newAttachmentSVN(profile, record.OperationID)
	if _, err := svn.Checkout(ctx, record.RepoURL, check.CanonicalPath); err != nil {
		p.rollbackRelocation(ctx, record, profile, fmt.Errorf("checkout relocated repository: %w", err))
		return
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
	for _, entry := range entries {
		if entry.Item != "normal" && entry.Item != "none" && entry.Item != "external" {
			p.rollbackRelocation(ctx, record, profile, fmt.Errorf("relocated checkout is incomplete or modified at %s (%s)", entry.Path, entry.Item))
			return
		}
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
	if current.State == localrepo.StateDeleting {
		if err := p.deleteServerRepository(ctx, current, profile); err != nil {
			return current, err
		}
	}
	if err := stripWorkingCopyMetadata(current.LocalPath, current.DetachOperationID); err != nil {
		return current, err
	}
	return p.local.CompleteDetach(current.OperationID)
}

func (p *daemonProvisioner) deleteServerRepository(ctx context.Context, record localrepo.Record, profile clientprofile.Profile) error {
	transport, err := controlclient.New(controlclient.Config{
		Address: profile.Address, Port: profile.SSHPort, IdentityFile: profile.IdentityFile,
		KnownHosts: profile.KnownHosts, Timeout: 45 * time.Minute,
	})
	if err != nil {
		return err
	}
	requestID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(record.DetachOperationID+":delete-repository")).String()
	ticket, err := control.NewTicket(
		record.DetachOperationID, requestID, control.TicketDeleteRepository,
		profile.ClientID, control.DeleteRepositoryPayload{RepoID: record.RepoID}, time.Now(),
	)
	if err != nil {
		return err
	}
	result, err := transport.Exchange(ctx, ticket)
	if err != nil {
		return err
	}
	if result.Status != control.ResultOK {
		if result.Error == nil {
			return errors.New("server rejected repository deletion")
		}
		return fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message)
	}
	return nil
}

func stripWorkingCopyMetadata(root, operationID string) error {
	root = filepath.Clean(root)
	if _, err := uuid.Parse(operationID); err != nil {
		return errors.New("repository detach operation ID must be UUID")
	}
	info, err := os.Lstat(root)
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
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
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
	if info, statErr := os.Stat(filepath.Join(record.LocalPath, ".svn")); statErr == nil && info.IsDir() {
		mode = provisioning.LocalPathAttachResume
	}
	check, err := provisioning.PreflightLocalPath(record.LocalPath, mode, p.otherRoots(record.OperationID))
	if err != nil {
		p.failAttach(record.OperationID, err)
		return
	}
	svn := p.newAttachmentSVN(profile, record.OperationID)
	if _, err := svn.Checkout(ctx, record.RepoURL, check.CanonicalPath); err != nil {
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
		repo := config.Repo{ID: operation.RepoID, RepoURL: operation.RepoURL, LocalPath: operation.LocalPath, SSHIdentityFile: profile.IdentityFile, SSHKnownHosts: profile.KnownHosts, SSHHostName: profile.Address, SSHPort: profile.SSHPort, ServerID: profile.ServerID, ServerDisplayName: profile.DisplayName, ClientRole: "normal", Access: "rw"}
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
	repo := config.Repo{ID: record.RepoID, RepoURL: record.RepoURL, LocalPath: record.LocalPath, SSHIdentityFile: profile.IdentityFile, SSHKnownHosts: profile.KnownHosts, SSHHostName: profile.Address, SSHPort: profile.SSHPort, ServerID: profile.ServerID, ServerDisplayName: profile.DisplayName, ClientRole: "normal", Access: record.Access}
	select {
	case p.attachments <- provisionedAttachment{Repo: repo}:
	case <-ctx.Done():
	}
}
