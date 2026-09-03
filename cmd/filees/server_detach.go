package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"filees/pkg/clientprofile"
	control "filees/pkg/control/v1"
	"filees/pkg/controlclient"
	"filees/pkg/detachment"
	"filees/pkg/localrepo"

	"github.com/google/uuid"
)

// serverDetachService performs the irreversible final part only after every
// attached working copy has been cleanly quiesced and detached. If local
// detachment or the remote revoke fails, the profile is deliberately kept.
type serverDetachService struct {
	local       *localrepo.Store
	provisioner *daemonProvisioner
	profileRoot string
	detachments *detachment.Store
}

func (s serverDetachService) Detach(ctx context.Context, serverID string) error {
	if s.local == nil || s.provisioner == nil {
		return errors.New("server detach service is incomplete")
	}
	profile, ok := s.provisioner.Profile(serverID)
	if !ok {
		return errors.New("activated client profile is unavailable")
	}
	// Read what the reader will want to know before any of it goes away. The
	// loop below walks every attached record through BeginDetach and the tail
	// of this function removes the profile, so by the time this has succeeded
	// there is no name and no path left to look up.
	name, address := profile.DisplayName, profile.Address
	workingCopies := workingCopiesOf(s.local, serverID)
	for _, record := range s.local.List() {
		if record.ServerID != serverID {
			continue
		}
		switch record.State {
		case localrepo.StateAttached:
			started, err := s.local.BeginDetach(serverID, record.RepoID, false)
			if err != nil {
				return fmt.Errorf("start detach for %s: %w", record.LocalPath, err)
			}
			if _, err := s.provisioner.Detach(ctx, started.OperationID); err != nil {
				return fmt.Errorf("detach %s: %w", record.LocalPath, err)
			}
		case localrepo.StateDetached, localrepo.StateDeleted:
			// Already detached records retain their local data but no metadata.
		default:
			return fmt.Errorf("repository %s has unfinished lifecycle state %q", record.LocalPath, record.State)
		}
	}
	transport, err := controlclient.New(controlclient.Config{Address: profile.Address, Port: profile.SSHPort, IdentityFile: profile.IdentityFile, KnownHosts: profile.KnownHosts, Timeout: 45 * time.Minute})
	if err != nil {
		return err
	}
	operationID := uuid.NewString()
	ticket, err := control.NewTicket(operationID, uuid.NewSHA1(uuid.NameSpaceOID, []byte(operationID+":client-deactivate")).String(), control.TicketClientDeactivate, profile.ClientID, control.ClientDeactivatePayload{}, time.Now())
	if err != nil {
		return err
	}
	result, err := transport.Exchange(ctx, ticket)
	if err != nil {
		return err
	}
	if result.Status != control.ResultOK {
		if result.Error == nil {
			return errors.New("server rejected client detach")
		}
		return fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message)
	}
	if err := clientprofile.Remove(s.profileRoot, serverID); err != nil {
		return fmt.Errorf("remove local credentials after server revoke: %w", err)
	}
	s.provisioner.RemoveProfile(serverID)
	// Recorded last, and deliberately not allowed to fail the detachment. The
	// server has revoked the key and the credentials are gone; reporting an
	// error now would tell the owner that an irreversible thing which did
	// happen did not, and he would try it again.
	if s.detachments != nil {
		_ = s.detachments.Record(detachment.Record{
			ServerID: serverID, DisplayName: name, Address: address,
			Cause: detachment.CauseSelf, At: time.Now(), WorkingCopies: workingCopies,
		})
	}
	return nil
}
