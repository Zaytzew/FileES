package v1

import "errors"

// Limits include the serialized envelope and its trailing newline. Public
// shares carry file metadata, not contents. Keep ordinary control operations
// small; a declaration and an aggregate listing need separate bounded budgets.
const (
	DefaultMessageBytes = 64 << 10
	MaxShareTicketBytes = 2 << 20
	MaxShareListBytes   = 16 << 20
)

var (
	ErrTicketTooLarge = errors.New("control ticket exceeds limit")
	ErrResultTooLarge = errors.New("repository control result exceeds limit")
)

func TicketByteLimit(kind TicketType) int {
	if kind == TicketCreatePublicShare || kind == TicketUpdatePublicShare {
		return MaxShareTicketBytes
	}
	return DefaultMessageBytes
}

func ResultByteLimit(kind TicketType) int {
	if kind == TicketListPublicShares {
		return MaxShareListBytes
	}
	return DefaultMessageBytes
}
