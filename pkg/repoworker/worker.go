// Package repoworker executes authenticated repository control requests.
// Identity and authority are supplied by the forced-command session, never by
// the ticket payload.
package repoworker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"filees/pkg/clientview"
	control "filees/pkg/control/v1"
	"github.com/google/uuid"
)

type Session struct {
	ClientID              string
	RealmID               string
	CanCreateRepositories bool
	// Repositories is the authenticated session's own current repository
	// grants, as resolved server-side from its view.json (never client
	// supplied). Used only to let a MOBILE_PAIRING request inherit the
	// initiating desktop's own repository list 1:1 - see mobilePairing.
	Repositories []clientview.Repository
}

func (s Session) Validate() error {
	if strings.TrimSpace(s.ClientID) == "" {
		return errors.New("authenticated client ID is required")
	}
	if _, err := uuid.Parse(s.RealmID); err != nil {
		return errors.New("authenticated realm ID must be UUID")
	}
	return nil
}

type Repository struct{ RepoID, URL string }

// Backend owns the durable server transaction: canonical record, FSFS, owner
// grant, authz and projection. Create must itself be resumable by operation ID.
type Backend interface {
	Create(context.Context, string, string, string) (Repository, error) // operation, realm, name
	Delete(context.Context, string, string, string) (time.Time, error)  // operation, realm, repo ID
}

type ResultStore interface {
	Load(operationID string, typ control.TicketType) (control.Result, bool, error)
	Save(control.Result) error
}

type RepositoryActivator interface {
	Activate(context.Context, string, string) error // repo ID, authenticated realm ID
}

type CapacityChecker interface {
	Check(context.Context, int64) (availableBytes, requiredBytes int64, err error)
}

// MobilePairingRepoGrant is one repository grant carried over from the
// pairing initiator's own session.Repositories into the minted operation,
// so the eventual mobile client's view.json can inherit it 1:1. Access and
// AttachmentPolicy are per-client and cannot be derived from the canonical
// repository record alone, so they must be threaded through from here
// rather than re-derived at publish time.
type MobilePairingRepoGrant struct {
	RepoID, Access, AttachmentPolicy string
}

// MobilePairingMinter mints a mobile pairing token for realmID - always the
// authenticated session's own realm, never a payload-supplied one. repos is
// the initiating session's own current repository list, forwarded so the
// resulting mobile client can inherit it; the minter decides how (or
// whether) to persist it. See pkg/onboarding.Files.CreateMobilePairing,
// which this wraps.
type MobilePairingMinter interface {
	CreatePairing(realmID string, repos []MobilePairingRepoGrant) (token string, expiresAt time.Time, err error)
}

// ClientDetacher revokes the authenticated installation's server credential.
// Its target is the forced-command session client ID, never ticket data.
type ClientDetacher interface {
	DetachClient(context.Context, string) (int64, error)
}

// RealmRemovalService owns the destructive scope. The worker supplies only
// the authenticated session and opaque operation/OTP fields from the ticket;
// implementations derive clients, repositories and grants server-side.
type RealmRemovalService interface {
	Request(context.Context, Session, string, RealmRemovalRequest) (RealmRemovalRecord, error)
	Confirm(context.Context, Session, string, string) (RealmRemovalRecord, RecoveryManifest, error)
}

type Worker struct {
	Backend              Backend
	Activator            RepositoryActivator
	Store                ResultStore
	Capacity             CapacityChecker
	Reservations         ReservationLedger
	MobilePairing        MobilePairingMinter
	Aliases              RealmAliasStore
	ClientDetacher       ClientDetacher
	RealmRemoval         RealmRemovalService
	DumpLoader           DumpLoader
	Grants               RealmGrantAuthority
	RecoveryAdminContact string
	DataErasureMaxDays   int
	Now                  func() time.Time
}

func (w *Worker) Handle(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	if err := session.Validate(); err != nil {
		return control.Result{}, err
	}
	if err := ticket.Validate(); err != nil {
		return control.Result{}, err
	}
	if ticket.ClientID != session.ClientID {
		return control.Result{}, errors.New("ticket client does not match authenticated session")
	}
	if ticket.Type != control.TicketStoragePreflight && ticket.Type != control.TicketCreateRepository && ticket.Type != control.TicketInitialCommit && ticket.Type != control.TicketDeleteRepository && ticket.Type != control.TicketMobilePairing && ticket.Type != control.TicketClaimRealmAlias && ticket.Type != control.TicketResolveOwnerLabels && ticket.Type != control.TicketClientDeactivate && ticket.Type != control.TicketRealmRemoveRequest && ticket.Type != control.TicketRealmRemoveConfirm && ticket.Type != control.TicketLoadRepositoryDump && ticket.Type != control.TicketGrantAccess && ticket.Type != control.TicketRevokeAccess && ticket.Type != control.TicketListGrantRecipients && ticket.Type != control.TicketSetRealmVisibility {
		return control.Result{}, errors.New("unsupported repository worker ticket")
	}
	if ticket.Type == control.TicketDeleteRepository && !session.CanCreateRepositories {
		return w.failure(ticket, "DELETE_REPOSITORY_FORBIDDEN", "authenticated session cannot delete repositories")
	}
	if (ticket.Type == control.TicketStoragePreflight || ticket.Type == control.TicketCreateRepository) && !session.CanCreateRepositories {
		return w.failure(ticket, "CREATE_REPOSITORY_FORBIDDEN", "authenticated session cannot create repositories")
	}
	// Same gate as CREATE_REPOSITORY/DELETE_REPOSITORY: LOAD_REPOSITORY_DUMP
	// replaces a repository's FSFS wholesale, so it requires the same
	// capability as creating one in the first place.
	if ticket.Type == control.TicketLoadRepositoryDump && !session.CanCreateRepositories {
		return w.failure(ticket, "LOAD_REPOSITORY_DUMP_FORBIDDEN", "authenticated session cannot create or load repositories")
	}
	if (ticket.Type == control.TicketGrantAccess || ticket.Type == control.TicketRevokeAccess) && !session.CanCreateRepositories {
		return w.failure(ticket, "REALM_GRANT_FORBIDDEN", "authenticated session cannot manage realm grants")
	}
	if ticket.Type == control.TicketClaimRealmAlias {
		return w.claimRealmAlias(ctx, session, ticket)
	}
	if ticket.Type == control.TicketResolveOwnerLabels {
		return w.resolveOwnerLabels(ctx, ticket)
	}
	if ticket.Type == control.TicketListGrantRecipients {
		return w.listGrantRecipients(ctx, session, ticket)
	}
	if ticket.Type == control.TicketSetRealmVisibility {
		return w.setRealmDirectoryVisibility(ctx, session, ticket)
	}
	if ticket.Type == control.TicketClientDeactivate {
		return w.detachClient(ctx, session, ticket)
	}
	if w.Store == nil {
		return control.Result{}, errors.New("repository result store is required")
	}
	if result, ok, err := w.Store.Load(ticket.OperationID, ticket.Type); err != nil {
		return control.Result{}, err
	} else if ok {
		if result.RequestID != ticket.RequestID {
			return control.Result{}, errors.New("operation already bound to another request")
		}
		return result, nil
	}
	if ticket.Type == control.TicketRealmRemoveRequest {
		return w.requestRealmRemoval(ctx, session, ticket)
	}
	if ticket.Type == control.TicketRealmRemoveConfirm {
		return w.confirmRealmRemoval(ctx, session, ticket)
	}
	if ticket.Type == control.TicketMobilePairing {
		return w.mobilePairing(session, ticket)
	}
	if ticket.Type == control.TicketInitialCommit {
		return w.activate(ctx, session, ticket)
	}
	if ticket.Type == control.TicketDeleteRepository {
		return w.deleteRepository(ctx, session, ticket)
	}
	if ticket.Type == control.TicketLoadRepositoryDump {
		return w.loadRepositoryDump(ctx, session, ticket)
	}
	if ticket.Type == control.TicketGrantAccess {
		return w.grantAccess(ctx, session, ticket)
	}
	if ticket.Type == control.TicketRevokeAccess {
		return w.revokeAccess(ctx, session, ticket)
	}
	if ticket.Type == control.TicketStoragePreflight {
		return w.preflight(ctx, ticket)
	}
	if w.Reservations != nil {
		if err := w.Reservations.Ensure(ctx, ticket.OperationID, w.now()); err != nil {
			return control.NewErrorResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.ErrorBody{Code: "STORAGE_RESERVATION_UNAVAILABLE", Message: err.Error()}, w.now())
		}
	}
	var payload control.CreateRepositoryPayload
	if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
		return control.Result{}, err
	}
	if w.Backend == nil {
		return control.Result{}, errors.New("repository backend is required")
	}
	repo, err := w.Backend.Create(ctx, ticket.OperationID, session.RealmID, strings.TrimSpace(payload.Name))
	if err != nil {
		// Backend boundaries are resumable. Report the current attempt but do not
		// bind a terminal result: the same operation/request must be retryable.
		return control.NewErrorResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.ErrorBody{Code: "CREATE_REPOSITORY_RETRY", Message: err.Error()}, w.now())
	}
	if strings.TrimSpace(repo.RepoID) == "" || strings.TrimSpace(repo.URL) == "" {
		return control.Result{}, fmt.Errorf("backend returned incomplete repository")
	}
	result, err := control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.CreateRepositoryResult{RepoID: repo.RepoID, RepoURL: repo.URL}, w.now())
	if err == nil {
		err = w.Store.Save(result)
	}
	return result, err
}

func (w *Worker) grantAccess(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	if w.Grants == nil {
		return w.failure(ticket, "REALM_GRANT_UNAVAILABLE", "realm grants are unavailable")
	}
	var payload control.GrantAccessPayload
	if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
		return control.Result{}, err
	}
	record, err := w.Grants.Grant(ctx, session.RealmID, payload.RecipientRealmID, payload.RepoID, payload.Access)
	if err != nil {
		return w.failure(ticket, "REALM_GRANT_REJECTED", err.Error())
	}
	result, err := control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.RealmGrantResult{RepoID: record.RepoID, RecipientRealmID: record.RecipientRealmID, Access: record.Access, State: record.State}, w.now())
	if err == nil {
		err = w.Store.Save(result)
	}
	return result, err
}

func (w *Worker) revokeAccess(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	if w.Grants == nil {
		return w.failure(ticket, "REALM_GRANT_UNAVAILABLE", "realm grants are unavailable")
	}
	var payload control.RevokeAccessPayload
	if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
		return control.Result{}, err
	}
	record, err := w.Grants.RevokeGrant(ctx, session.RealmID, payload.RecipientRealmID, payload.RepoID)
	if err != nil {
		return w.failure(ticket, "REALM_GRANT_REJECTED", err.Error())
	}
	result, err := control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.RealmGrantResult{RepoID: record.RepoID, RecipientRealmID: record.RecipientRealmID, State: record.State}, w.now())
	if err == nil {
		err = w.Store.Save(result)
	}
	return result, err
}

func (w *Worker) listGrantRecipients(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	if w.Grants == nil {
		return w.failure(ticket, "REALM_DIRECTORY_UNAVAILABLE", "realm directory is unavailable")
	}
	recipients, err := w.Grants.ListGrantRecipients(ctx, session.RealmID)
	if err != nil {
		return w.failure(ticket, "REALM_DIRECTORY_UNAVAILABLE", "realm directory is unavailable")
	}
	result := make([]control.GrantRecipient, 0, len(recipients))
	for _, recipient := range recipients {
		result = append(result, control.GrantRecipient{RealmID: recipient.RealmID, Alias: recipient.Alias})
	}
	return control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.ListGrantRecipientsResult{Recipients: result}, w.now())
}

func (w *Worker) setRealmDirectoryVisibility(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	if w.Grants == nil {
		return w.failure(ticket, "REALM_DIRECTORY_UNAVAILABLE", "realm directory is unavailable")
	}
	var payload control.SetRealmDirectoryVisibilityPayload
	if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
		return control.Result{}, err
	}
	visibility, err := w.Grants.SetRealmDirectoryVisibility(ctx, session.RealmID, payload.Visibility)
	if err != nil {
		return w.failure(ticket, "REALM_DIRECTORY_REJECTED", err.Error())
	}
	return control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.SetRealmDirectoryVisibilityResult{Visibility: visibility}, w.now())
}

func (w *Worker) requestRealmRemoval(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	if w.RealmRemoval == nil {
		return w.failure(ticket, "REALM_REMOVE_UNAVAILABLE", "realm removal is unavailable")
	}
	var payload control.RealmRemoveRequestPayload
	if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
		return control.Result{}, err
	}
	record, err := w.RealmRemoval.Request(ctx, session, ticket.OperationID, RealmRemovalRequest{NotificationEmail: payload.NotificationEmail, ErasureRequested: payload.ErasureRequested, RecoveryPublicKey: payload.RecoveryPublicKey})
	if err != nil {
		return w.retryable(ticket, "REALM_REMOVE_REQUEST_RETRY", err.Error())
	}
	result, err := control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.RealmRemoveRequestResult{
		ExpiresAt:            record.ExpiresAt.UTC().Format(time.RFC3339Nano),
		ActiveClientCount:    len(record.Scope.ClientIDs),
		OwnedRepositoryCount: len(record.Scope.OwnedRepoIDs),
		ForeignGrantCount:    len(record.Scope.ForeignGrantRepoIDs),
		AdminContact:         w.RecoveryAdminContact,
	}, w.now())
	if err == nil {
		err = w.Store.Save(result)
	}
	return result, err
}

func (w *Worker) confirmRealmRemoval(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	if w.RealmRemoval == nil {
		return w.failure(ticket, "REALM_REMOVE_UNAVAILABLE", "realm removal is unavailable")
	}
	var payload control.RealmRemoveConfirmPayload
	if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
		return control.Result{}, err
	}
	record, manifest, err := w.RealmRemoval.Confirm(ctx, session, ticket.OperationID, payload.OTP)
	if err != nil {
		return w.retryable(ticket, "REALM_REMOVE_CONFIRM_RETRY", err.Error())
	}
	result, err := control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.RealmRemoveConfirmResult{
		State: string(record.State), Manifest: controlRecoveryManifest(manifest),
		AdminContact: w.RecoveryAdminContact, ErasureRequested: record.Request.ErasureRequested,
		ErasureMaxDays: w.DataErasureMaxDays,
	}, w.now())
	if err == nil {
		err = w.Store.Save(result)
	}
	return result, err
}

func controlRecoveryManifest(manifest RecoveryManifest) control.RealmRecoveryManifest {
	archives := make([]control.RealmRecoveryArchive, 0, len(manifest.Archives))
	for _, archive := range manifest.Archives {
		archives = append(archives, control.RealmRecoveryArchive{
			ArchiveID: archive.ArchiveID, RepoID: archive.RepoID,
			SHA256: archive.SHA256, Size: archive.Size,
		})
	}
	return control.RealmRecoveryManifest{
		Schema: manifest.Schema, OperationID: manifest.OperationID, RealmID: manifest.RealmID,
		Archives: archives, DownloadUntil: manifest.DownloadUntil,
		AdminGraceUntil: manifest.AdminGraceUntil, CreatedAt: manifest.CreatedAt,
	}
}

func (w *Worker) detachClient(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	if w.ClientDetacher == nil {
		return w.failure(ticket, "CLIENT_DETACH_UNAVAILABLE", "client detach is unavailable")
	}
	revision, err := w.ClientDetacher.DetachClient(ctx, session.ClientID)
	if err != nil {
		return w.retryable(ticket, "CLIENT_DETACH_RETRY", err.Error())
	}
	return control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.ClientDeactivateResult{ServiceRevision: revision}, w.now())
}

func (w *Worker) claimRealmAlias(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	if w.Aliases == nil {
		return w.failure(ticket, "REALM_ALIAS_UNAVAILABLE", "realm alias service is unavailable")
	}
	var payload control.ClaimRealmAliasPayload
	if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
		return control.Result{}, err
	}
	alias, err := w.Aliases.Claim(ctx, session.RealmID, payload.Alias)
	if err != nil {
		// Intentionally collapse collision and policy details: this is a claim
		// operation, never an alias availability oracle.
		return w.failure(ticket, "REALM_ALIAS_REJECTED", "realm alias cannot be assigned")
	}
	return control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.ClaimRealmAliasResult{Alias: alias}, w.now())
}

func (w *Worker) resolveOwnerLabels(ctx context.Context, ticket control.Ticket) (control.Result, error) {
	if w.Aliases == nil {
		return w.failure(ticket, "OWNER_LABELS_UNAVAILABLE", "owner labels are unavailable")
	}
	var payload control.ResolveOwnerLabelsPayload
	if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
		return control.Result{}, err
	}
	labels, err := w.Aliases.Resolve(ctx, payload.OwnerIDs)
	if err != nil {
		return w.failure(ticket, "OWNER_LABELS_UNAVAILABLE", "owner labels are unavailable")
	}
	return control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.ResolveOwnerLabelsResult{Labels: labels}, w.now())
}

func (w *Worker) deleteRepository(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	var payload control.DeleteRepositoryPayload
	if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
		return control.Result{}, err
	}
	if w.Backend == nil {
		return control.Result{}, errors.New("repository backend is required")
	}
	retainUntil, err := w.Backend.Delete(ctx, ticket.OperationID, session.RealmID, payload.RepoID)
	if err != nil {
		return w.retryable(ticket, "DELETE_REPOSITORY_RETRY", err.Error())
	}
	result, err := control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.DeleteRepositoryResult{
		RepoID: payload.RepoID, RetainUntil: retainUntil.UTC().Format(time.RFC3339Nano),
	}, w.now())
	if err == nil {
		err = w.Store.Save(result)
	}
	return result, err
}

// mobilePairing mints a pairing token for the authenticated session's own
// realm. session.RealmID is resolved server-side from the connection
// (ViewResolver), never from the ticket payload (MobilePairingPayload
// carries no realm_id at all) - any authenticated client of a realm may
// pair a new mobile device into that same realm, no CanCreateRepositories
// gate needed.
// loadRepositoryDump handles LOAD_REPOSITORY_DUMP. Every failure returned by
// DumpLoader.Load is treated as terminal (w.failure, bound to the operation
// ID) — consistent with LOAD_REPOSITORY_DUMP_CONCEPT.md §5.2/§8: a missing
// toolchain capability or a failed precondition never gets a silent retry.
//
// [KNOWN GAP] True resumability for a crash between a successful swap and
// this function's Store.Save call is not yet implemented: a retry of the
// same operation_id would re-run the §4 precondition, which the repository
// no longer satisfies once loaded, and would fail — even though the load
// itself already succeeded. The generation is still recoverable from
// ArchiveDir by an operator; nothing is silently destroyed.
func (w *Worker) loadRepositoryDump(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	if w.DumpLoader == nil {
		return w.failure(ticket, "LOAD_REPOSITORY_DUMP_UNAVAILABLE", "repository dump loading is not configured on this worker")
	}
	var payload control.LoadRepositoryDumpPayload
	if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
		return control.Result{}, err
	}
	loaded, err := w.DumpLoader.Load(ctx, session.RealmID, payload.RepoID, ticket.OperationID, payload.ApplyCurrentIgnorePolicy, payload.KeepLastRevisions)
	if err != nil {
		return w.failure(ticket, "LOAD_REPOSITORY_DUMP_FAILED", err.Error())
	}
	result, err := control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.LoadRepositoryDumpResult{
		RepoID: payload.RepoID, OldUUID: loaded.OldUUID, NewUUID: loaded.NewUUID,
		SourceRevisionRange: loaded.SourceRevisionRange, ToolVersions: loaded.ToolVersions,
	}, w.now())
	if err == nil {
		err = w.Store.Save(result)
	}
	return result, err
}

func (w *Worker) mobilePairing(session Session, ticket control.Ticket) (control.Result, error) {
	if w.MobilePairing == nil {
		return w.failure(ticket, "MOBILE_PAIRING_UNAVAILABLE", "mobile pairing is not configured on this worker")
	}
	grants := make([]MobilePairingRepoGrant, 0, len(session.Repositories))
	for _, repo := range session.Repositories {
		grants = append(grants, MobilePairingRepoGrant{RepoID: repo.RepoID, Access: repo.Access, AttachmentPolicy: repo.AttachmentPolicy})
	}
	token, expiresAt, err := w.MobilePairing.CreatePairing(session.RealmID, grants)
	if err != nil {
		return w.failure(ticket, "MOBILE_PAIRING_FAILED", err.Error())
	}
	result, err := control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.MobilePairingResult{Token: token, ExpiresAt: expiresAt.UTC().Format(time.RFC3339Nano)}, w.now())
	if err == nil {
		err = w.Store.Save(result)
	}
	return result, err
}

func (w *Worker) preflight(ctx context.Context, ticket control.Ticket) (control.Result, error) {
	var payload control.StoragePreflightPayload
	if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
		return control.Result{}, err
	}
	if w.Capacity == nil {
		return control.Result{}, errors.New("repository capacity checker is required")
	}
	var available, required int64
	var expires time.Time
	var err error
	if w.Reservations != nil {
		available, required, expires, err = w.Reservations.Reserve(ctx, ticket.OperationID, payload.ContentBytes, w.now())
		if err != nil && !errors.Is(err, ErrReservationUnavailable) {
			return w.retryable(ticket, "STORAGE_PREFLIGHT_FAILED", err.Error())
		}
	} else {
		available, required, err = w.Capacity.Check(ctx, payload.ContentBytes)
		if err != nil {
			return w.retryable(ticket, "STORAGE_PREFLIGHT_FAILED", err.Error())
		}
	}
	if err != nil || available < required {
		// Deliberately NOT persisted through Store.Save. Insufficient storage is
		// a temporarily-unavailable dependency, not an invalid request: binding
		// it as the operation's terminal result would permanently block this
		// exact (client, local path, name) tuple even after the disk is freed,
		// with no reset path anywhere in pkg/provisioning. Same reasoning, and
		// same treatment, as the CREATE_REPOSITORY_RETRY/INITIAL_COMMIT_RETRY
		// branches above.
		return control.NewErrorResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.ErrorBody{
			Code:    "STORAGE_INSUFFICIENT",
			Message: fmt.Sprintf("server storage requires %s, %s available", formatBytes(required), formatBytes(available)),
			Details: map[string]string{
				"available_bytes": fmt.Sprintf("%d", available),
				"required_bytes":  fmt.Sprintf("%d", required),
			},
		}, w.now())
	}
	expiresAt := ""
	if !expires.IsZero() {
		expiresAt = expires.UTC().Format(time.RFC3339Nano)
	}
	result, err := control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.StoragePreflightResult{AvailableBytes: available, RequiredBytes: required, ReservationExpiresAt: expiresAt}, w.now())
	if err == nil {
		err = w.Store.Save(result)
	}
	return result, err
}

func (w *Worker) activate(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	var payload control.InitialCommitPayload
	if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
		return control.Result{}, err
	}
	if w.Activator == nil {
		return control.Result{}, errors.New("repository activator is required")
	}
	if err := w.Activator.Activate(ctx, payload.RepoID, session.RealmID); err != nil {
		return control.NewErrorResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.ErrorBody{Code: "INITIAL_COMMIT_RETRY", Message: err.Error()}, w.now())
	}
	result, err := control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.InitialCommitResult{Acknowledged: true}, w.now())
	if err == nil {
		err = w.Store.Save(result)
	}
	if err == nil && w.Reservations != nil {
		err = w.Reservations.Release(ticket.OperationID)
	}
	return result, err
}

// retryable reports a failure without binding it as the operation's terminal
// result, so the same operation may be attempted again once the underlying
// dependency recovers. Use it for "temporarily unavailable"; use failure() for
// "this request is invalid and will never succeed".
func (w *Worker) retryable(t control.Ticket, code, message string) (control.Result, error) {
	return control.NewErrorResult(t.OperationID, t.RequestID, t.Type, control.ErrorBody{Code: code, Message: message}, w.now())
}

func (w *Worker) failure(t control.Ticket, code, message string) (control.Result, error) {
	r, e := control.NewErrorResult(t.OperationID, t.RequestID, t.Type, control.ErrorBody{Code: code, Message: message}, w.now())
	if e == nil {
		if w.Store == nil {
			return control.Result{}, errors.New("repository result store is required")
		}
		e = w.Store.Save(r)
	}
	return r, e
}

// formatBytes renders a byte count for a human reader (e.g. "178.7 MB"),
// used only in user-facing error messages -- Details keeps the exact
// integer byte counts for anything that parses the result programmatically.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}
