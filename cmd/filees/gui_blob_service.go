package main

import (
	"context"
	"errors"
	control "filees/pkg/control/v1"
	"filees/pkg/guiblob"
)

// GUIBlob transports bytes only. The realm comes from the SSH session.
func (s *realmAliasService) GUIBlob(ctx context.Context, server string, write *guiblob.Write) (guiblob.State, error) {
	profile, ok := s.provisioner.Profile(server)
	if !ok {
		return guiblob.State{}, errors.New("no active profile")
	}
	typ := control.TicketGetGUIBlob
	var payload any = struct{}{}
	if write != nil {
		typ = control.TicketSetGUIBlob
		payload = *write
	}
	result, err := s.exchange(ctx, profile, typ, payload)
	if err != nil {
		return guiblob.State{}, err
	}
	var state guiblob.State
	if err = control.DecodeResultPayload(result.Result, &state); err == nil {
		err = state.Validate()
	}
	return state, err
}
