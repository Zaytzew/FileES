package main

import (
	"context"
	"errors"
	"filees/pkg/guiblob"
	"github.com/google/uuid"
	"time"
)

type guiBlobClient interface {
	GUIBlob(context.Context, string, *guiblob.Write) (guiblob.State, error)
}

// GUIBlobState exposes a cache scope, never the server's raw realm ID.
type GUIBlobState struct {
	Scope    string `json:"scope"`
	Version  string `json:"version"`
	Data     string `json:"data"`
	Conflict bool   `json:"conflict,omitempty"`
}

func guiScope(server, realm string) string {
	if realm == "" {
		return ""
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("filees.gui.scope/v1\x00"+server+"\x00"+realm)).String()
}
func guiBlobPresentation(server string, state guiblob.State, err error) (GUIBlobState, error) {
	if err != nil {
		return GUIBlobState{}, err
	}
	return GUIBlobState{Scope: guiScope(server, state.RealmID), Version: state.Version, Data: state.Data, Conflict: state.Conflict}, nil
}

// GetGUIBlob and SetGUIBlob expose opaque realm presentation state to Wails.
// No filesystem path, realm selector or synchronization setting comes from JS.
func (s *GUIService) GetGUIBlob(server string) (GUIBlobState, error) {
	state, err := s.exchangeGUIBlob(server, nil)
	return guiBlobPresentation(server, state, err)
}
func (s *GUIService) SetGUIBlob(server, expected, data string) (GUIBlobState, error) {
	write := guiblob.Write{Expected: expected, Data: data}
	if err := write.Validate(); err != nil {
		return GUIBlobState{}, err
	}
	state, err := s.exchangeGUIBlob(server, &write)
	return guiBlobPresentation(server, state, err)
}
func (s *GUIService) exchangeGUIBlob(server string, write *guiblob.Write) (guiblob.State, error) {
	snapshot := s.Snapshot()
	var realm string
	for _, item := range snapshot.Servers {
		if item.ID == server {
			realm = item.RealmID
			break
		}
	}
	if s.guiBlobs == nil || !snapshot.Connected || realm == "" {
		return guiblob.State{}, errors.New("GUI state unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	state, err := s.guiBlobs.GUIBlob(ctx, server, write)
	if err != nil {
		return state, err
	}
	if err = state.Validate(); err != nil {
		return guiblob.State{}, err
	}
	current := s.Snapshot()
	for _, item := range current.Servers {
		if current.Connected && item.ID == server && item.RealmID == realm && state.RealmID == realm {
			return state, nil
		}
	}
	return guiblob.State{}, errors.New("GUI realm changed")
}
