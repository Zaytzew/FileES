package repoworker

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	control "filees/pkg/control/v1"
	"filees/pkg/realmbranding"
	"filees/public-shares/channel"
	"filees/public-shares/manifest"
)

type PublicShareTokenDeliverer interface {
	DeliverPublicShareTokens(context.Context, channel.Record, []channel.Delivery) error
}

type ChannelPublicShareService struct {
	Channels  *channel.Store
	Deliverer PublicShareTokenDeliverer
}

func (s ChannelPublicShareService) List(_ context.Context, ownerRealm, repoID string) ([]control.PublicShareSummary, error) {
	if s.Channels == nil {
		return nil, errors.New("public share channel store is unavailable")
	}
	records, err := s.Channels.ListOwned(ownerRealm, repoID)
	if err != nil {
		return nil, classifyPublicShareError(err)
	}
	result := make([]control.PublicShareSummary, 0, len(records))
	for _, record := range records {
		if record.Manifest == nil {
			return nil, errors.New("public share channel record is incomplete")
		}
		objects := make([]control.PublicShareObject, 0, len(record.Manifest.Objects))
		for _, object := range record.Manifest.Objects {
			objects = append(objects, control.PublicShareObject{PublicID: object.PublicID, RepoPath: object.RepoPath, DisplayName: object.DisplayName, Size: object.Size})
		}
		result = append(result, control.PublicShareSummary{
			ChannelID: record.ChannelID, RepoID: record.RepoID, Alias: record.Alias, Slug: record.Slug, State: record.State,
			SourceRoot: record.Manifest.SourceRoot, Recipients: append([]string(nil), record.Manifest.Recipients...), PasswordProtected: record.Manifest.Password != "",
			DoNotFollow: record.Manifest.DoNotFollow, Objects: objects, UpdatedAt: record.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return result, nil
}

func (s ChannelPublicShareService) Create(ctx context.Context, operationID, ownerRealm string, declaration control.PublicShareDeclaration) (control.PublicShareResult, error) {
	if s.Channels == nil {
		return control.PublicShareResult{}, errors.New("public share channel store is unavailable")
	}
	share := toShare(ownerRealm, declaration)
	if err := share.Validate(); err != nil {
		return control.PublicShareResult{}, fmt.Errorf("%w: %v", ErrPublicShareRejected, err)
	}
	record, deliveries, err := s.Channels.Create(operationID, ownerRealm, share)
	if err != nil {
		return control.PublicShareResult{}, classifyPublicShareError(err)
	}
	if len(deliveries) > 0 {
		if s.Deliverer == nil {
			return control.PublicShareResult{}, errors.New("public share token delivery is unavailable")
		}
		if err := s.Deliverer.DeliverPublicShareTokens(ctx, record, deliveries); err != nil {
			return control.PublicShareResult{}, err
		}
	}
	return publicShareResult(record, len(deliveries)), nil
}

func (s ChannelPublicShareService) Update(ctx context.Context, operationID, ownerRealm, channelID string, declaration control.PublicShareDeclaration, keepPassword bool) (control.PublicShareResult, error) {
	if s.Channels == nil {
		return control.PublicShareResult{}, errors.New("public share channel store is unavailable")
	}
	share := toShare(ownerRealm, declaration)
	if err := share.Validate(); err != nil {
		return control.PublicShareResult{}, fmt.Errorf("%w: %v", ErrPublicShareRejected, err)
	}
	var record channel.Record
	var deliveries []channel.Delivery
	var err error
	if keepPassword {
		record, deliveries, err = s.Channels.UpdatePreservingPassword(operationID, ownerRealm, channelID, share)
	} else {
		record, deliveries, err = s.Channels.Update(operationID, ownerRealm, channelID, share)
	}
	if err != nil {
		return control.PublicShareResult{}, classifyPublicShareError(err)
	}
	if len(deliveries) > 0 {
		if s.Deliverer == nil {
			return control.PublicShareResult{}, errors.New("public share token delivery is unavailable")
		}
		if err := s.Deliverer.DeliverPublicShareTokens(ctx, record, deliveries); err != nil {
			return control.PublicShareResult{}, err
		}
	}
	return publicShareResult(record, len(deliveries)), nil
}

func (s ChannelPublicShareService) Revoke(_ context.Context, ownerRealm, channelID string) (control.PublicShareResult, error) {
	if s.Channels == nil {
		return control.PublicShareResult{}, errors.New("public share channel store is unavailable")
	}
	record, err := s.Channels.Revoke(ownerRealm, channelID)
	if err != nil {
		return control.PublicShareResult{}, classifyPublicShareError(err)
	}
	return publicShareResult(record, 0), nil
}

func (s ChannelPublicShareService) Delete(_ context.Context, ownerRealm, channelID string) (control.PublicShareResult, error) {
	if s.Channels == nil {
		return control.PublicShareResult{}, errors.New("public share channel store is unavailable")
	}
	record, err := s.Channels.Delete(ownerRealm, channelID)
	if err != nil {
		return control.PublicShareResult{}, classifyPublicShareError(err)
	}
	return publicShareResult(record, 0), nil
}

func toShare(ownerRealm string, declaration control.PublicShareDeclaration) manifest.Share {
	objects := make([]manifest.Object, 0, len(declaration.Objects))
	for _, object := range declaration.Objects {
		objects = append(objects, manifest.Object{PublicID: object.PublicID, RepoPath: object.RepoPath, DisplayName: object.DisplayName, Size: object.Size})
	}
	return manifest.Share{OwnerRealm: ownerRealm, RepoID: declaration.RepoID, SourceRoot: declaration.SourceRoot, Slug: declaration.Slug, Recipients: declaration.Recipients, Password: declaration.PasswordHash, DoNotFollow: declaration.DoNotFollow, Objects: objects}
}

func publicShareResult(record channel.Record, deliveries int) control.PublicShareResult {
	return control.PublicShareResult{ChannelID: record.ChannelID, Alias: record.Alias, Slug: record.Slug, State: record.State, RecipientDeliveries: deliveries}
}

func classifyPublicShareError(err error) error {
	if errors.Is(err, channel.ErrNotFound) || errors.Is(err, channel.ErrForbidden) || errors.Is(err, channel.ErrSlugTaken) || errors.Is(err, channel.ErrInactive) || errors.Is(err, channel.ErrRecordConflict) || errors.Is(err, channel.ErrPolicy) {
		return fmt.Errorf("%w: %v", ErrPublicShareRejected, err)
	}
	return err
}

// OwnsActiveRepository implements channel.RepositoryAuthority directly from
// canonical service records. A realm grant, including rw, never satisfies it.
func (p ServicePublisher) OwnsActiveRepository(realmID, repoID string) error {
	record, err := p.loadActiveRepository(repoID)
	if err != nil || record.OwnerRealmID != realmID {
		return errors.New("requester is not the active repository owner")
	}
	return nil
}

func (p ServicePublisher) ActiveRealmAlias(realmID string) (string, error) {
	if !filepath.IsAbs(p.ServiceWC) {
		return "", errors.New("authority service working copy is incomplete")
	}
	record, err := readRealmRecord(filepath.Join(p.ServiceWC, "admin", "realms", realmID+".json"))
	if err != nil || record.State != "active" || strings.TrimSpace(record.Alias) == "" {
		return "", errors.New("active realm alias is unavailable")
	}
	return record.Alias, nil
}

func (p ServicePublisher) ActiveRealmBranding(realmID string) (realmbranding.Branding, error) {
	return p.RealmPublicBranding(context.Background(), realmID)
}
