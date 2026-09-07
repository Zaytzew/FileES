package v1

import (
	"errors"
	"strings"
)

const (
	TicketArmPassportAcquisition    TicketType = "ARM_PASSPORT_ACQUISITION"
	TicketSettlePassportAcquisition TicketType = "SETTLE_PASSPORT_ACQUISITION"
	TicketExpirePassportPath        TicketType = "EXPIRE_PASSPORT_PATH"
)

// Identity and executor PID never come from this payload.
type PassportExecutionPayload struct {
	RepoID  string `json:"repo_id"`
	Path    string `json:"path"`
	Comment string `json:"comment,omitempty"`
}

func (p PassportExecutionPayload) Validate(expire bool) error {
	if err := validateUUID("repo_id", p.RepoID); err != nil {
		return err
	}
	if err := validateLockReleasePath(p.Path); err != nil {
		return err
	}
	if expire {
		if p.Comment != "" {
			return errors.New("expiry cannot select an acquisition comment")
		}
	} else if p.Comment == "" || len(p.Comment) > 4096 || strings.ContainsAny(p.Comment, "\x00\r\n") {
		return errors.New("invalid acquisition comment")
	}
	return nil
}

type PassportExecutionResult struct {
	RepoID  string `json:"repo_id"`
	Path    string `json:"path"`
	Comment string `json:"comment,omitempty"`
	State   string `json:"state"`
}

func (p PassportExecutionResult) Validate(kind TicketType) error {
	if err := (PassportExecutionPayload{p.RepoID, p.Path, p.Comment}).Validate(kind == TicketExpirePassportPath); err != nil {
		return err
	}
	want := "armed"
	if kind == TicketSettlePassportAcquisition {
		want = "closed"
	}
	if kind == TicketExpirePassportPath {
		want = "checked"
	}
	if p.State != want {
		return errors.New("invalid acquisition receipt state")
	}
	return nil
}
