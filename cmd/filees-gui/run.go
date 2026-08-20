package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"filees/internal/gui/actions"
	"filees/internal/gui/app"
	"filees/internal/gui/notifications"
	"filees/internal/gui/platform"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
	"filees/pkg/localpin"
	"filees/pkg/realmbranding"
	"filees/public-shares/gate"
	"github.com/google/uuid"
)

var errGUIRestartRequested = errors.New("GUI restart requested")

type dependencies struct {
	tray      tray.Backend
	platform  platform.Backend
	client    app.DaemonClient
	icons     tray.IconSet
	activator actions.Activator
	// pinStore is nil if the local PIN store could not be opened (e.g. no
	// durable state root available) - the local-PIN feature is then simply
	// disabled, activation and mobile pairing still work.
	pinStore *localpin.Store
}

type viewStore struct {
	mu sync.RWMutex
	vm app.ViewModel
}

type updateClient interface {
	UpdatePlan(context.Context) (*contract.UpdatePlanResult, error)
	UpdateApply(context.Context) (*contract.UpdateApplyResult, error)
}

type repositoryCreateClient interface {
	RepoCreateRequest(context.Context, contract.RepoCreateRequestPayload) (*contract.RepoLifecycleResult, error)
	RepoLifecycleStatus(context.Context, string) (*contract.RepoLifecycleResult, error)
}

type repositoryCreateAdapter struct{ client repositoryCreateClient }

func (adapter repositoryCreateAdapter) CreateRepository(ctx context.Context, serverID, displayName, localPath string) (string, error) {
	result, err := adapter.client.RepoCreateRequest(ctx, contract.RepoCreateRequestPayload{ServerID: serverID, DisplayName: displayName, LocalPath: localPath})
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", errors.New("daemon returned an empty repository operation")
	}
	return result.OperationID, nil
}

func (adapter repositoryCreateAdapter) CreationStatus(ctx context.Context, operationID string) (state, lastError string, err error) {
	result, err := adapter.client.RepoLifecycleStatus(ctx, operationID)
	if err != nil {
		return "", "", err
	}
	if result == nil {
		return "", "", errors.New("daemon returned an empty repository operation")
	}
	return result.State, result.LastError, nil
}

type repositoryAttachClient interface {
	RepoAttachIntent(context.Context, contract.RepoAttachIntentPayload) (*contract.RepoLifecycleResult, error)
	RepoAttachApprove(context.Context, contract.RepoAttachApprovePayload) (*contract.RepoLifecycleResult, error)
	RepoLifecycleStatus(context.Context, string) (*contract.RepoLifecycleResult, error)
}

type repositoryAttachAdapter struct{ client repositoryAttachClient }

type repositoryLocateClient interface {
	RepoLocate(context.Context, contract.RepoLocatePayload) (*contract.RepoLifecycleResult, error)
}

type repositoryLocateAdapter struct{ client repositoryLocateClient }

func (adapter repositoryLocateAdapter) LocateRepository(ctx context.Context, serverID, repoID, existingLocalPath string) (string, error) {
	result, err := adapter.client.RepoLocate(ctx, contract.RepoLocatePayload{ServerID: serverID, RepoID: repoID, ExistingLocalPath: existingLocalPath})
	if err != nil {
		return "", err
	}
	if result == nil || result.OperationID == "" {
		return "", errors.New("daemon returned an empty repository locate operation")
	}
	return result.OperationID, nil
}

func (adapter repositoryAttachAdapter) AttachRepository(ctx context.Context, serverID, repoID, localPath string) (string, error) {
	intent, err := adapter.client.RepoAttachIntent(ctx, contract.RepoAttachIntentPayload{ServerID: serverID, RepoID: repoID, LocalPath: localPath})
	if err != nil {
		return "", err
	}
	if intent == nil || intent.OperationID == "" {
		return "", errors.New("daemon returned an empty repository attachment intent")
	}
	approved, err := adapter.client.RepoAttachApprove(ctx, contract.RepoAttachApprovePayload{OperationID: intent.OperationID, ServerID: serverID, RepoID: repoID})
	if err != nil {
		return "", err
	}
	if approved == nil || approved.OperationID != intent.OperationID {
		return "", errors.New("daemon returned an invalid repository attachment approval")
	}
	return approved.OperationID, nil
}

func (adapter repositoryAttachAdapter) AttachmentStatus(ctx context.Context, operationID string) (state, lastError string, err error) {
	result, err := adapter.client.RepoLifecycleStatus(ctx, operationID)
	if err != nil {
		return "", "", err
	}
	if result == nil {
		return "", "", errors.New("daemon returned an empty repository attachment operation")
	}
	return result.State, result.LastError, nil
}

type repositoryDetachClient interface {
	RepoDetach(context.Context, string, string) (*contract.RepoLifecycleResult, error)
	RepoDelete(context.Context, string, string) (*contract.RepoLifecycleResult, error)
}

type repositoryDetachAdapter struct{ client repositoryDetachClient }

type repositoryDumpLoadClient interface {
	RepoLoadDump(context.Context, string, string, bool, *int) (*contract.RepoLifecycleResult, error)
}

type repositoryDumpLoadAdapter struct{ client repositoryDumpLoadClient }

// LoadDump takes no options yet (actions.RepositoryDumpLoader, first pass):
// always apply the shared ignore policy, never bound the history.
func (adapter repositoryDumpLoadAdapter) LoadDump(ctx context.Context, serverID, repoID string) error {
	operationCtx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	result, err := adapter.client.RepoLoadDump(operationCtx, serverID, repoID, true, nil)
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("daemon returned an empty repository dump-load result")
	}
	return nil
}

type serverDetachClient interface {
	ServerDetach(context.Context, string) (*contract.ServerDetachResult, error)
}

type serverDetachAdapter struct{ client serverDetachClient }

func (adapter serverDetachAdapter) DetachServer(ctx context.Context, serverID string) error {
	result, err := adapter.client.ServerDetach(ctx, serverID)
	if err != nil {
		return err
	}
	if result == nil || result.ServerID != serverID {
		return errors.New("daemon did not complete server detach")
	}
	return nil
}

type realmRemovalClient interface {
	RealmRemoveBegin(context.Context, contract.RealmRemoveBeginPayload) (*contract.RealmRemoveBeginResult, error)
	RealmRemoveConfirm(context.Context, contract.RealmRemoveConfirmPayload) (*contract.RealmRemoveConfirmResult, error)
	RecoveryDownload(context.Context, contract.RecoveryDownloadPayload) (*contract.RecoveryDownloadResult, error)
}

func (adapter realmRemovalAdapter) DownloadRecovery(ctx context.Context, operationID, outputRoot string) ([]string, error) {
	result, err := adapter.client.RecoveryDownload(ctx, contract.RecoveryDownloadPayload{OperationID: operationID, OutputRoot: outputRoot})
	if err != nil {
		return nil, err
	}
	if result == nil || result.OperationID != operationID {
		return nil, errors.New("daemon returned an invalid recovery download result")
	}
	return result.Paths, nil
}

type realmRemovalAdapter struct{ client realmRemovalClient }

func (adapter realmRemovalAdapter) BeginRealmRemoval(ctx context.Context, request actions.RealmRemovalBeginRequest) (actions.RealmRemovalBeginResult, error) {
	result, err := adapter.client.RealmRemoveBegin(ctx, contract.RealmRemoveBeginPayload{
		ServerID: request.ServerID, NotificationEmail: request.NotificationEmail,
		RecoveryDirectory: request.RecoveryDirectory, ErasureRequested: request.ErasureRequested,
	})
	if err != nil {
		return actions.RealmRemovalBeginResult{}, err
	}
	if result == nil {
		return actions.RealmRemovalBeginResult{}, errors.New("daemon returned an empty realm removal request")
	}
	return actions.RealmRemovalBeginResult{
		OperationID: result.OperationID, RecoveryKitPath: result.RecoveryKitPath, ExpiresAt: result.ExpiresAt,
		ActiveClientCount: result.ActiveClientCount, OwnedRepositoryCount: result.OwnedRepositoryCount, ForeignGrantCount: result.ForeignGrantCount,
	}, nil
}

func (adapter realmRemovalAdapter) ConfirmRealmRemoval(ctx context.Context, serverID, operationID string, otp []byte, kitPath string) (actions.RealmRemovalConfirmResult, error) {
	result, err := adapter.client.RealmRemoveConfirm(ctx, contract.RealmRemoveConfirmPayload{
		ServerID: serverID, OperationID: operationID, RecoveryKitPath: kitPath, OTP: contract.Secret(otp),
	})
	if err != nil {
		return actions.RealmRemovalConfirmResult{}, err
	}
	if result == nil {
		return actions.RealmRemovalConfirmResult{}, errors.New("daemon returned an empty realm removal confirmation")
	}
	return actions.RealmRemovalConfirmResult{
		RecoveryKitPath: result.RecoveryKitPath, ArchiveCount: result.ArchiveCount,
		DownloadUntil: result.DownloadUntil, AdminGraceUntil: result.AdminGraceUntil,
		ErasureRequested: result.ErasureRequested, ErasureMaxDays: result.ErasureMaxDays,
	}, nil
}

type reservationClient interface {
	RepoReservationList(context.Context, string) (*contract.RepoReservationListResult, error)
	RepoReservationRelease(context.Context, contract.RepoReservationReleasePayload) error
}

type realmAliasClient interface {
	RealmAliasClaim(context.Context, string, string) (*contract.RealmAliasClaimResult, error)
}

type realmAliasAdapter struct{ client realmAliasClient }

func (adapter realmAliasAdapter) ClaimAlias(ctx context.Context, serverID, alias string) error {
	result, err := adapter.client.RealmAliasClaim(ctx, serverID, alias)
	if err != nil {
		return err
	}
	if result == nil || result.Alias == "" {
		return errors.New("daemon returned an empty realm alias")
	}
	return nil
}

type realmGrantClient interface {
	RealmGrantRecipients(context.Context, string, string) (*contract.RealmGrantRecipientsResult, error)
	RealmSetVisibility(context.Context, string, string) (*contract.RealmSetVisibilityResult, error)
	RepoGrantAccess(context.Context, contract.RepoGrantAccessPayload) (*contract.RealmGrantResult, error)
	RepoRevokeAccess(context.Context, contract.RepoRevokeAccessPayload) (*contract.RealmGrantResult, error)
	RepoSetEditingPolicy(context.Context, contract.RepoSetEditingPolicyPayload) (*contract.RepoSetEditingPolicyResult, error)
}

type realmGrantAdapter struct{ client realmGrantClient }

type realmBrandingClient interface {
	RealmPublicBranding(context.Context, string) (*contract.RealmPublicBrandingResult, error)
	RealmSetPublicBranding(context.Context, string, realmbranding.Branding) (*contract.RealmPublicBrandingResult, error)
}

type sessionTimeoutClient interface {
	ServerSetSessionTimeout(context.Context, contract.ServerSetSessionTimeoutPayload) (*contract.ServerSetSessionTimeoutResult, error)
}

type sessionTimeoutAdapter struct{ client sessionTimeoutClient }

func (adapter sessionTimeoutAdapter) SetSessionTimeout(ctx context.Context, serverID string, minutes int) (int, error) {
	result, err := adapter.client.ServerSetSessionTimeout(ctx, contract.ServerSetSessionTimeoutPayload{ServerID: serverID, Minutes: minutes})
	if err != nil {
		return 0, err
	}
	if result == nil {
		return 0, errors.New("daemon returned an empty session timeout")
	}
	return result.Minutes, nil
}

type realmBrandingAdapter struct{ client realmBrandingClient }

func (adapter realmGrantAdapter) ListRecipients(ctx context.Context, serverID, repoID string) ([]actions.RealmGrantRecipient, error) {
	result, err := adapter.client.RealmGrantRecipients(ctx, serverID, repoID)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("daemon returned an empty realm directory")
	}
	recipients := make([]actions.RealmGrantRecipient, 0, len(result.Recipients))
	for _, recipient := range result.Recipients {
		recipients = append(recipients, actions.RealmGrantRecipient{RealmID: recipient.RealmID, Alias: recipient.Alias, Access: recipient.Access, State: recipient.State})
	}
	return recipients, nil
}

func (adapter realmGrantAdapter) SetVisibility(ctx context.Context, serverID, visibility string) error {
	result, err := adapter.client.RealmSetVisibility(ctx, serverID, visibility)
	if err != nil {
		return err
	}
	if result == nil || result.Visibility != visibility {
		return errors.New("daemon returned an invalid realm visibility result")
	}
	return nil
}

func (adapter realmBrandingAdapter) PublicBranding(ctx context.Context, serverID string) (realmbranding.Branding, error) {
	result, err := adapter.client.RealmPublicBranding(ctx, serverID)
	if err != nil {
		return realmbranding.Branding{}, err
	}
	if result == nil {
		return realmbranding.Branding{}, errors.New("daemon returned empty realm branding")
	}
	return realmbranding.Normalize(result.Branding)
}

func (adapter realmBrandingAdapter) SetPublicBranding(ctx context.Context, serverID string, branding realmbranding.Branding) (realmbranding.Branding, error) {
	result, err := adapter.client.RealmSetPublicBranding(ctx, serverID, branding)
	if err != nil {
		return realmbranding.Branding{}, err
	}
	if result == nil {
		return realmbranding.Branding{}, errors.New("daemon returned empty realm branding")
	}
	return realmbranding.Normalize(result.Branding)
}

func (adapter realmGrantAdapter) Grant(ctx context.Context, serverID, repoID, recipientRealmID, access string) error {
	result, err := adapter.client.RepoGrantAccess(ctx, contract.RepoGrantAccessPayload{ServerID: serverID, RepoID: repoID, RecipientRealmID: recipientRealmID, Access: access})
	if err != nil {
		return err
	}
	if result == nil || result.RepoID != repoID || result.RecipientRealmID != recipientRealmID || result.Access != access || result.State != "active" {
		return errors.New("daemon returned an invalid realm grant result")
	}
	return nil
}

func (adapter realmGrantAdapter) Revoke(ctx context.Context, serverID, repoID, recipientRealmID string) error {
	result, err := adapter.client.RepoRevokeAccess(ctx, contract.RepoRevokeAccessPayload{ServerID: serverID, RepoID: repoID, RecipientRealmID: recipientRealmID})
	if err != nil {
		return err
	}
	if result == nil || result.RepoID != repoID || result.RecipientRealmID != recipientRealmID || result.State != "revoked" {
		return errors.New("daemon returned an invalid realm grant revoke result")
	}
	return nil
}

type publicShareClient interface {
	PublicShareList(context.Context, string, string) (*contract.PublicShareListResult, error)
	PublicShareCreate(context.Context, contract.PublicShareCreatePayload) (*contract.PublicShareResult, error)
	PublicShareUpdate(context.Context, contract.PublicShareUpdatePayload) (*contract.PublicShareResult, error)
	PublicShareRevoke(context.Context, contract.PublicShareChannelPayload) (*contract.PublicShareResult, error)
	PublicShareDelete(context.Context, contract.PublicShareChannelPayload) (*contract.PublicShareResult, error)
}

type publicShareAdapter struct{ client publicShareClient }

func (adapter publicShareAdapter) ListPublicShares(ctx context.Context, serverID, repoID string) ([]actions.PublicShareSummary, error) {
	result, err := adapter.client.PublicShareList(ctx, serverID, repoID)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("daemon returned an empty public share list")
	}
	shares := make([]actions.PublicShareSummary, 0, len(result.Shares))
	for _, share := range result.Shares {
		row := actions.PublicShareSummary{ChannelID: share.ChannelID, Alias: share.Alias, Slug: share.Slug, State: share.State, SourceRoot: share.SourceRoot, UpdatedAt: share.UpdatedAt, Recipients: append([]string(nil), share.Recipients...), PasswordProtected: share.PasswordProtected, DoNotFollow: share.DoNotFollow}
		for _, object := range share.Objects {
			row.Objects = append(row.Objects, actions.PublicShareObject{PublicID: object.PublicID, RepoPath: object.RepoPath, DisplayName: object.DisplayName, Size: object.Size})
		}
		shares = append(shares, row)
	}
	return shares, nil
}

func (adapter publicShareAdapter) CreatePublicShare(ctx context.Context, serverID string, declaration actions.PublicShareDeclaration) error {
	remote, err := publicShareDeclarationToContract(declaration)
	if err != nil {
		return err
	}
	result, err := adapter.client.PublicShareCreate(ctx, contract.PublicShareCreatePayload{ServerID: serverID, PublicShareDeclaration: remote})
	if err != nil {
		return err
	}
	if result == nil || result.ChannelID == "" || result.State != "active" {
		return errors.New("daemon returned an invalid public share result")
	}
	return nil
}

func (adapter publicShareAdapter) UpdatePublicShare(ctx context.Context, serverID, channelID string, declaration actions.PublicShareDeclaration) error {
	remote, err := publicShareDeclarationToContract(declaration)
	if err != nil {
		return err
	}
	result, err := adapter.client.PublicShareUpdate(ctx, contract.PublicShareUpdatePayload{ServerID: serverID, ChannelID: channelID, KeepPassword: declaration.KeepPassword, PublicShareDeclaration: remote})
	if err != nil {
		return err
	}
	if result == nil || result.ChannelID != channelID || result.State != "active" {
		return errors.New("daemon returned an invalid public share update result")
	}
	return nil
}

func (adapter publicShareAdapter) RevokePublicShare(ctx context.Context, serverID, repoID, channelID string) error {
	result, err := adapter.client.PublicShareRevoke(ctx, contract.PublicShareChannelPayload{ServerID: serverID, RepoID: repoID, ChannelID: channelID})
	if err != nil {
		return err
	}
	if result == nil || result.ChannelID != channelID || result.State != "revoked" {
		return errors.New("daemon returned an invalid public share revoke result")
	}
	return nil
}

func (adapter publicShareAdapter) DeletePublicShare(ctx context.Context, serverID, repoID, channelID string) error {
	result, err := adapter.client.PublicShareDelete(ctx, contract.PublicShareChannelPayload{ServerID: serverID, RepoID: repoID, ChannelID: channelID})
	if err != nil {
		return err
	}
	if result == nil || result.ChannelID != channelID || result.State != "deleted" {
		return errors.New("daemon returned an invalid public share delete result")
	}
	return nil
}

func publicShareDeclarationToContract(declaration actions.PublicShareDeclaration) (contract.PublicShareDeclaration, error) {
	passwordHash := ""
	if len(declaration.Password) > 0 {
		var err error
		passwordHash, err = gate.HashPassword(string(declaration.Password), nil)
		if err != nil {
			return contract.PublicShareDeclaration{}, err
		}
	}
	objects := make([]contract.PublicShareObject, 0, len(declaration.Objects))
	for _, object := range declaration.Objects {
		publicID := object.PublicID
		if publicID == "" {
			publicID = strings.ReplaceAll(uuid.NewString(), "-", "")
		}
		objects = append(objects, contract.PublicShareObject{PublicID: publicID, RepoPath: object.RepoPath, DisplayName: object.DisplayName, Size: object.Size})
	}
	return contract.PublicShareDeclaration{RepoID: declaration.RepoID, SourceRoot: declaration.SourceRoot, Slug: declaration.Slug, Recipients: append([]string(nil), declaration.Recipients...), PasswordHash: passwordHash, DoNotFollow: declaration.DoNotFollow, Objects: objects}, nil
}

type reservationAdapter struct{ client reservationClient }

func (adapter reservationAdapter) ListReservations(ctx context.Context, serverID string) ([]app.Reservation, error) {
	result, err := adapter.client.RepoReservationList(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("daemon returned an empty reservation list")
	}
	reservations := make([]app.Reservation, len(result.Reservations))
	for i, reservation := range result.Reservations {
		reservations[i] = app.Reservation{RepoID: reservation.RepoID, WorkingCopy: reservation.WorkingCopy, Path: reservation.Path, Token: reservation.Token, OwnerLabel: reservation.OwnerLabel, CreatedAt: reservation.CreatedAt, CanRelease: reservation.CanRelease, LocalChanges: reservation.LocalChanges, ActivePassport: reservation.ActivePassport}
	}
	return reservations, nil
}

func (adapter reservationAdapter) ReleaseReservation(ctx context.Context, request app.ReservationReleaseRequest) error {
	return adapter.client.RepoReservationRelease(ctx, contract.RepoReservationReleasePayload{ServerID: request.ServerID, RepoID: request.RepoID, Path: request.Path, ExpectedToken: request.ExpectedToken, ConfirmRisk: request.ConfirmRisk})
}

func (adapter repositoryDetachAdapter) DetachRepository(ctx context.Context, serverID, repoID string, deleteRepository bool) error {
	operationCtx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	var (
		result *contract.RepoLifecycleResult
		err    error
	)
	if deleteRepository {
		result, err = adapter.client.RepoDelete(operationCtx, serverID, repoID)
	} else {
		result, err = adapter.client.RepoDetach(operationCtx, serverID, repoID)
	}
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("daemon returned an empty repository detach result")
	}
	expectedState := "detached"
	if deleteRepository {
		expectedState = "deleted"
	}
	if result.State != expectedState {
		if result.LastError != "" {
			return errors.New(result.LastError)
		}
		return errors.New("daemon did not complete repository detach")
	}
	return nil
}

type systemLifecycleClient interface {
	SystemRestart(context.Context) (*contract.SystemLifecycleResult, error)
	SystemShutdown(context.Context) (*contract.SystemLifecycleResult, error)
}

type stackLifecycleAdapter struct{ client systemLifecycleClient }

func (adapter stackLifecycleAdapter) RestartFileES(ctx context.Context) error {
	result, err := adapter.client.SystemRestart(ctx)
	return validateSystemLifecycleResult(result, "restart", err)
}

func (adapter stackLifecycleAdapter) ShutdownFileES(ctx context.Context) error {
	result, err := adapter.client.SystemShutdown(ctx)
	return validateSystemLifecycleResult(result, "shutdown", err)
}

func validateSystemLifecycleResult(result *contract.SystemLifecycleResult, expected string, err error) error {
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("daemon returned an empty lifecycle result")
	}
	if result.Action != expected {
		return errors.New("daemon returned an unexpected lifecycle action")
	}
	return nil
}

type mobilePairingClient interface {
	MobilePairingBegin(context.Context, string) (*contract.MobilePairingBeginResult, error)
}

// mobilePairingAdapter fetches a pairing token from the daemon over IPC,
// then hands it to the separate filees-pair-gui helper process over its
// stdin - a single JSON line, written once, stdin closed immediately after
// (os/exec already gives a private, unnamed pipe invisible to argv/env/ps;
// see pkg/deploy/tunnel_linux.go for the analogous discipline used where a
// named FIFO is unavoidable). The helper owns the rest of the flow (PIN
// gate, QR rendering) entirely on its own - this adapter does not wait for
// it to exit.
type mobilePairingAdapter struct {
	client     mobilePairingClient
	helperPath func() (string, error)
}

// defaultPairingHelperPath resolves the helper binary next to the running
// filees-gui executable, so it works identically whether installed via
// install-user.sh or run from a dist/ staging tree during development.
func defaultPairingHelperPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	name := "filees-pair-gui"
	if filepath.Ext(exe) == ".exe" {
		name += ".exe"
	}
	return filepath.Join(filepath.Dir(exe), name), nil
}

func (adapter mobilePairingAdapter) Launch(ctx context.Context, serverID string) error {
	result, err := adapter.client.MobilePairingBegin(ctx, serverID)
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("daemon returned an empty mobile pairing result")
	}
	helperPathFunc := adapter.helperPath
	if helperPathFunc == nil {
		helperPathFunc = defaultPairingHelperPath
	}
	helperPath, err := helperPathFunc()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		Address       string `json:"address"`
		HostPublicKey string `json:"host_public_key"`
		Token         string `json:"token"`
		ExpiresAt     string `json:"expires_at"`
	}{Address: result.Address, HostPublicKey: result.HostPublicKey, Token: result.Token, ExpiresAt: result.ExpiresAt})
	if err != nil {
		return err
	}
	defer clear(payload)
	cmd := exec.Command(helperPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if _, err := stdin.Write(payload); err != nil {
		stdin.Close()
		_ = cmd.Wait()
		return err
	}
	if err := stdin.Close(); err != nil {
		_ = cmd.Wait()
		return err
	}
	// Reap the helper process in the background - Launch does not wait for
	// the helper's own UI lifetime (PIN entry, QR display, timeout), only
	// for the handoff to succeed.
	go func() { _ = cmd.Wait() }()
	return nil
}

type updateAdapter struct{ client updateClient }

func (adapter updateAdapter) UpdatePlan(ctx context.Context) (*actions.UpdatePlan, error) {
	result, err := adapter.client.UpdatePlan(ctx)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("daemon returned an empty update plan")
	}
	plan := &actions.UpdatePlan{
		CurrentVersion: result.CurrentVersion, AvailableVersion: result.AvailableVersion,
		ReleaseID: result.ReleaseID, RestartRequired: result.RestartRequired,
		Changes: make([]actions.UpdateChange, len(result.Changes)),
	}
	for index, change := range result.Changes {
		plan.Changes[index] = actions.UpdateChange{Action: change.Action, Path: change.Path, Detail: change.Detail}
	}
	return plan, nil
}

func (adapter updateAdapter) UpdateApply(ctx context.Context) (*actions.UpdateResult, error) {
	result, err := adapter.client.UpdateApply(ctx)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("daemon returned an empty update result")
	}
	return &actions.UpdateResult{InstalledVersion: result.InstalledVersion, RestartRequired: result.RestartRequired}, nil
}

func (s *viewStore) load() app.ViewModel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.vm
}

func (s *viewStore) store(vm app.ViewModel) {
	s.mu.Lock()
	s.vm = vm
	s.mu.Unlock()
}

// run owns the GUI process lifecycle. The native tray remains on the calling
// goroutine; app and intent loops are started only after the tray is ready.
func run(parent context.Context, deps dependencies) error {
	if deps.tray == nil || deps.platform == nil || deps.client == nil {
		return errors.New("incomplete GUI dependencies")
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	intents := make(chan tray.Intent, 64)
	views := &viewStore{vm: app.ViewModel{Icon: app.IconDisconnected}}
	var notificationPolicy notifications.Policy
	notificationQueue := make(chan platform.Notification, 64)
	renderer := tray.NewRenderer(deps.tray, deps.icons, intents, func(intent tray.Intent) {
		log.Printf("filees-gui: dropped tray intent kind=%s repo_id=%s", intent.Kind, intent.RepoID)
	})
	guiApp := app.New(app.Config{
		Client: deps.client,
		OnChange: func(vm app.ViewModel) {
			views.store(vm)
			renderer.Render(tray.BuildMenu(vm))
			for _, notification := range notificationPolicy.Observe(vm) {
				select {
				case notificationQueue <- notification:
				case <-ctx.Done():
					return
				}
			}
		},
	})
	restartRequested := make(chan struct{}, 1)
	var updater actions.Updater
	if candidate, ok := deps.client.(updateClient); ok {
		updater = updateAdapter{client: candidate}
	}
	var repositoryCreator actions.RepositoryCreator
	if candidate, ok := deps.client.(repositoryCreateClient); ok {
		repositoryCreator = repositoryCreateAdapter{client: candidate}
	}
	var repositoryAttacher actions.RepositoryAttacher
	if candidate, ok := deps.client.(repositoryAttachClient); ok {
		repositoryAttacher = repositoryAttachAdapter{client: candidate}
	}
	var repositoryLocator actions.RepositoryLocator
	if candidate, ok := deps.client.(repositoryLocateClient); ok {
		repositoryLocator = repositoryLocateAdapter{client: candidate}
	}
	var repositoryDetacher actions.RepositoryDetacher
	if candidate, ok := deps.client.(repositoryDetachClient); ok {
		repositoryDetacher = repositoryDetachAdapter{client: candidate}
	}
	var repositoryDumpLoader actions.RepositoryDumpLoader
	if candidate, ok := deps.client.(repositoryDumpLoadClient); ok {
		repositoryDumpLoader = repositoryDumpLoadAdapter{client: candidate}
	}
	var serverDetacher actions.ServerDetacher
	if candidate, ok := deps.client.(serverDetachClient); ok {
		serverDetacher = serverDetachAdapter{client: candidate}
	}
	var realmRemover actions.RealmRemover
	var recoveryDownloader actions.RecoveryDownloader
	if candidate, ok := deps.client.(realmRemovalClient); ok {
		adapter := realmRemovalAdapter{client: candidate}
		realmRemover, recoveryDownloader = adapter, adapter
	}
	var reservations actions.ReservationManager
	if candidate, ok := deps.client.(reservationClient); ok {
		reservations = reservationAdapter{client: candidate}
	}
	var realmAliases actions.RealmAliasManager
	if candidate, ok := deps.client.(realmAliasClient); ok {
		realmAliases = realmAliasAdapter{client: candidate}
	}
	var realmGrants actions.RealmGrantManager
	if candidate, ok := deps.client.(realmGrantClient); ok {
		realmGrants = realmGrantAdapter{client: candidate}
	}
	var realmBranding actions.RealmBrandingManager
	if candidate, ok := deps.client.(realmBrandingClient); ok {
		realmBranding = realmBrandingAdapter{client: candidate}
	}
	var sessionTimeouts actions.SessionTimeoutManager
	if candidate, ok := deps.client.(sessionTimeoutClient); ok {
		sessionTimeouts = sessionTimeoutAdapter{client: candidate}
	}
	var publicShares actions.PublicShareManager
	if candidate, ok := deps.client.(publicShareClient); ok {
		publicShares = publicShareAdapter{client: candidate}
	}
	var stack actions.StackLifecycle
	if candidate, ok := deps.client.(systemLifecycleClient); ok {
		stack = stackLifecycleAdapter{client: candidate}
	}
	var mobilePairer actions.MobilePairingLauncher
	if candidate, ok := deps.client.(mobilePairingClient); ok {
		mobilePairer = mobilePairingAdapter{client: candidate}
	}
	controller := actions.New(actions.Config{
		Intents:              intents,
		ViewModel:            views.load,
		Opener:               deps.platform,
		Picker:               deps.platform,
		FolderPicker:         deps.platform,
		Prompter:             deps.platform,
		RepositoryCreator:    repositoryCreator,
		RepositoryAttacher:   repositoryAttacher,
		RepositoryLocator:    repositoryLocator,
		RepositoryDetacher:   repositoryDetacher,
		RepositoryDumpLoader: repositoryDumpLoader,
		ServerDetacher:       serverDetacher,
		RealmRemover:         realmRemover,
		RecoveryDownloader:   recoveryDownloader,
		MobilePairer:         mobilePairer,
		PinStore:             deps.pinStore,
		Activator:            deps.activator,
		Updater:              updater,
		Stack:                stack,
		Notifier:             deps.platform,
		Locker:               deps.client,
		Reservations:         reservations,
		RealmAliases:         realmAliases,
		RealmGrants:          realmGrants,
		RealmBranding:        realmBranding,
		SessionTimeouts:      sessionTimeouts,
		PublicShares:         publicShares,
		Shouts:               newShoutAdapter(deps.client),
		Notices:              newShoutAdapter(deps.client),
		ReservationBrowser:   deps.platform,
		SettingsBrowser:      deps.platform,
		JournalBrowser:       deps.platform,
		RealmGrantBrowser:    deps.platform,
		PublicShareBrowser:   deps.platform,
		ConsentPrompter:      deps.platform,
		Progress:             progressPresenter(deps.platform),
		Reconnect:            guiApp.Reconnect,
		Refresh:              guiApp.Refresh,
		PrepareRestart:       notificationPolicy.SuppressConnectionTransitions,
		AbortRestart:         notificationPolicy.RestoreConnectionTransitions,
		Restart: func() {
			select {
			case restartRequested <- struct{}{}:
			default:
			}
			cancel()
		},
		Shutdown: cancel,
	})

	var wg sync.WaitGroup
	var startOnce sync.Once
	onReady := func() {
		startOnce.Do(func() {
			renderer.Render(tray.BuildMenu(views.load()))
			wg.Add(3)
			go func() {
				defer wg.Done()
				guiApp.Run(ctx)
			}()
			go func() {
				defer wg.Done()
				controller.Run(ctx)
			}()
			go func() {
				defer wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					case notification := <-notificationQueue:
						if err := deps.platform.Notify(ctx, notification); err != nil && ctx.Err() == nil {
							log.Printf("filees-gui: notification delivery failed: %v", err)
						}
					}
				}
			}()
		})
	}
	onExit := cancel

	trayStopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			deps.tray.Quit()
		case <-trayStopped:
		}
	}()
	deps.tray.Run(onReady, onExit)
	close(trayStopped)
	cancel()
	renderer.Close()
	wg.Wait()
	select {
	case <-restartRequested:
		return errGUIRestartRequested
	default:
	}
	return nil
}

// SetEditingPolicy translates the GUI's boolean intent into the wire
// vocabulary, and reports back what the server actually stored rather than
// what was asked for - the server is the authority on its own repositories.
func (adapter realmGrantAdapter) SetEditingPolicy(ctx context.Context, serverID, repoID string, lockRequired bool) (bool, error) {
	policy := contract.EditingFree
	if lockRequired {
		policy = contract.EditingLockRequired
	}
	result, err := adapter.client.RepoSetEditingPolicy(ctx, contract.RepoSetEditingPolicyPayload{ServerID: serverID, RepoID: repoID, Policy: policy})
	if err != nil {
		return false, err
	}
	if result == nil || result.RepoID != repoID {
		return false, errors.New("daemon returned an invalid editing policy result")
	}
	return result.Policy == contract.EditingLockRequired, nil
}

// progressPresenter narrows the platform backend to the optional progress
// surface. ProgressPresenter is deliberately outside platform.Backend, so a
// backend that does not implement it simply yields nil and the controller
// skips the window (see actions.Controller.showProgress).
func progressPresenter(backend platform.Backend) platform.ProgressPresenter {
	if presenter, ok := backend.(platform.ProgressPresenter); ok {
		return presenter
	}
	return nil
}

type shoutIPC interface {
	RepoPublish(context.Context, string, string) (*contract.RepoPublishResult, error)
	NoticeAck(context.Context, string) error
}

type shoutAdapter struct{ client shoutIPC }

func newShoutAdapter(client app.DaemonClient) shoutAdapter {
	ipc, _ := client.(shoutIPC)
	return shoutAdapter{client: ipc}
}

func (a shoutAdapter) Publish(ctx context.Context, repoID, comment string) (int64, error) {
	if a.client == nil {
		return 0, errors.New("shout publish is unavailable")
	}
	result, err := a.client.RepoPublish(ctx, repoID, comment)
	if err != nil {
		return 0, err
	}
	if result == nil {
		return 0, errors.New("daemon returned an empty publish result")
	}
	return result.Revision, nil
}

func (a shoutAdapter) AckNotice(ctx context.Context, noticeID string) error {
	if a.client == nil {
		return errors.New("shout ack is unavailable")
	}
	return a.client.NoticeAck(ctx, noticeID)
}
