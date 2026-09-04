// Package ipcserver implements the FileES daemon-side IPC contract server.
// It listens on a Unix domain socket, speaks filees.contract/v1 over JSON Lines,
// and is the single point of contact between the daemon engine and all clients.
package ipcserver

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"filees/pkg/activity"
	contract "filees/pkg/contract/v1"
	"filees/pkg/realmbranding"
	"filees/pkg/talk"
)

// Server is the IPC contract server. Create with New, register repos with
// RegisterRepo, then call Start. Safe for concurrent use.
type Server struct {
	sockPath  string
	startTime time.Time
	lg        talk.Logger

	mu                   sync.RWMutex
	repos                map[string]*RepoState // keyed by repo ID
	activations          map[string]contract.ActivationStatus
	activation           ActivationService
	realmAlias           RealmAliasService
	realmGrants          RealmGrantService
	realmBranding        RealmPublicBrandingService
	editingPolicy        EditingPolicyService
	publicShares         PublicShareService
	publicShareAggregate PublicShareSource
	uploadChannels       UploadChannelService
	ownerLabels          OwnerLabelResolver
	lockReleases         LockReleaseService
	lockReleaseRequests  map[string][]contract.LockReleaseRequest
	lifecycle            RepositoryLifecycleService
	mobilePair           MobilePairingService
	serverDetach         ServerDetachService
	sessionTimeout       SessionTimeoutService
	realmRemoval         RealmRemovalService
	updates              UpdateService
	whales               WhaleService
	activity             ActivitySource
	detachments          DetachmentSource
	lifecycleFn          SystemLifecycleService

	connsMu  sync.Mutex
	conns    map[net.Conn]struct{}
	stopping bool

	evSeq int64 // atomic monotone counter for Event.Sequence

	subsMu sync.Mutex
	subs   map[chan contract.Event]struct{}
}

type ActivationService interface {
	Begin(context.Context, contract.ActivationBeginPayload) (contract.ActivationCommandResult, error)
	Finish(context.Context, contract.ActivationFinishPayload) (contract.ActivationCommandResult, error)
	Pending(context.Context, contract.ActivationPendingPayload) (contract.ActivationPendingResult, error)
	Resume(context.Context, contract.ActivationResumePayload) (contract.ActivationCommandResult, error)
}

type RealmAliasService interface {
	Claim(context.Context, string, string) (string, error)
}

type RealmGrantService interface {
	ListRecipients(context.Context, string, string) ([]contract.RealmGrantRecipient, error)
	SetVisibility(context.Context, string, string) (string, error)
	Grant(context.Context, string, string, string, string) (contract.RealmGrantResult, error)
	Revoke(context.Context, string, string, string) (contract.RealmGrantResult, error)
}

type RealmPublicBrandingService interface {
	GetPublicBranding(context.Context, string) (realmbranding.Branding, error)
	SetPublicBranding(context.Context, string, realmbranding.Branding) (realmbranding.Branding, error)
}

// EditingPolicyService is owner-only at the far end: the daemon forwards the
// request and the server decides, since only it knows who owns what.
type EditingPolicyService interface {
	SetEditingPolicy(ctx context.Context, serverID, repoID, policy string) (string, error)
}

type PublicShareService interface {
	ListPublicShares(context.Context, string, string) ([]contract.PublicShareSummary, error)
	CreatePublicShare(context.Context, string, contract.PublicShareDeclaration) (contract.PublicShareResult, error)
	UpdatePublicShare(context.Context, string, string, contract.PublicShareDeclaration, bool) (contract.PublicShareResult, error)
	RevokePublicShare(context.Context, string, string) (contract.PublicShareResult, error)
	DeletePublicShare(context.Context, string, string) (contract.PublicShareResult, error)
}

type UploadChannelService interface {
	ListUploadChannels(context.Context, string, string) (contract.UploadChannelListResult, error)
	CreateUploadChannel(context.Context, string, contract.UploadChannelDeclaration) (contract.UploadChannelResult, error)
	UpdateUploadChannel(context.Context, string, string, contract.UploadChannelDeclaration) (contract.UploadChannelResult, error)
	RevokeUploadChannel(context.Context, string, string) (contract.UploadChannelResult, error)
	DeleteUploadChannel(context.Context, string, string) (contract.UploadChannelResult, error)
	ListQuarantine(context.Context, string) (contract.QuarantineListResult, error)
	HideQuarantine(context.Context, string, string) (contract.QuarantineHideResult, error)
	FetchQuarantine(context.Context, string, string) (contract.QuarantineFetchResult, error)
}

// OwnerLabelResolver converts opaque SVN client IDs to server-owned display
// aliases. It is deliberately absent from GUI-facing contracts.
type OwnerLabelResolver interface {
	Resolve(context.Context, string, []string) (map[string]string, error)
}

// LockReleaseService crosses the authenticated server control plane. The
// projected records themselves are cached by Server from clientview, so list
// rendering remains available while the remote worker is temporarily offline.
type LockReleaseService interface {
	Request(context.Context, contract.LockReleaseRequestPayload) (contract.LockReleaseRequest, error)
	Dismiss(context.Context, contract.LockReleaseDecisionPayload) (contract.LockReleaseRequest, error)
	Accept(context.Context, contract.LockReleaseDecisionPayload) (contract.LockReleaseRequest, error)
}

type RepositoryLifecycleService interface {
	BeginCreate(serverID, displayName, localPath string) (contract.RepoLifecycleResult, error)
	BeginAttach(serverID, repoID, localPath string, required bool) (contract.RepoLifecycleResult, error)
	ApproveAttach(operationID, serverID, repoID, repoURL, access string) (contract.RepoLifecycleResult, error)
	BeginRelocate(serverID, repoID, newLocalPath string) (contract.RepoLifecycleResult, error)
	BeginLocate(serverID, repoID, existingLocalPath string) (contract.RepoLifecycleResult, error)
	BeginLoadDump(serverID, repoID string, applyIgnorePolicy bool, keepLastRevisions *int) (contract.RepoLifecycleResult, error)
	BeginDetach(context.Context, string, string, bool) (contract.RepoLifecycleResult, error)
	Status(operationID string) (contract.RepoLifecycleResult, error)
	Repair(context.Context, string, string, string, string) (contract.RepoLifecycleResult, error)
}

type SystemLifecycleService interface {
	Restart()
	Shutdown()
}

// MobilePairingService mints a mobile pairing token for the given
// activated server profile, through the daemon's own control-plane
// channel (mirrors RepositoryLifecycleService's role for repo-create).
type MobilePairingService interface {
	Begin(ctx context.Context, serverID string) (contract.MobilePairingBeginResult, error)
}

type ServerDetachService interface {
	Detach(context.Context, string) error
}

type SessionTimeoutService interface {
	SetSessionTimeout(context.Context, string, int) (int, error)
}

type RealmRemovalService interface {
	Begin(context.Context, string, string, contract.RealmRemoveBeginPayload) (contract.RealmRemoveBeginResult, error)
	Confirm(context.Context, contract.RealmRemoveConfirmPayload) (contract.RealmRemoveConfirmResult, error)
	List(context.Context) ([]contract.RecoveryStatus, error)
	Download(context.Context, contract.RecoveryDownloadPayload) (contract.RecoveryDownloadResult, error)
}

type UpdateService interface {
	Status(context.Context) (contract.UpdateStatus, error)
	Plan(context.Context) (contract.UpdatePlanResult, error)
	Apply(context.Context) (contract.UpdateApplyResult, error)
}

type WhaleService interface {
	List(context.Context) ([]contract.WhaleOperation, error)
	Get(context.Context, string) (contract.WhaleOperation, error)
	BeginPut(context.Context, contract.WhalePutBeginPayload) (contract.WhaleOperation, error)
	BeginGet(context.Context, contract.WhaleGetBeginPayload) (contract.WhaleOperation, error)
	ConfirmGet(context.Context, string) (contract.WhaleOperation, error)
	Retry(context.Context, string) (contract.WhaleOperation, error)
	Cancel(context.Context, string, bool) (contract.WhaleOperation, error)
}

type ActivitySource interface {
	List() []activity.Entry
}

func (s *Server) SetActivitySource(source ActivitySource) {
	s.mu.Lock()
	s.activity = source
	s.mu.Unlock()
}

// DetachmentSource answers which relationships with servers have recently
// ended. The daemon (cmd/filees) owns the durable record and applies its own
// expiry; ipcserver only pulls, the same split used for ActivitySource.
//
// It is a source rather than a service on purpose: there is no command here
// and no capability to advertise. A detachment is something the reader is told
// about, never something acted on from this side - the way back is to activate
// the client again, which is its own command already.
type DetachmentSource interface {
	List() []contract.Detachment
}

func (s *Server) SetDetachmentSource(source DetachmentSource) {
	s.mu.Lock()
	s.detachments = source
	s.mu.Unlock()
}

func (s *Server) detachmentSource() DetachmentSource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.detachments
}

// PublicShareSource answers the cached, cross-repo aggregate of every public
// share the daemon has discovered across all activated servers. The daemon
// (cmd/filees) refreshes it as each server's projection updates; ipcserver
// only pulls from it, the same split used for ActivitySource above.
type PublicShareSource interface {
	List() []contract.PublicShareSummary
}

func (s *Server) SetPublicShareSource(source PublicShareSource) {
	s.mu.Lock()
	s.publicShareAggregate = source
	s.mu.Unlock()
}

func (s *Server) publicShareSource() PublicShareSource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.publicShareAggregate
}

func (s *Server) activitySource() ActivitySource {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activity
}

func (s *Server) SetUpdateService(service UpdateService) {
	s.mu.Lock()
	s.updates = service
	s.mu.Unlock()
}

func (s *Server) updateService() UpdateService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updates
}

func (s *Server) capabilities() []string {
	caps := append([]string(nil), contract.AllCapabilities...)
	if s.realmGrantService() != nil {
		caps = append(caps, contract.CapRealmGrantRecipients, contract.CapRealmSetVisibility, contract.CapRepoGrantAccess, contract.CapRepoRevokeAccess)
	}
	if s.realmPublicBrandingService() != nil {
		caps = append(caps, contract.CapRealmPublicBrandingGet, contract.CapRealmPublicBrandingSet)
	}
	if s.publicShareService() != nil {
		caps = append(caps, contract.CapRepoPublicShareList, contract.CapRepoPublicShareCreate, contract.CapRepoPublicShareUpdate, contract.CapRepoPublicShareRevoke, contract.CapRepoPublicShareDelete)
	}
	if s.uploadChannelService() != nil {
		caps = append(caps, contract.CapRepoUploadChannelList, contract.CapRepoUploadChannelCreate, contract.CapRepoUploadChannelUpdate, contract.CapRepoUploadChannelRevoke, contract.CapRepoUploadChannelDelete, contract.CapRepoQuarantineList, contract.CapRepoQuarantineHide, contract.CapRepoQuarantineFetch)
	}
	if s.lockReleaseService() != nil {
		caps = append(caps, contract.CapLockReleaseRequest, contract.CapLockReleaseDismiss, contract.CapLockReleaseAccept)
	}
	if s.editingPolicyService() != nil {
		caps = append(caps, contract.CapRepoSetEditingPolicy)
	}
	if s.sessionTimeoutService() != nil {
		caps = append(caps, contract.CapServerSetSessionTimeout)
	}
	if s.systemLifecycleService() != nil {
		caps = append(caps, contract.CapSystemRestart, contract.CapSystemShutdown)
	}
	if s.updateService() != nil {
		caps = append(caps, contract.CapUpdateStatus, contract.CapUpdatePlan, contract.CapUpdateApply)
	}
	if s.activitySource() != nil {
		caps = append(caps, contract.CapRepoActivity)
	}
	if s.whaleService() != nil {
		caps = append(caps, contract.CapWhaleList, contract.CapWhaleGet, contract.CapWhalePutBegin, contract.CapWhaleGetBegin, contract.CapWhaleGetConfirm, contract.CapWhaleRetry, contract.CapWhaleCancel)
	}
	return caps
}

func (s *Server) SetWhaleService(service WhaleService) {
	s.mu.Lock()
	s.whales = service
	s.mu.Unlock()
}

func (s *Server) whaleService() WhaleService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.whales
}

func (s *Server) SetSystemLifecycleService(service SystemLifecycleService) {
	s.mu.Lock()
	s.lifecycleFn = service
	s.mu.Unlock()
}

func (s *Server) systemLifecycleService() SystemLifecycleService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lifecycleFn
}

func (s *Server) SetRepositoryLifecycleService(service RepositoryLifecycleService) {
	s.mu.Lock()
	s.lifecycle = service
	s.mu.Unlock()
}

func (s *Server) repositoryLifecycleService() RepositoryLifecycleService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lifecycle
}

func (s *Server) SetMobilePairingService(service MobilePairingService) {
	s.mu.Lock()
	s.mobilePair = service
	s.mu.Unlock()
}

func (s *Server) mobilePairingService() MobilePairingService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mobilePair
}

func (s *Server) SetServerDetachService(service ServerDetachService) {
	s.mu.Lock()
	s.serverDetach = service
	s.mu.Unlock()
}

func (s *Server) serverDetachService() ServerDetachService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.serverDetach
}

func (s *Server) SetSessionTimeoutService(service SessionTimeoutService) {
	s.mu.Lock()
	s.sessionTimeout = service
	s.mu.Unlock()
}

func (s *Server) sessionTimeoutService() SessionTimeoutService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionTimeout
}

func (s *Server) SetRealmRemovalService(service RealmRemovalService) {
	s.mu.Lock()
	s.realmRemoval = service
	s.mu.Unlock()
}

func (s *Server) realmRemovalService() RealmRemovalService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.realmRemoval
}

func (s *Server) SetActivationService(service ActivationService) {
	s.mu.Lock()
	s.activation = service
	s.mu.Unlock()
}

func (s *Server) SetRealmAliasService(service RealmAliasService) {
	s.mu.Lock()
	s.realmAlias = service
	s.mu.Unlock()
}

func (s *Server) realmAliasService() RealmAliasService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.realmAlias
}

func (s *Server) SetRealmGrantService(service RealmGrantService) {
	s.mu.Lock()
	s.realmGrants = service
	s.mu.Unlock()
}

func (s *Server) realmGrantService() RealmGrantService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.realmGrants
}

func (s *Server) SetRealmPublicBrandingService(service RealmPublicBrandingService) {
	s.mu.Lock()
	s.realmBranding = service
	s.mu.Unlock()
}

func (s *Server) realmPublicBrandingService() RealmPublicBrandingService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.realmBranding
}

func (s *Server) SetEditingPolicyService(service EditingPolicyService) {
	s.mu.Lock()
	s.editingPolicy = service
	s.mu.Unlock()
}

func (s *Server) editingPolicyService() EditingPolicyService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.editingPolicy
}

func (s *Server) SetPublicShareService(service PublicShareService) {
	s.mu.Lock()
	s.publicShares = service
	s.mu.Unlock()
}

func (s *Server) publicShareService() PublicShareService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.publicShares
}

func (s *Server) SetUploadChannelService(service UploadChannelService) {
	s.mu.Lock()
	s.uploadChannels = service
	s.mu.Unlock()
}

func (s *Server) uploadChannelService() UploadChannelService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.uploadChannels
}

func (s *Server) SetOwnerLabelResolver(resolver OwnerLabelResolver) {
	s.mu.Lock()
	s.ownerLabels = resolver
	s.mu.Unlock()
}

func (s *Server) SetLockReleaseService(service LockReleaseService) {
	s.mu.Lock()
	s.lockReleases = service
	s.mu.Unlock()
}

func (s *Server) lockReleaseService() LockReleaseService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lockReleases
}

// SetLockReleaseProjection replaces one activation's private slice with the
// latest validated clientview generation. It never contacts the server.
func (s *Server) SetLockReleaseProjection(serverID string, requests []contract.LockReleaseRequest) {
	cloned := append([]contract.LockReleaseRequest(nil), requests...)
	s.mu.Lock()
	if s.lockReleaseRequests == nil {
		s.lockReleaseRequests = make(map[string][]contract.LockReleaseRequest)
	}
	current := s.lockReleaseRequests[serverID]
	changed := !sameLockReleaseProjection(current, cloned)
	if changed {
		s.lockReleaseRequests[serverID] = cloned
	}
	s.mu.Unlock()
	if changed {
		s.Emit(contract.NewEvent("", 0, contract.EvLockReleaseChanged, "", nil))
	}
}

func sameLockReleaseProjection(left, right []contract.LockReleaseRequest) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (s *Server) allLockReleaseRequests() []contract.LockReleaseRequest {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var requests []contract.LockReleaseRequest
	for _, projected := range s.lockReleaseRequests {
		requests = append(requests, projected...)
	}
	sort.Slice(requests, func(i, j int) bool {
		if requests[i].UpdatedAt != requests[j].UpdatedAt {
			return requests[i].UpdatedAt > requests[j].UpdatedAt
		}
		return requests[i].RequestID < requests[j].RequestID
	})
	return requests
}

func (s *Server) ownerLabelResolver() OwnerLabelResolver {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ownerLabels
}

func (s *Server) activationService() ActivationService {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.activation
}

// New creates a Server that will listen on sockPath.
func New(sockPath string) *Server {
	return &Server{
		sockPath:            sockPath,
		startTime:           time.Now(),
		lg:                  talk.With("ipc"),
		repos:               make(map[string]*RepoState),
		activations:         make(map[string]contract.ActivationStatus),
		lockReleaseRequests: make(map[string][]contract.LockReleaseRequest),
		subs:                make(map[chan contract.Event]struct{}),
		conns:               make(map[net.Conn]struct{}),
	}
}

func (s *Server) RegisterActivation(status contract.ActivationStatus) {
	s.mu.Lock()
	s.activations[status.ServerID] = status
	s.mu.Unlock()
	s.Emit(contract.NewEvent("", 0, contract.EvActivationChanged, "", status))
}

// RemoveServer clears one profile's in-memory activation and repositories
// after its credential has been revoked and local profile removed.
func (s *Server) RemoveServer(serverID string) {
	s.mu.Lock()
	delete(s.activations, serverID)
	delete(s.lockReleaseRequests, serverID)
	for id, repo := range s.repos {
		if repo.ServerID() == serverID {
			delete(s.repos, id)
		}
	}
	s.mu.Unlock()
	s.Emit(contract.NewEvent("", 0, contract.EvProjectionChanged, "", nil))
}

func (s *Server) SetActivationRepositoryReadiness(serverID string, ready bool, pending int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status, ok := s.activations[serverID]
	if !ok {
		return
	}
	status.RepositoriesReady = ready
	status.PendingRequiredRepos = pending
	s.activations[serverID] = status
}

func (s *Server) allActivations() []contract.ActivationStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contract.ActivationStatus, 0, len(s.activations))
	for _, status := range s.activations {
		out = append(out, status)
	}
	return out
}

// DefaultSocketPath returns the canonical per-user socket path.
// Prefers $XDG_RUNTIME_DIR (Linux systemd sessions) for proper tmpfs placement.
func DefaultSocketPath() string {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "filees.sock")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".filees", "daemon.sock")
}

// RegisterRepo adds a repo to the server's registry. Returns the RepoState the
// daemon should update as operations progress. Must be called before Start.
func (s *Server) RegisterRepo(id, url, localPath string) *RepoState {
	return s.RegisterRepoAccess(id, url, localPath, "default", contract.AccessReadWrite)
}

func (s *Server) RegisterRepoAccess(id, url, localPath, serverID, access string) *RepoState {
	displayName := filepath.Base(filepath.Clean(localPath))
	if displayName == "." || displayName == string(filepath.Separator) || displayName == "" {
		displayName = id
	}
	rs := &RepoState{
		server:       s,
		id:           id,
		url:          url,
		localPath:    localPath,
		serverID:     serverID,
		access:       access,
		displayName:  displayName,
		attached:     true,
		state:        contract.StateInitializing,
		connectivity: contract.ConnOnline,
	}
	s.mu.Lock()
	s.repos[id] = rs
	s.mu.Unlock()
	return rs
}

func (s *Server) RegisterProjectedRepo(id, displayName, url, serverID, access, state string, attached bool) *RepoState {
	return s.RegisterProjectedRepoPolicy(id, displayName, url, serverID, access, state, "", "optional", attached)
}

func (s *Server) RegisterProjectedRepoPolicy(id, displayName, url, serverID, access, state, ownerRealmID, attachmentPolicy string, attached bool) *RepoState {
	s.mu.Lock()
	rs := s.repos[id]
	if rs == nil {
		rs = &RepoState{server: s, id: id, serverID: serverID, connectivity: contract.ConnOnline}
		s.repos[id] = rs
	}
	s.mu.Unlock()
	rs.SetProjectedMetadata(displayName, url, access, state, ownerRealmID, attachmentPolicy, attached)
	return rs
}

// ReconcileProjectedRepos replaces the presentation knowledge for one server.
// Repositories omitted by the authoritative projection are removed from IPC;
// this never removes or otherwise mutates their local working copies.
func (s *Server) ReconcileProjectedRepos(serverID string, repos []ProjectedRepo) {
	present := make(map[string]struct{}, len(repos))
	for _, repo := range repos {
		present[repo.ID] = struct{}{}
		state := s.RegisterProjectedRepoPolicy(repo.ID, repo.DisplayName, repo.URL, serverID, repo.Access, repo.State, repo.OwnerRealmID, repo.AttachmentPolicy, repo.Attached)
		if repo.PendingLocalPath != "" {
			state.SetPendingLocalPath(repo.PendingLocalPath)
		}
		state.SetDeletionMetadata(repo.ServerDeleted, repo.LocalCleanupPending, repo.RetainUntil, repo.RecoveryOperationID, repo.RecoveryAvailable, repo.RecoveryPending, repo.CleanupError)
		state.SetLifecycleRepairMetadata(repo.LifecycleOperationID, repo.LifecycleError, repo.CanRetryLifecycle, repo.CanAbandonLifecycle)
		state.SetEditingPolicy(repo.EditingPolicy)
		state.SetPurpose(repo.Purpose)
	}
	s.mu.Lock()
	removed := false
	for id, repo := range s.repos {
		if repo.ServerID() != serverID {
			continue
		}
		if _, ok := present[id]; !ok {
			delete(s.repos, id)
			removed = true
		}
	}
	s.mu.Unlock()
	if removed {
		s.Emit(contract.NewEvent("", 0, contract.EvProjectionChanged, "", nil))
	}
}

// MarkRepoDetached clears only local attachment knowledge when no current
// authoritative projection is available (for example during an offline
// detach). Server-owned metadata remains presentation-only until the next
// projection refresh.
func (s *Server) MarkRepoDetached(serverID, repoID string) {
	repo := s.repoByID(repoID)
	if repo == nil || repo.ServerID() != serverID {
		return
	}
	repo.markDetached()
	s.Emit(contract.NewEvent("", 0, contract.EvProjectionChanged, "", nil))
}

// RepoState returns the already-registered state for one server-scoped
// repository. It is used by daemon-side projection adapters after an
// authoritative ReconcileProjectedRepos pass; it never creates knowledge.
func (s *Server) RepoState(serverID, repoID string) *RepoState {
	repo := s.repoByID(repoID)
	if repo == nil || repo.ServerID() != serverID {
		return nil
	}
	return repo
}

type ProjectedRepo struct {
	ID, DisplayName, URL, Access, State    string
	OwnerRealmID, AttachmentPolicy         string
	EditingPolicy                          string
	Purpose                                string
	Attached                               bool
	PendingLocalPath                       string
	ServerDeleted, LocalCleanupPending     bool
	RetainUntil, RecoveryOperationID       string
	RecoveryAvailable                      bool
	RecoveryPending                        bool
	CleanupError                           string
	LifecycleOperationID, LifecycleError   string
	CanRetryLifecycle, CanAbandonLifecycle bool
}

// NewRepoEvent builds an event envelope for the given repo.
// Sequence and EventID are intentionally zero/empty: Emit() assigns them
// inside subsMu so sequence order == delivery order.
func (s *Server) NewRepoEvent(repoID, evType string, payload any) contract.Event {
	return contract.NewEvent("", 0, evType, repoID, payload)
}

// Start binds the socket and begins accepting connections, then returns.
// Cancelling ctx closes the listener and every active request or event-stream
// connection so clients can immediately enter their reconnect flow.
func (s *Server) Start(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.sockPath), 0o700); err != nil {
		return err
	}

	ln, err := listenUnixSocket(s.sockPath)
	if err != nil {
		return err
	}
	_ = os.Chmod(s.sockPath, 0o600) // restrict to owner
	ownedSocket, err := os.Stat(s.sockPath)
	if err != nil {
		_ = ln.Close()
		return err
	}

	s.lg.Infof("listening on %s", s.sockPath)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
		s.closeConnections()
		removeSocketIfOwned(s.sockPath, ownedSocket)
	}()

	go s.acceptLoop(ln)
	return nil
}

// listenUnixSocket preserves a live daemon and removes only a stale Unix
// socket. Blindly unlinking before Listen lets a second daemon bind the same
// pathname while the first one keeps an orphaned listener alive.
func listenUnixSocket(path string) (net.Listener, error) {
	ln, err := net.Listen("unix", path)
	if err == nil {
		disableUnlinkOnClose(ln)
		return ln, nil
	}
	info, statErr := os.Lstat(path)
	if statErr != nil || info.Mode()&os.ModeSocket == 0 {
		return nil, err
	}
	conn, dialErr := net.DialTimeout("unix", path, 250*time.Millisecond)
	if dialErr == nil {
		_ = conn.Close()
		return nil, fmt.Errorf("filees daemon is already listening on %s", path)
	}
	if removeErr := os.Remove(path); removeErr != nil {
		return nil, fmt.Errorf("remove stale IPC socket: %w", removeErr)
	}
	ln, err = net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	disableUnlinkOnClose(ln)
	return ln, nil
}

func disableUnlinkOnClose(listener net.Listener) {
	if unix, ok := listener.(*net.UnixListener); ok {
		unix.SetUnlinkOnClose(false)
	}
}

// removeSocketIfOwned prevents an older daemon from unlinking a replacement
// socket that was bound after its own pathname disappeared.
func removeSocketIfOwned(path string, owned os.FileInfo) {
	current, err := os.Stat(path)
	if err == nil && os.SameFile(owned, current) {
		_ = os.Remove(path)
	}
}

func (s *Server) acceptLoop(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return // listener closed — normal on shutdown
		}
		s.connsMu.Lock()
		if s.stopping {
			s.connsMu.Unlock()
			_ = c.Close()
			continue
		}
		s.conns[c] = struct{}{}
		s.connsMu.Unlock()
		go func() {
			defer func() {
				s.connsMu.Lock()
				delete(s.conns, c)
				s.connsMu.Unlock()
			}()
			s.handleConn(c)
		}()
	}
}

func (s *Server) closeConnections() {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	s.stopping = true
	for conn := range s.conns {
		_ = conn.Close()
		delete(s.conns, conn)
	}
}

// Emit assigns a monotone sequence number and broadcasts ev to all subscribers.
// Sequence is assigned inside subsMu so delivery order == sequence order:
// two concurrent callers cannot produce seq=N delivered after seq=N+1.
// Slow subscribers receive a dropped event (non-blocking send); they must
// resync via repo.status.
func (s *Server) Emit(ev contract.Event) {
	s.subsMu.Lock()
	seq := atomic.AddInt64(&s.evSeq, 1)
	ev.Sequence = seq
	ev.EventID = fmt.Sprintf("%016x", seq)
	for ch := range s.subs {
		select {
		case ch <- ev:
		default:
		}
	}
	s.subsMu.Unlock()
}

func (s *Server) addSub(ch chan contract.Event) {
	s.subsMu.Lock()
	s.subs[ch] = struct{}{}
	s.subsMu.Unlock()
}

func (s *Server) removeSub(ch chan contract.Event) {
	s.subsMu.Lock()
	delete(s.subs, ch)
	s.subsMu.Unlock()
}

// repoByID looks up a repo under read lock. Returns nil if not found.
func (s *Server) repoByID(id string) *RepoState {
	s.mu.RLock()
	rs := s.repos[id]
	s.mu.RUnlock()
	return rs
}

// allRepos returns a snapshot slice of all registered repos.
func (s *Server) allRepos() []*RepoState {
	s.mu.RLock()
	out := make([]*RepoState, 0, len(s.repos))
	for _, rs := range s.repos {
		out = append(out, rs)
	}
	s.mu.RUnlock()
	return out
}

// uptime returns seconds since the server started.
func (s *Server) uptime() int64 {
	return int64(time.Since(s.startTime).Seconds())
}
