package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"filees/internal/gui/actions"
	guiapp "filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
	"filees/public-shares/gate"
	"github.com/google/uuid"
)

type actionRunner interface {
	Run(context.Context)
}

// configureActions deliberately wires only the actions exposed by the first
// Wails UX slice.  The controller remains the authority on eligibility; the
// WebView projection merely avoids offering an obviously unavailable button.
func configureActions(service *GUIService, locker actions.LockUnlocker, reservations actions.ReservationManager, stack actions.StackLifecycle, settings platform.SettingsBrowser, sessionTimeouts actions.SessionTimeoutManager, publicShareBrowser platform.PublicShareBrowser, publicShares actions.PublicShareManager, repositoryAttacher actions.RepositoryAttacher, repositoryDetacher actions.RepositoryDetacher, recoveryDownloader actions.RecoveryDownloader, backend platform.Backend, restart, shutdown func()) actionRunner {
	if backend == nil {
		return nil
	}
	intents := make(chan tray.Intent, 32)
	service.attachActions(intents)
	return actions.New(actions.Config{
		Intents:            intents,
		ViewModel:          service.viewModel,
		Opener:             backend,
		Picker:             backend,
		FolderPicker:       backend,
		Prompter:           backend,
		Notifier:           actionNotifier{service: service},
		Locker:             locker,
		Reservations:       reservations,
		SettingsBrowser:    settings,
		SessionTimeouts:    sessionTimeouts,
		PublicShareBrowser: publicShareBrowser,
		PublicShares:       publicShares,
		RepositoryAttacher: repositoryAttacher,
		RepositoryDetacher: repositoryDetacher,
		RecoveryDownloader: recoveryDownloader,
		ActionLifecycle:    service.runner,
		Stack:              stack,
		Reconnect:          service.runner.Reconnect,
		Refresh:            service.runner.Refresh,
		Restart:            restart,
		Shutdown:           shutdown,
	})
}

type recoveryDownloadClient interface {
	RecoveryDownload(context.Context, contract.RecoveryDownloadPayload) (*contract.RecoveryDownloadResult, error)
}

type recoveryDownloadAdapter struct{ client recoveryDownloadClient }

func (adapter recoveryDownloadAdapter) DownloadRecovery(ctx context.Context, operationID, outputRoot string) ([]string, error) {
	result, err := adapter.client.RecoveryDownload(ctx, contract.RecoveryDownloadPayload{OperationID: operationID, OutputRoot: outputRoot})
	if err != nil {
		return nil, err
	}
	if result == nil || result.OperationID != operationID {
		return nil, errors.New("daemon returned an invalid recovery download result")
	}
	return result.Paths, nil
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
	if result == nil || result.Minutes != minutes {
		return 0, errors.New("daemon returned an invalid session timeout")
	}
	return result.Minutes, nil
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

type repositoryDetachClient interface {
	RepoDetach(context.Context, string, string) (*contract.RepoLifecycleResult, error)
	RepoDelete(context.Context, string, string) (*contract.RepoLifecycleResult, error)
}

type repositoryAttachClient interface {
	RepoAttachIntent(context.Context, contract.RepoAttachIntentPayload) (*contract.RepoLifecycleResult, error)
	RepoAttachApprove(context.Context, contract.RepoAttachApprovePayload) (*contract.RepoLifecycleResult, error)
	RepoLifecycleStatus(context.Context, string) (*contract.RepoLifecycleResult, error)
}

type repositoryAttachAdapter struct{ client repositoryAttachClient }

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

type repositoryDetachAdapter struct{ client repositoryDetachClient }

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
	if deleteRepository {
		if result.ServerDeleteCompleted || result.State == "deleted" {
			return nil
		}
		if result.LastError != "" {
			return errors.New(result.LastError)
		}
		return errors.New("daemon did not confirm server repository deletion")
	}
	if result.State != "detached" {
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

type stackLifecycleAdapter struct {
	client systemLifecycleClient
}

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

type reservationClient interface {
	RepoReservationList(context.Context, string) (*contract.RepoReservationListResult, error)
	RepoReservationRelease(context.Context, contract.RepoReservationReleasePayload) error
}

type reservationAdapter struct {
	client reservationClient
}

func (adapter reservationAdapter) ListReservations(ctx context.Context, serverID string) ([]guiapp.Reservation, error) {
	result, err := adapter.client.RepoReservationList(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("daemon returned an empty reservation list")
	}
	reservations := make([]guiapp.Reservation, len(result.Reservations))
	for i, reservation := range result.Reservations {
		reservations[i] = guiapp.Reservation{
			ServerID: serverID, RepoID: reservation.RepoID, WorkingCopy: reservation.WorkingCopy,
			Path: reservation.Path, Token: reservation.Token, OwnerLabel: reservation.OwnerLabel,
			CreatedAt: reservation.CreatedAt, CanRelease: reservation.CanRelease,
			LocalChanges: reservation.LocalChanges, ActivePassport: reservation.ActivePassport,
		}
	}
	return reservations, nil
}

func (adapter reservationAdapter) ReleaseReservation(ctx context.Context, request guiapp.ReservationReleaseRequest) error {
	return adapter.client.RepoReservationRelease(ctx, contract.RepoReservationReleasePayload{
		ServerID: request.ServerID, RepoID: request.RepoID, Path: request.Path,
		ExpectedToken: request.ExpectedToken, ConfirmRisk: request.ConfirmRisk,
	})
}

// actionNotifier keeps action feedback in the same Wails surface. It carries
// information only: no callback can turn a toast into a privileged action.
type actionNotifier struct {
	service *GUIService
}

func (notifier actionNotifier) Notify(_ context.Context, notification platform.Notification) error {
	notifier.service.emitActionFeedback(ActionFeedback{
		Level:   string(notification.Urgency),
		Title:   notification.Title,
		Message: notification.Body,
	})
	return nil
}
