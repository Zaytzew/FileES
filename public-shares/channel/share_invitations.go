package channel

import (
	"errors"

	"filees/public-shares/manifest"
	"github.com/google/uuid"
)

// Resend returns the existing credentials, never new grants. A stale owner
// dialog cannot silently change recipients, content, revision or access policy.
func (s *Store) Resend(operationID, requesterRealm, channelID string, declaration manifest.Share) (Record, []Delivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validate(); err != nil {
		return Record{}, nil, err
	}
	if _, err := uuid.Parse(operationID); err != nil {
		return Record{}, nil, errors.New("public share operation_id must be UUID")
	}
	record, err := s.load(channelID)
	if err != nil {
		return Record{}, nil, err
	}
	if record.Manifest == nil || record.OwnerRealm != requesterRealm || record.Manifest.OwnerRealm != requesterRealm {
		return Record{}, nil, ErrForbidden
	}
	if record.State != StateActive {
		return Record{}, nil, ErrInactive
	}
	if err := s.Authority.OwnsActiveRepository(requesterRealm, record.RepoID); err != nil {
		return Record{}, nil, ErrForbidden
	}
	if len(record.Recipients) == 0 {
		return Record{}, nil, ErrPolicy
	}
	if !sameDeclaration(*record.Manifest, declaration) {
		return Record{}, nil, ErrRecordConflict
	}
	deliveries := make([]Delivery, 0, len(record.Recipients))
	for _, credential := range record.Recipients {
		deliveries = append(deliveries, Delivery{Email: credential.Email, Token: s.recipientToken(channelID, credential.Epoch, credential.Email)})
	}
	record.MailOperationID = operationID
	return record, deliveries, nil
}
