package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"filees/internal/durable"
	"filees/pkg/clientprofile"
	control "filees/pkg/control/v1"
	"filees/pkg/controlclient"
	"filees/pkg/detachment"
	"filees/pkg/localrepo"
	"filees/pkg/privatefile"

	"github.com/google/uuid"
)

// serverDetachService performs the irreversible final part only after every
// attached working copy has been cleanly quiesced and detached. If local
// detachment or the remote revoke fails, the profile is deliberately kept.
type serverDetachService struct {
	mu          sync.Mutex
	exchange    func(context.Context, clientprofile.Profile, control.Ticket) (control.Result, error)
	local       *localrepo.Store
	provisioner *daemonProvisioner
	profileRoot string
	detachments *detachment.Store
}

func (s *serverDetachService) Detach(ctx context.Context, serverID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.local == nil || s.provisioner == nil || s.detachments == nil {
		return errors.New("server detach service is incomplete")
	}
	profile, ok := s.provisioner.Profile(serverID)
	if !ok {
		if s.detachments.Current(serverID) {
			return nil
		}
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
			if record.RemoteDeletionObserved && !record.LocalCleanupCompleted {
				return fmt.Errorf("repository %s still has pending local metadata cleanup", record.LocalPath)
			}
			// Already detached records retain their local data but no metadata.
		default:
			return fmt.Errorf("repository %s has unfinished lifecycle state %q", record.LocalPath, record.State)
		}
	}
	intent, err := prepareServerDetach(s.profileRoot, profile, workingCopies)
	if err != nil {
		return err
	}
	exchange := s.exchange
	if exchange == nil {
		exchange = func(ctx context.Context, p clientprofile.Profile, ticket control.Ticket) (control.Result, error) {
			transport, err := controlclient.New(controlclient.Config{Address: p.Address, Port: p.SSHPort, IdentityFile: p.IdentityFile, KnownHosts: p.KnownHosts, Timeout: 45 * time.Minute})
			if err != nil {
				return control.Result{}, err
			}
			return transport.Exchange(ctx, ticket)
		}
	}
	result, err := exchange(ctx, profile, intent.Ticket)
	if err != nil && !errors.Is(err, controlclient.ErrIdentityRefused) {
		return err
	}
	// A pinned server's terminal refusal also settles our durable self-detach
	// intent. EOF/timeout alone leaves the intent and credentials for retry.
	refused := err != nil

	if !refused && result.Status != control.ResultOK {
		if result.Error == nil {
			return errors.New("server rejected client detach")
		}
		return fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message)
	}
	// Write the restart fence before deleting credentials. If profile removal
	// is interrupted, startup finishes it from this durable detachment.
	if _, err := s.detachments.RecordFirstNoticed(detachment.Record{
		ServerID: serverID, DisplayName: name, Address: address,
		Cause: detachment.CauseSelf, At: time.Now(), WorkingCopies: intent.WorkingCopies,
	}); err != nil {
		return fmt.Errorf("persist completed server detach: %w", err)
	}
	if err := clientprofile.Remove(s.profileRoot, serverID); err != nil {
		return fmt.Errorf("remove local credentials after server revoke: %w", err)
	}
	s.provisioner.RemoveProfile(serverID)
	return nil
}

// A pending intent is not a detachment fence: only a successful result or a
// terminal server refusal may remove the profile. The ticket survives retries.
type serverDetachIntent struct {
	ServerID      string         `json:"server_id"`
	Ticket        control.Ticket `json:"ticket"`
	WorkingCopies []string       `json:"working_copies"`
}

func serverDetachPath(root string, profile clientprofile.Profile) (string, error) {
	if !filepath.IsAbs(root) {
		return "", errors.New("server detach root must be absolute")
	}
	if _, err := uuid.Parse(profile.ClientID); err != nil {
		return "", err
	}
	name, err := clientprofile.StateDirName(profile.ServerID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name, "client-deactivate.json"), nil
}

func readServerDetach(root string, profile clientprofile.Profile) (serverDetachIntent, error) {
	path, err := serverDetachPath(root, profile)
	if err != nil {
		return serverDetachIntent{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return serverDetachIntent{}, err
	}
	var intent serverDetachIntent
	if err := json.Unmarshal(raw, &intent); err != nil {
		return serverDetachIntent{}, err
	}
	if intent.ServerID != profile.ServerID || intent.Ticket.ClientID != profile.ClientID || intent.Ticket.Type != control.TicketClientDeactivate {
		return serverDetachIntent{}, errors.New("server detach intent does not match activation")
	}
	if err := intent.Ticket.Validate(); err != nil {
		return serverDetachIntent{}, err
	}
	return intent, nil
}

func prepareServerDetach(root string, profile clientprofile.Profile, copies []string) (serverDetachIntent, error) {
	intent, err := readServerDetach(root, profile)
	if err == nil {
		return intent, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return serverDetachIntent{}, err
	}
	path, err := serverDetachPath(root, profile)
	if err != nil {
		return serverDetachIntent{}, err
	}
	operation := uuid.NewString()
	ticket, err := control.NewTicket(operation, uuid.NewSHA1(uuid.NameSpaceOID, []byte(operation+":client-deactivate")).String(), control.TicketClientDeactivate, profile.ClientID, control.ClientDeactivatePayload{}, time.Now())
	if err != nil {
		return serverDetachIntent{}, err
	}
	intent = serverDetachIntent{ServerID: profile.ServerID, Ticket: ticket, WorkingCopies: copies}
	raw, err := json.Marshal(intent)
	if err != nil {
		return serverDetachIntent{}, err
	}
	if err := privatefile.EnsureDir(filepath.Dir(path)); err != nil {
		return serverDetachIntent{}, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".client-deactivate-*")
	if err != nil {
		return serverDetachIntent{}, err
	}
	defer os.Remove(f.Name())
	if err = privatefile.Harden(f.Name()); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return serverDetachIntent{}, err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return serverDetachIntent{}, err
	}
	if err := durable.SyncDirectory(filepath.Dir(path)); err != nil {
		return serverDetachIntent{}, err
	}
	return intent, nil
}
