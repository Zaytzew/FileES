package repoworker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	control "filees/pkg/control/v1"
	"filees/pkg/guiblob"
	"github.com/google/uuid"
)

// GUIBlobStore keeps one opaque document per authenticated realm. It never
// interprets drawer names, memberships or other GUI semantics.
type GUIBlobStore struct{ Root string }

func (s GUIBlobStore) Exchange(ctx context.Context, realm string, write *guiblob.Write) (state guiblob.State, err error) {
	if _, err = uuid.Parse(realm); err != nil {
		return state, err
	}
	if !filepath.IsAbs(s.Root) {
		return state, errors.New("GUI blob root must be absolute")
	}
	if write != nil {
		if err = write.Validate(); err != nil {
			return state, err
		}
	}
	if err = os.MkdirAll(s.Root, 0700); err != nil {
		return state, err
	}
	err = WithFileLock(filepath.Join(s.Root, realm+".lock"), func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		state = guiblob.State{RealmID: realm}
		path := filepath.Join(s.Root, realm+".json")
		raw, e := os.ReadFile(path)
		if e == nil {
			if len(raw) > guiblob.MaxBytes*6+1024 {
				return errors.New("GUI blob envelope too large")
			}
			if e = json.Unmarshal(raw, &state); e != nil {
				return e
			}
			if e = state.Validate(); e != nil {
				return e
			}
			if state.RealmID != realm || state.Conflict {
				return errors.New("GUI blob identity mismatch")
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if write == nil {
			return nil
		}
		if write.Expected != state.Version {
			state.Conflict = true
			return nil
		}
		if write.Data == state.Data {
			return nil
		}
		state.Version = uuid.NewString()
		state.Data = write.Data
		return atomicJSON(path, state)
	})
	return state, err
}

func (w *Worker) guiBlob(ctx context.Context, session Session, ticket control.Ticket) (control.Result, error) {
	if w.GUIBlobs == nil {
		return w.failure(ticket, "GUI_BLOB_UNAVAILABLE", "GUI state is unavailable")
	}
	var write *guiblob.Write
	if ticket.Type == control.TicketSetGUIBlob {
		write = &guiblob.Write{}
		if err := control.DecodePayload(ticket.Payload, write); err != nil {
			return control.Result{}, err
		}
	}
	state, err := w.GUIBlobs.Exchange(ctx, session.RealmID, write)
	if err != nil {
		return w.failure(ticket, "GUI_BLOB_REJECTED", "GUI state could not be stored or read")
	}
	return control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, state, w.now())
}

// DeleteRealm removes GUI state after the realm credentials have been fenced.
// Keep the lock inode stable for other workers already waiting on it.
func (s GUIBlobStore) DeleteRealm(realm string) error {
	if _, err := uuid.Parse(realm); err != nil {
		return err
	}
	if !filepath.IsAbs(s.Root) {
		return errors.New("GUI blob root must be absolute")
	}
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return err
	}
	return WithFileLock(filepath.Join(s.Root, realm+".lock"), func() error {
		if err := os.Remove(filepath.Join(s.Root, realm+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return syncDirectory(s.Root)
	})
}
