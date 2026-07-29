package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"filees/pkg/clientprofile"
	contract "filees/pkg/contract/v1"
	control "filees/pkg/control/v1"
	"filees/pkg/controlclient"
	"filees/pkg/localrepo"
	"filees/pkg/recoverykit"
	"filees/pkg/repoworker"
	"github.com/google/uuid"
)

type realmRemovalClientService struct {
	local       realmLocalStore
	provisioner *daemonProvisioner
	profileRoot string
	registry    recoverykit.Registry
}

func (s realmRemovalClientService) List(ctx context.Context) ([]contract.RecoveryStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := s.registry.List(time.Now())
	if err != nil {
		return nil, err
	}
	result := make([]contract.RecoveryStatus, 0, len(entries))
	for _, entry := range entries {
		result = append(result, contract.RecoveryStatus{
			OperationID: entry.OperationID, ServerID: entry.ServerID, ServerName: entry.ServerName,
			KitPath: entry.KitPath, AdminContact: entry.AdminContact, ArchiveCount: entry.ArchiveCount,
			DownloadUntil: entry.DownloadUntil.Format(time.RFC3339Nano), AdminGraceUntil: entry.AdminGraceUntil.Format(time.RFC3339Nano),
		})
	}
	return result, nil
}

func (s realmRemovalClientService) Download(ctx context.Context, payload contract.RecoveryDownloadPayload) (contract.RecoveryDownloadResult, error) {
	now := time.Now().UTC()
	entry, err := s.registry.Find(payload.OperationID, now)
	if err != nil {
		return contract.RecoveryDownloadResult{}, err
	}
	kit, err := recoverykit.Load(entry.KitPath, now)
	if err != nil {
		return contract.RecoveryDownloadResult{}, err
	}
	paths, err := recoverykit.Download(ctx, kit, filepath.Clean(payload.OutputRoot), now)
	if err != nil {
		return contract.RecoveryDownloadResult{}, err
	}
	return contract.RecoveryDownloadResult{OperationID: payload.OperationID, Paths: paths}, nil
}

func (s realmRemovalClientService) Begin(ctx context.Context, serverID, realmID string, payload contract.RealmRemoveBeginPayload) (contract.RealmRemoveBeginResult, error) {
	profile, ok := s.provisioner.Profile(serverID)
	if !ok {
		return contract.RealmRemoveBeginResult{}, errors.New("activated client profile is unavailable")
	}
	knownHostRaw, err := os.ReadFile(profile.KnownHosts)
	if err != nil || len(knownHostRaw) > 16<<10 {
		return contract.RealmRemoveBeginResult{}, errors.New("pinned server host key cannot be read")
	}
	operationID := uuid.NewString()
	kit, publicKey, err := recoverykit.CreateDraft(recoveryProfileAddress(profile), strings.TrimSpace(string(knownHostRaw)), operationID, realmID)
	if err != nil {
		return contract.RealmRemoveBeginResult{}, err
	}
	kitPath := filepath.Join(filepath.Clean(payload.RecoveryDirectory), "filees-recovery-"+serverID+"-"+operationID+".fkr")
	if err := recoverykit.Store(kitPath, kit); err != nil {
		return contract.RealmRemoveBeginResult{}, fmt.Errorf("store pending recovery kit: %w", err)
	}
	transport, err := realmRemovalTransport(profile)
	if err != nil {
		return contract.RealmRemoveBeginResult{}, err
	}
	ticket, err := control.NewTicket(operationID, uuid.NewSHA1(uuid.NameSpaceOID, []byte(operationID+":realm-remove-request")).String(), control.TicketRealmRemoveRequest, profile.ClientID, control.RealmRemoveRequestPayload{
		NotificationEmail: payload.NotificationEmail, ErasureRequested: payload.ErasureRequested,
		RecoveryPublicKey: strings.TrimSpace(publicKey),
	}, time.Now())
	if err != nil {
		return contract.RealmRemoveBeginResult{}, err
	}
	response, err := transport.Exchange(ctx, ticket)
	if err != nil {
		return contract.RealmRemoveBeginResult{}, err
	}
	if response.Status != control.ResultOK {
		return contract.RealmRemoveBeginResult{}, controlResultError(response)
	}
	var result control.RealmRemoveRequestResult
	if err := control.DecodeResultPayload(response.Result, &result); err != nil {
		return contract.RealmRemoveBeginResult{}, err
	}
	return contract.RealmRemoveBeginResult{
		ServerID: serverID, OperationID: operationID, RecoveryKitPath: kitPath, ExpiresAt: result.ExpiresAt,
		ActiveClientCount: result.ActiveClientCount, OwnedRepositoryCount: result.OwnedRepositoryCount, ForeignGrantCount: result.ForeignGrantCount,
		AdminContact: result.AdminContact,
	}, nil
}

func (s realmRemovalClientService) Confirm(ctx context.Context, payload contract.RealmRemoveConfirmPayload) (contract.RealmRemoveConfirmResult, error) {
	profile, ok := s.provisioner.Profile(payload.ServerID)
	if !ok {
		return contract.RealmRemoveConfirmResult{}, errors.New("activated client profile is unavailable")
	}
	kitPath := filepath.Clean(payload.RecoveryKitPath)
	kit, err := recoverykit.LoadDraft(kitPath)
	if err != nil {
		return contract.RealmRemoveConfirmResult{}, fmt.Errorf("load pending recovery kit: %w", err)
	}
	if kit.OperationID != payload.OperationID || kit.ServerAddress != recoveryProfileAddress(profile) {
		return contract.RealmRemoveConfirmResult{}, errors.New("pending recovery kit does not match removal operation")
	}
	if err := detachRealmWorkingCopies(ctx, s.local, s.provisioner, payload.ServerID); err != nil {
		return contract.RealmRemoveConfirmResult{}, err
	}
	transport, err := realmRemovalTransport(profile)
	if err != nil {
		return contract.RealmRemoveConfirmResult{}, err
	}
	ticket, err := control.NewTicket(payload.OperationID, uuid.NewSHA1(uuid.NameSpaceOID, []byte(payload.OperationID+":realm-remove-confirm")).String(), control.TicketRealmRemoveConfirm, profile.ClientID, control.RealmRemoveConfirmPayload{OTP: string(payload.OTP)}, time.Now())
	if err != nil {
		return contract.RealmRemoveConfirmResult{}, err
	}
	response, err := transport.Exchange(ctx, ticket)
	if err != nil {
		return contract.RealmRemoveConfirmResult{}, err
	}
	if response.Status != control.ResultOK {
		return contract.RealmRemoveConfirmResult{}, controlResultError(response)
	}
	var result control.RealmRemoveConfirmResult
	if err := control.DecodeResultPayload(response.Result, &result); err != nil {
		return contract.RealmRemoveConfirmResult{}, err
	}
	if result.ErasureRequested && result.ErasureMaxDays <= 0 {
		return contract.RealmRemoveConfirmResult{}, errors.New("server returned an invalid data-erasure completion window")
	}
	manifest := recoveryManifestFromControl(result.Manifest)
	finalKit, err := recoverykit.Finalize(kit, manifest)
	if err != nil {
		return contract.RealmRemoveConfirmResult{}, err
	}
	if len(manifest.Archives) == 0 {
		if err := os.Remove(kitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return contract.RealmRemoveConfirmResult{}, fmt.Errorf("remove unused recovery kit: %w", err)
		}
		kitPath = ""
	} else {
		if err := recoverykit.Store(kitPath, finalKit); err != nil {
			return contract.RealmRemoveConfirmResult{}, fmt.Errorf("finalize recovery kit: %w", err)
		}
		if err := s.registry.Put(recoverykit.RegistryEntry{
			Schema: recoverykit.RegistrySchema, OperationID: payload.OperationID,
			ServerID: payload.ServerID, ServerName: profile.DisplayName, KitPath: kitPath,
			ArchiveCount: len(manifest.Archives), DownloadUntil: manifest.DownloadUntil,
			AdminGraceUntil: manifest.AdminGraceUntil, AdminContact: result.AdminContact,
		}); err != nil {
			return contract.RealmRemoveConfirmResult{}, fmt.Errorf("register recovery capability: %w", err)
		}
	}
	if err := clientprofile.Remove(s.profileRoot, payload.ServerID); err != nil {
		return contract.RealmRemoveConfirmResult{}, fmt.Errorf("remove revoked local profile: %w", err)
	}
	s.provisioner.RemoveProfile(payload.ServerID)
	confirmResult := contract.RealmRemoveConfirmResult{
		ServerID: payload.ServerID, OperationID: payload.OperationID, RecoveryKitPath: kitPath,
		ArchiveCount:     len(manifest.Archives),
		ErasureRequested: result.ErasureRequested, ErasureMaxDays: result.ErasureMaxDays,
	}
	if len(manifest.Archives) > 0 {
		confirmResult.DownloadUntil = manifest.DownloadUntil.Format(time.RFC3339Nano)
		confirmResult.AdminGraceUntil = manifest.AdminGraceUntil.Format(time.RFC3339Nano)
	}
	return confirmResult, nil
}

func realmRemovalTransport(profile clientprofile.Profile) (*controlclient.Client, error) {
	return controlclient.New(controlclient.Config{
		Address: profile.Address, Port: profile.SSHPort, IdentityFile: profile.IdentityFile,
		KnownHosts: profile.KnownHosts, Timeout: 45 * time.Minute,
	})
}

func recoveryProfileAddress(profile clientprofile.Profile) string {
	if _, _, err := net.SplitHostPort(profile.Address); err == nil || profile.SSHPort == 22 {
		return profile.Address
	}
	return net.JoinHostPort(strings.Trim(profile.Address, "[]"), strconv.Itoa(profile.SSHPort))
}

func controlResultError(result control.Result) error {
	if result.Error == nil {
		return errors.New("server rejected realm removal")
	}
	return fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message)
}

func recoveryManifestFromControl(manifest control.RealmRecoveryManifest) repoworker.RecoveryManifest {
	archives := make([]repoworker.RecoveryArchive, 0, len(manifest.Archives))
	for _, archive := range manifest.Archives {
		archives = append(archives, repoworker.RecoveryArchive{ArchiveID: archive.ArchiveID, RepoID: archive.RepoID, SHA256: archive.SHA256, Size: archive.Size})
	}
	return repoworker.RecoveryManifest{
		Schema: manifest.Schema, OperationID: manifest.OperationID, RealmID: manifest.RealmID,
		Archives: archives, DownloadUntil: manifest.DownloadUntil, AdminGraceUntil: manifest.AdminGraceUntil, CreatedAt: manifest.CreatedAt,
	}
}

type realmLocalStore interface {
	List() []localrepo.Record
	BeginDetach(string, string, bool) (localrepo.Record, error)
}

func detachRealmWorkingCopies(ctx context.Context, local realmLocalStore, provisioner *daemonProvisioner, serverID string) error {
	for _, record := range local.List() {
		if record.ServerID != serverID {
			continue
		}
		switch record.State {
		case localrepo.StateAttached:
			started, err := local.BeginDetach(serverID, record.RepoID, false)
			if err != nil {
				return fmt.Errorf("start detach for %s: %w", record.LocalPath, err)
			}
			if _, err := provisioner.Detach(ctx, started.OperationID); err != nil {
				return fmt.Errorf("detach %s: %w", record.LocalPath, err)
			}
		case localrepo.StateDetached, localrepo.StateDeleted:
		default:
			return fmt.Errorf("repository %s has unfinished lifecycle state %q", record.LocalPath, record.State)
		}
	}
	return nil
}
