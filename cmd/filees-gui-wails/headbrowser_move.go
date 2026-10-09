package main

import (
	"context"
	"errors"
	"filees/internal/gui/platform"
	contract "filees/pkg/contract/v1"
	"fmt"
)

// Uses existing confirmation/progress presentation. JS supplies paths only;
// daemon preview binds the exact objects and every execution checks it again.
func (service *HeadBrowserService) MoveFolder(source, destination string) (contract.RepoAnchorMoveResult, error) {
	finish, err := beginDirectDisplayCall(service.beginDisplayCall)
	if err != nil {
		return contract.RepoAnchorMoveResult{}, err
	}
	defer finish()
	repo, err := service.focused()
	if err != nil {
		return contract.RepoAnchorMoveResult{}, err
	}
	daemon, ok := service.daemon.(headBrowserMoveDaemon)
	if !ok || !repo.CanMove || service.confirm == nil || service.showProgress == nil {
		return contract.RepoAnchorMoveResult{}, errors.New(service.render("HEAD-2020", "head.folder_unavailable", nil))
	}
	focus := service.Context()
	ctx, cancel := context.WithTimeout(context.Background(), headBrowserWriteTimeout)
	defer cancel()
	p := contract.RepoAnchorMoveSelection{ServerID: repo.ServerID, RepoID: repo.RepoID, Source: source, Destination: destination}
	plan, err := headBrowserCall(service, headBrowserWriteTimeout, "HEAD-2025", "head.move_refused", func(ctx context.Context) (*contract.RepoAnchorMovePlan, error) { return daemon.AnchorMovePlan(ctx, p) })
	if err != nil {
		return contract.RepoAnchorMoveResult{}, err
	}
	if plan.Source != source || plan.Destination != destination || !folderDigest(plan.Fingerprint) {
		return contract.RepoAnchorMoveResult{}, errors.New(service.render("HEAD-2025", "head.move_refused", nil))
	}
	accepted, err := service.confirm(ctx, platform.ConfirmRequest{PresentationKey: "confirm.anchorMove", PresentationArgs: map[string]string{"source": source, "destination": destination}, Title: "Przenieść folder?", Text: fmt.Sprintf("%s → %s\nZmiana zostanie opublikowana na serwerze bez pobierania zdalnych plików.", source, destination), ConfirmText: "Przenieś", CancelText: "Anuluj"})
	if err != nil {
		return contract.RepoAnchorMoveResult{}, err
	}
	if !accepted {
		return contract.RepoAnchorMoveResult{State: "cancelled"}, nil
	}
	current, err := service.focused()
	if err != nil {
		return contract.RepoAnchorMoveResult{}, err
	}
	if service.Context() != focus || !current.CanMove || current.LocalPath != repo.LocalPath {
		return contract.RepoAnchorMoveResult{}, errors.New(service.render("HEAD-2001", "head.forbidden", nil))
	}
	closeProgress, err := service.showProgress(ctx, platform.ProgressRequest{PresentationKey: "progress.anchorMove", PresentationArgs: map[string]string{"source": source, "destination": destination}, Title: "Przenoszenie folderu…", Text: "Poczekaj na potwierdzenie zmiany na serwerze i w tym folderze."})
	if err != nil {
		return contract.RepoAnchorMoveResult{}, err
	}
	defer closeProgress()
	result, err := headBrowserCall(service, headBrowserWriteTimeout, "HEAD-2025", "head.move_refused", func(ctx context.Context) (*contract.RepoAnchorMoveResult, error) {
		return daemon.AnchorMove(ctx, contract.RepoAnchorMovePayload{RepoAnchorMoveSelection: p, Fingerprint: plan.Fingerprint})
	})
	if err == nil && result.State != "moved" {
		return result, errors.New(service.render("HEAD-2025", "head.move_refused", nil))
	}
	return result, err
}

func (service *HeadBrowserService) RecoverMove() (contract.RepoAnchorMoveResult, error) {
	finish, err := beginDirectDisplayCall(service.beginDisplayCall)
	if err != nil {
		return contract.RepoAnchorMoveResult{}, err
	}
	defer finish()
	repo, err := service.focused()
	if err != nil {
		return contract.RepoAnchorMoveResult{}, err
	}
	daemon, ok := service.daemon.(headBrowserMoveDaemon)
	if !ok || !repo.CanMove || service.showProgress == nil {
		return contract.RepoAnchorMoveResult{}, errors.New(service.render("HEAD-2020", "head.folder_unavailable", nil))
	}
	ctx, cancel := context.WithTimeout(context.Background(), headBrowserWriteTimeout)
	defer cancel()
	closeProgress, err := service.showProgress(ctx, platform.ProgressRequest{PresentationKey: "progress.anchorMoveRecovery", Title: "Sprawdzanie przeniesienia…", Text: "FileES sprawdza poprzedni wynik. Nie wysyła ponownie przeniesienia."})
	if err != nil {
		return contract.RepoAnchorMoveResult{}, err
	}
	defer closeProgress()
	return headBrowserCall(service, headBrowserWriteTimeout, "HEAD-2019", "head.folder_recovery_required", func(ctx context.Context) (*contract.RepoAnchorMoveResult, error) {
		return daemon.AnchorMoveRecover(ctx, contract.RepoAnchorMoveSelection{ServerID: repo.ServerID, RepoID: repo.RepoID})
	})
}
