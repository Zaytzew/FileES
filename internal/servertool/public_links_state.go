package servertool

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"filees/pkg/publicstate"
	"filees/pkg/repoworker"
	reservationv1 "filees/pkg/reservation/v1"
	"filees/pkg/serverconfig"
	"filees/public-shares/channel"
)

// Build the complete authorized inventory under the same authority fence as
// mutations, then release it before writing to the network. Canonical channel
// records are already atomically maintained by workers; no SVN listing is used.
func publicLinksState(config serverconfig.Config, clientID string, req publicstate.Request) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var raw []byte
	err := repoworker.WithFileLockContext(ctx, filepath.Join(config.Activation.Root, ".service-wc.lock"), func() error {
		confirm, err := stateObservationFence(config.Activation.Root)
		if err != nil {
			return err
		}
		view, _, err := authorizedStateView(config.Activation.ServiceWorkingCopy, clientID, reservationv1.Request{Schema: reservationv1.StateSchema})
		if err != nil {
			return err
		}
		names := map[string]string{}
		ids := []string{}
		for _, repo := range view.Repositories {
			if repo.State == "active" && repo.OwnerRealmID == view.RealmID {
				ids = append(ids, repo.RepoID)
				names[repo.RepoID] = repo.DisplayName
			}
		}
		items := []publicstate.Item{}
		if config.PublicShares.Enabled {
			reader := repoworker.ChannelPublicShareService{BaseURL: config.PublicShares.BaseURL, Channels: &channel.Store{
				Root:      config.PublicShares.EffectiveStateRoot(config.Repositories.ResultsRoot),
				Authority: repoworker.ServicePublisher{ServiceWC: config.Activation.ServiceWorkingCopy},
			}}
			shares, err := reader.ListOwnedRepositories(ctx, view.RealmID, ids)
			if err != nil {
				return err
			}
			for _, share := range shares {
				share.InvitationMessage = "" // dashboard, not the invitation editor
				items = append(items, publicstate.Item{RepoDisplayName: names[share.RepoID], Share: share})
			}
		}
		result, err := publicstate.Build(publicstate.Binding{ServerID: config.ServerID, ClientID: clientID, RealmID: view.RealmID}, req, items)
		if err != nil {
			return err
		}
		raw, err = json.Marshal(result)
		if err != nil {
			return err
		}
		if len(raw) > publicstate.MaxResponseBytes {
			return errors.New("public link state exceeds response limit")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return confirm()
	})
	if err != nil {
		return nil, err
	}
	return raw, nil
}
