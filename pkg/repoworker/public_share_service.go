package repoworker

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	control "filees/pkg/control/v1"
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

func (s ChannelPublicShareService) Update(ctx context.Context, operationID, ownerRealm, channelID string, declaration control.PublicShareDeclaration) (control.PublicShareResult, error) {
	if s.Channels == nil {
		return control.PublicShareResult{}, errors.New("public share channel store is unavailable")
	}
	share := toShare(ownerRealm, declaration)
	if err := share.Validate(); err != nil {
		return control.PublicShareResult{}, fmt.Errorf("%w: %v", ErrPublicShareRejected, err)
	}
	record, deliveries, err := s.Channels.Update(operationID, ownerRealm, channelID, share)
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
		objects = append(objects, manifest.Object{PublicID: object.PublicID, RepoPath: object.RepoPath, DisplayName: object.DisplayName})
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
