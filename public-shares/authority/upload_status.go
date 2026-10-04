package authority

import (
	"context"
	"crypto/hmac"

	"filees/public-shares/channel"
	"filees/public-shares/gate"
)

type UploadStatusRequest struct {
	Alias      string `json:"alias"`
	Slug       string `json:"slug"`
	Invitation string `json:"invitation"`
	UploadID   string `json:"upload_id"`
}

func (r Resolver) UploadStatus(_ context.Context, request UploadStatusRequest) (channel.UploadStatus, error) {
	projection, err := r.InspectUpload(request.Alias, request.Slug)
	if err != nil || request.Invitation == "" {
		return channel.UploadStatus{}, ErrNotFound
	}
	hash := gate.TokenHash(request.Invitation)
	authorized := false
	for _, recipient := range projection.Recipients {
		authorized = hmac.Equal([]byte(recipient.InvitationHash), []byte(hash)) || authorized
	}
	if !authorized {
		return channel.UploadStatus{}, ErrNotFound
	}
	status, err := r.Channels.ReadUploadStatus(projection.ChannelID, request.UploadID, hash)
	if err != nil {
		return channel.UploadStatus{}, ErrNotFound
	}
	return status, nil
}
