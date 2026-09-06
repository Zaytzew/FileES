package v1

import "errors"

const TicketPreparePassportReplacement TicketType = "PREPARE_PASSPORT_REPLACEMENT"

// The authenticated SSH session supplies the actor. No realm or holder input.
type PreparePassportReplacementPayload struct {
	RepoID         string `json:"repo_id"`
	Path           string `json:"path"`
	ObservedLockID string `json:"observed_lock_id"`
	PassportID     string `json:"passport_id"`
	InstanceUID    string `json:"instance_uid"`
	Mode           string `json:"mode"`
}

// Prepared confirms release of the observed token, NOT possession of a new lock.
type PreparePassportReplacementResult struct {
	RepoID         string `json:"repo_id"`
	Path           string `json:"path"`
	ObservedLockID string `json:"observed_lock_id"`
	State          string `json:"state"`
}

func (p PreparePassportReplacementPayload) Validate() error {
	for field, value := range map[string]string{"repo_id": p.RepoID, "passport_id": p.PassportID, "instance_uid": p.InstanceUID} {
		if err := validateUUID(field, value); err != nil {
			return err
		}
	}
	if err := validateLockReleasePath(p.Path); err != nil {
		return err
	}
	if err := validateObservedLockID(p.ObservedLockID); err != nil {
		return err
	}
	if p.Mode != "renew" && p.Mode != "migrate" {
		return errors.New("passport replacement mode must be renew or migrate")
	}
	return nil
}

func (p PreparePassportReplacementResult) Validate() error {
	if err := validateUUID("repo_id", p.RepoID); err != nil {
		return err
	}
	if err := validateLockReleasePath(p.Path); err != nil {
		return err
	}
	if err := validateObservedLockID(p.ObservedLockID); err != nil {
		return err
	}
	if p.State != "prepared" {
		return errors.New("passport preparation is not a reservation")
	}
	return nil
}
