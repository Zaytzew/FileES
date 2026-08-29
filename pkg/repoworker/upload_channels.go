package repoworker

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"filees/internal/uploadworker"
	"filees/pkg/clientview"
	control "filees/pkg/control/v1"
	"filees/pkg/onboarding"
	"filees/public-shares/channel"
	"filees/public-shares/manifest"
	"github.com/google/uuid"
)

var ErrUploadChannelRejected = errors.New("upload channel request rejected")

type UploadTokenDeliverer interface {
	DeliverUploadTokens(context.Context, channel.UploadRecord, []channel.Delivery) error
}

const uploadChannelMailSchema = "filees.upload-channel-mail/v1"

type UploadChannelMailJob struct {
	Schema          string    `json:"schema"`
	MessageID       string    `json:"message_id"`
	ChannelID       string    `json:"channel_id"`
	Alias           string    `json:"alias"`
	Slug            string    `json:"slug"`
	DeliveryAddress string    `json:"delivery_address"`
	Invitation      string    `json:"invitation"`
	State           string    `json:"state"`
	AttemptID       string    `json:"attempt_id,omitempty"`
	LeaseUntil      time.Time `json:"lease_until,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

type UploadChannelOutbox struct {
	Root string
	Now  func() time.Time
}

func (o UploadChannelOutbox) DeliverUploadTokens(_ context.Context, record channel.UploadRecord, deliveries []channel.Delivery) error {
	if !filepath.IsAbs(o.Root) {
		return errors.New("upload channel outbox root must be absolute")
	}
	now := time.Now().UTC()
	if o.Now != nil {
		now = o.Now().UTC()
	}
	for _, delivery := range deliveries {
		email, err := onboarding.CanonicalEmail(delivery.Email)
		if err != nil || delivery.Token == "" {
			return errors.New("upload channel delivery is invalid")
		}
		digest := sha256.Sum256([]byte(record.ChannelID + "\x00upload\x00" + email + "\x00" + delivery.Token))
		job := UploadChannelMailJob{Schema: uploadChannelMailSchema, MessageID: uuid.NewSHA1(uuid.NameSpaceOID, digest[:]).String(), ChannelID: record.ChannelID, Alias: record.Alias, Slug: record.Slug, DeliveryAddress: email, Invitation: delivery.Token, State: "pending", CreatedAt: now}
		if err := o.queue(job); err != nil {
			return err
		}
	}
	return nil
}

type UploadChannelService interface {
	List(context.Context, string, string) (control.ListUploadChannelsResult, error)
	ListQuarantine(context.Context, string) (control.ListQuarantineResult, error)
	HideQuarantine(context.Context, string, string) (control.HideQuarantineResult, error)
	FetchQuarantine(context.Context, string, string) (control.FetchQuarantineResult, error)
	Create(context.Context, string, string, control.UploadChannelDeclaration) (control.UploadChannelResult, error)
	Update(context.Context, string, string, string, control.UploadChannelDeclaration) (control.UploadChannelResult, error)
	Revoke(context.Context, string, string) (control.UploadChannelResult, error)
	Delete(context.Context, string, string) (control.UploadChannelResult, error)
}

type ChannelUploadService struct {
	Channels  *channel.Store
	Backend   Backend
	Deliverer UploadTokenDeliverer
	TrashRoot string
}

func (s ChannelUploadService) List(_ context.Context, ownerRealm, authorityRepoID string) (control.ListUploadChannelsResult, error) {
	if s.Channels == nil {
		return control.ListUploadChannelsResult{}, errors.New("upload channel store is unavailable")
	}
	records, err := s.Channels.ListOwnedUploads(ownerRealm, authorityRepoID)
	if err != nil {
		return control.ListUploadChannelsResult{}, classifyUploadError(err)
	}
	listed := control.ListUploadChannelsResult{Channels: make([]control.UploadChannelSummary, 0, len(records))}
	for _, record := range records {
		if record.Manifest == nil {
			return control.ListUploadChannelsResult{}, errors.New("upload channel record is incomplete")
		}
		listed.Channels = append(listed.Channels, control.UploadChannelSummary{
			ChannelID: record.ChannelID, AuthorityRepoID: record.Manifest.AuthorityRepoID, UploadRepoID: record.Manifest.UploadRepoID,
			Alias: record.Alias, Slug: record.Slug, Kind: manifest.NormalizeKind(record.Manifest.Kind), State: record.State, Recipients: append([]string(nil), record.Manifest.Recipients...),
			RequireOTP: record.Manifest.RequireOTP, CollisionPolicy: string(record.Manifest.CollisionPolicy),
			UpdatedAt: record.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return listed, nil
}

func (s ChannelUploadService) waiting() uploadworker.Reaper {
	return uploadworker.Reaper{TrashRoot: s.TrashRoot}
}

func (s ChannelUploadService) ListQuarantine(_ context.Context, ownerRealm string) (control.ListQuarantineResult, error) {
	if s.TrashRoot == "" {
		return control.ListQuarantineResult{}, nil
	}
	list, err := s.waiting().ListWaiting(ownerRealm, time.Now().UTC())
	if err != nil {
		return control.ListQuarantineResult{}, err
	}
	out := control.ListQuarantineResult{Items: make([]control.QuarantineItem, 0, len(list.Entries))}
	for _, item := range list.Entries {
		out.Items = append(out.Items, control.QuarantineItem{
			UploadID: item.UploadID, OriginalName: item.OriginalName, Size: item.Size,
			AVVerdict: item.AVVerdict, ReceivedAt: item.ReceivedAt.UTC().Format(time.RFC3339Nano),
			RemainingHours: item.RemainingHours,
		})
	}
	for _, item := range list.Purged {
		out.Purged = append(out.Purged, control.QuarantinePurged{
			UploadID: item.UploadID, OriginalName: item.OriginalName, PurgedAt: item.PurgedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	if n := len(list.Purged); n == 1 {
		out.Message = "Usunięto z kwarantanny 1 plik po 48 godzinach."
	} else if n > 1 {
		word := "plików"
		if n%10 >= 2 && n%10 <= 4 && (n%100 < 10 || n%100 >= 20) {
			word = "pliki"
		}
		out.Message = fmt.Sprintf("Usunięto z kwarantanny %d %s po 48 godzinach.", n, word)
	}
	return out, nil
}

func (s ChannelUploadService) HideQuarantine(_ context.Context, _, uploadID string) (control.HideQuarantineResult, error) {
	if err := s.waiting().HideWaiting(uploadID, time.Now().UTC()); err != nil {
		return control.HideQuarantineResult{}, err
	}
	return control.HideQuarantineResult{UploadID: uploadID}, nil
}

func (s ChannelUploadService) FetchQuarantine(_ context.Context, _, uploadID string) (control.FetchQuarantineResult, error) {
	idx, payload, hours, err := s.waiting().FetchWaiting(uploadID, time.Now().UTC())
	if err != nil {
		return control.FetchQuarantineResult{}, err
	}
	if len(payload) > 64<<20 {
		return control.FetchQuarantineResult{}, errors.New("quarantine payload exceeds 64 MiB")
	}
	return control.FetchQuarantineResult{UploadID: idx.UploadID, OriginalName: idx.OriginalName, Payload: payload, RemainingHours: hours}, nil
}

func (s ChannelUploadService) Create(ctx context.Context, operationID, ownerRealm string, declaration control.UploadChannelDeclaration) (control.UploadChannelResult, error) {
	if s.Channels == nil || s.Backend == nil {
		return control.UploadChannelResult{}, errors.New("upload channel service is incomplete")
	}
	if declaration.CollisionPolicy == "" {
		declaration.CollisionPolicy = string(manifest.CollisionDeny)
	}
	if manifest.CollisionPolicy(declaration.CollisionPolicy) != manifest.CollisionDeny {
		return control.UploadChannelResult{}, fmt.Errorf("%w: %v", ErrUploadChannelRejected, channel.ErrPolicy)
	}
	if len(declaration.Recipients) == 0 {
		return control.UploadChannelResult{}, fmt.Errorf("%w: %v", ErrUploadChannelRejected, manifest.ErrNoRecipients)
	}
	if err := s.Channels.Authority.OwnsActiveRepository(ownerRealm, declaration.AuthorityRepoID); err != nil {
		return control.UploadChannelResult{}, classifyUploadError(channel.ErrForbidden)
	}
	alias, err := s.Channels.Authority.ActiveRealmAlias(ownerRealm)
	if err != nil {
		return control.UploadChannelResult{}, classifyUploadError(channel.ErrForbidden)
	}
	if err := s.Channels.ReserveAddress(operationID, ownerRealm, alias, declaration.Slug); err != nil {
		return control.UploadChannelResult{}, classifyUploadError(err)
	}
	trash, err := s.provision(ctx, trashOperationID(ownerRealm), ownerRealm, "Kwarantanna", clientview.PurposeUploadTrash)
	if err != nil {
		return control.UploadChannelResult{}, err
	}
	upload, err := s.provision(ctx, operationID, ownerRealm, "Półka "+declaration.Slug, clientview.PurposeUploadShelf)
	if err != nil {
		return control.UploadChannelResult{}, err
	}
	body := toUploadManifest(ownerRealm, upload.RepoID, declaration)
	if err := body.Validate(); err != nil {
		return control.UploadChannelResult{}, fmt.Errorf("%w: %v", ErrUploadChannelRejected, err)
	}
	record, deliveries, err := s.Channels.CreateUpload(operationID, ownerRealm, body)
	if err != nil {
		return control.UploadChannelResult{}, classifyUploadError(err)
	}
	if len(deliveries) > 0 {
		if s.Deliverer == nil {
			return control.UploadChannelResult{}, errors.New("upload channel token delivery is unavailable")
		}
		if err := s.Deliverer.DeliverUploadTokens(ctx, record, deliveries); err != nil {
			return control.UploadChannelResult{}, err
		}
	}
	return uploadResult(record, upload.URL, trash, len(deliveries)), nil
}

func (s ChannelUploadService) Update(ctx context.Context, operationID, ownerRealm, channelID string, declaration control.UploadChannelDeclaration) (control.UploadChannelResult, error) {
	if s.Channels == nil {
		return control.UploadChannelResult{}, errors.New("upload channel store is unavailable")
	}
	current, err := s.Channels.GetUpload(channelID)
	if err != nil {
		return control.UploadChannelResult{}, classifyUploadError(err)
	}
	if current.Manifest == nil {
		return control.UploadChannelResult{}, classifyUploadError(channel.ErrNotFound)
	}
	declaration.AuthorityRepoID = current.Manifest.AuthorityRepoID
	declaration.Slug = current.Manifest.Slug
	currentKind := manifest.NormalizeKind(current.Manifest.Kind)
	if declaration.Kind != "" && manifest.NormalizeKind(declaration.Kind) != currentKind {
		return control.UploadChannelResult{}, fmt.Errorf("%w: %v", ErrUploadChannelRejected, manifest.ErrKind)
	}
	declaration.Kind = currentKind
	if declaration.CollisionPolicy == "" {
		declaration.CollisionPolicy = string(manifest.CollisionDeny)
	}
	body := toUploadManifest(ownerRealm, current.Manifest.UploadRepoID, declaration)
	record, deliveries, err := s.Channels.UpdateUpload(operationID, ownerRealm, channelID, body)
	if err != nil {
		return control.UploadChannelResult{}, classifyUploadError(err)
	}
	if len(deliveries) > 0 {
		if s.Deliverer == nil {
			return control.UploadChannelResult{}, errors.New("upload channel token delivery is unavailable")
		}
		if err := s.Deliverer.DeliverUploadTokens(ctx, record, deliveries); err != nil {
			return control.UploadChannelResult{}, err
		}
	}
	return uploadResult(record, "", Repository{}, len(deliveries)), nil
}

func (s ChannelUploadService) Revoke(_ context.Context, ownerRealm, channelID string) (control.UploadChannelResult, error) {
	if s.Channels == nil {
		return control.UploadChannelResult{}, errors.New("upload channel store is unavailable")
	}
	record, err := s.Channels.RevokeUpload(ownerRealm, channelID)
	if err != nil {
		return control.UploadChannelResult{}, classifyUploadError(err)
	}
	return uploadResult(record, "", Repository{}, 0), nil
}

func (s ChannelUploadService) Delete(_ context.Context, ownerRealm, channelID string) (control.UploadChannelResult, error) {
	if s.Channels == nil {
		return control.UploadChannelResult{}, errors.New("upload channel store is unavailable")
	}
	record, err := s.Channels.DeleteUpload(ownerRealm, channelID)
	if err != nil {
		return control.UploadChannelResult{}, classifyUploadError(err)
	}
	return uploadResult(record, "", Repository{}, 0), nil
}

func toUploadManifest(ownerRealm, uploadRepoID string, declaration control.UploadChannelDeclaration) manifest.Upload {
	policy := manifest.CollisionPolicy(declaration.CollisionPolicy)
	if policy == "" {
		policy = manifest.CollisionDeny
	}
	kind := manifest.NormalizeKind(declaration.Kind)
	if kind == "" {
		kind = manifest.KindShelf
	}
	return manifest.Upload{
		OwnerRealm: ownerRealm, AuthorityRepoID: declaration.AuthorityRepoID, UploadRepoID: uploadRepoID,
		Slug: declaration.Slug, Kind: kind, Recipients: append([]string(nil), declaration.Recipients...),
		RequireOTP: declaration.RequireOTP, CollisionPolicy: policy,
	}
}

type purposeCreator interface {
	CreateWithPurpose(context.Context, string, string, string, string) (Repository, error)
}

func (s ChannelUploadService) provision(ctx context.Context, op, realm, name, purpose string) (Repository, error) {
	if creator, ok := s.Backend.(purposeCreator); ok {
		return creator.CreateWithPurpose(ctx, op, realm, name, purpose)
	}
	return s.Backend.Create(ctx, op, realm, name)
}

func uploadResult(record channel.UploadRecord, uploadURL string, trash Repository, deliveries int) control.UploadChannelResult {
	result := control.UploadChannelResult{ChannelID: record.ChannelID, Alias: record.Alias, Slug: record.Slug, State: record.State, RecipientDeliveries: deliveries}
	if record.Manifest != nil {
		result.UploadRepoID = record.Manifest.UploadRepoID
	}
	result.UploadRepoURL = uploadURL
	result.TrashRepoID = trash.RepoID
	result.TrashRepoURL = trash.URL
	return result
}

func classifyUploadError(err error) error {
	if errors.Is(err, channel.ErrNotFound) || errors.Is(err, channel.ErrForbidden) || errors.Is(err, channel.ErrSlugTaken) || errors.Is(err, channel.ErrInactive) || errors.Is(err, channel.ErrRecordConflict) || errors.Is(err, channel.ErrPolicy) {
		return fmt.Errorf("%w: %v", ErrUploadChannelRejected, err)
	}
	return err
}

func trashOperationID(realmID string) string {
	return channel.TrashOperationID(realmID)
}

func UploadTrashRepositoryID(realmID string) string {
	return channel.TrashRepositoryID(realmID)
}

func isUploadChannelTicket(typ control.TicketType) bool {
	switch typ {
	case control.TicketListUploadChannels, control.TicketCreateUploadChannel, control.TicketUpdateUploadChannel, control.TicketRevokeUploadChannel, control.TicketDeleteUploadChannel, control.TicketListQuarantine, control.TicketHideQuarantine, control.TicketFetchQuarantine:
		return true
	default:
		return false
	}
}

func (w *Worker) uploadChannel(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	if w.UploadChannels == nil {
		return w.failure(ticket, "UPLOAD_CHANNEL_UNAVAILABLE", "upload channels are unavailable")
	}
	var response any
	var err error
	switch ticket.Type {
	case control.TicketListUploadChannels:
		var payload control.ListUploadChannelsPayload
		if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
			return control.Result{}, err
		}
		response, err = w.UploadChannels.List(ctx, session.RealmID, payload.AuthorityRepoID)
	case control.TicketCreateUploadChannel:
		var payload control.CreateUploadChannelPayload
		if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
			return control.Result{}, err
		}
		response, err = w.UploadChannels.Create(ctx, ticket.OperationID, session.RealmID, payload.UploadChannelDeclaration)
	case control.TicketUpdateUploadChannel:
		var payload control.UpdateUploadChannelPayload
		if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
			return control.Result{}, err
		}
		response, err = w.UploadChannels.Update(ctx, ticket.OperationID, session.RealmID, payload.ChannelID, payload.UploadChannelDeclaration)
	case control.TicketRevokeUploadChannel:
		var payload control.RevokeUploadChannelPayload
		if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
			return control.Result{}, err
		}
		response, err = w.UploadChannels.Revoke(ctx, session.RealmID, payload.ChannelID)
	case control.TicketDeleteUploadChannel:
		var payload control.DeleteUploadChannelPayload
		if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
			return control.Result{}, err
		}
		response, err = w.UploadChannels.Delete(ctx, session.RealmID, payload.ChannelID)
	case control.TicketListQuarantine:
		response, err = w.UploadChannels.ListQuarantine(ctx, session.RealmID)
	case control.TicketHideQuarantine:
		var payload control.HideQuarantinePayload
		if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
			return control.Result{}, err
		}
		response, err = w.UploadChannels.HideQuarantine(ctx, session.RealmID, payload.UploadID)
	case control.TicketFetchQuarantine:
		var payload control.FetchQuarantinePayload
		if err := control.DecodePayload(ticket.Payload, &payload); err != nil {
			return control.Result{}, err
		}
		response, err = w.UploadChannels.FetchQuarantine(ctx, session.RealmID, payload.UploadID)
	default:
		return control.Result{}, errors.New("unsupported upload channel ticket")
	}
	if err != nil {
		if errors.Is(err, ErrUploadChannelRejected) {
			return w.failure(ticket, "UPLOAD_CHANNEL_REJECTED", "upload channel request was rejected")
		}
		return w.retryable(ticket, "UPLOAD_CHANNEL_RETRY", err.Error())
	}
	wire, err := control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, response, w.now())
	if err == nil {
		err = w.Store.Save(wire)
	}
	return wire, err
}
