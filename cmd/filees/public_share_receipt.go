package main

import (
	"context"

	"filees/pkg/clientprofile"
	control "filees/pkg/control/v1"
	"filees/pkg/publicstate"
)

type publicLinksCapability struct {
	profile clientprofile.Profile
	binding publicstate.Binding
}

// This process-local result marker never crosses IPC. The synchronous wrapper
// emits a GUI event without scheduling another server listing after an ACK.
type publicShareReceiptKey struct{}

func withPublicShareReceipt(ctx context.Context) context.Context {
	return context.WithValue(ctx, publicShareReceiptKey{}, new(bool))
}

func publicShareReceiptRequest(payload any, include bool) any {
	switch p := payload.(type) {
	case control.CreatePublicSharePayload:
		p.IncludeState = include
		return p
	case control.UpdatePublicSharePayload:
		p.IncludeState = include
		return p
	case control.RevokePublicSharePayload:
		p.IncludeState = include
		return p
	case control.DeletePublicSharePayload:
		p.IncludeState = include
		return p
	default:
		return payload
	}
}
