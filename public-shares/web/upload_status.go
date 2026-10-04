package web

import (
	"context"
	"encoding/json"
	"net/http"

	"filees/public-shares/authority"
	"filees/public-shares/channel"
)

func (h Handler) uploadStatus(w http.ResponseWriter, request *http.Request, projection channel.UploadProjection, invitation, uploadID string) {
	if !uploadAuthorized(projection, invitation) || (projection.RequireOTP && h.verifyUploadGrant(request.URL.Query().Get("g"), projection, invitation) != nil) {
		h.notFound(w)
		return
	}
	reader, ok := h.Backend.(interface {
		UploadStatus(context.Context, authority.UploadStatusRequest) (channel.UploadStatus, error)
	})
	if !ok {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "Nie można teraz potwierdzić przyjęcia.", http.StatusServiceUnavailable)
		return
	}
	status, err := reader.UploadStatus(request.Context(), authority.UploadStatusRequest{
		Alias: projection.Alias, Slug: projection.Slug, Invitation: invitation, UploadID: uploadID,
	})
	if err != nil || status.UploadID != uploadID || !status.Valid() {
		h.notFound(w)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(status)
}
