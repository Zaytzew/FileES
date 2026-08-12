// Package recipientotp owns the short-lived, e-mail delivered credential for
// recipient-restricted Public Shares. The opaque invitation in the URL only
// selects a mailbox; it never authorizes a visit by itself.
package recipientotp

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filees/internal/durable"
	"filees/pkg/repoworker"
	"filees/public-shares/channel"
	"github.com/google/uuid"
)

const (
	stateSchema     = "filees.public-share-recipient-otp/v1"
	DefaultTTL      = 5 * time.Minute
	DefaultCooldown = 30 * time.Second
	DefaultAttempts = 5
)

var ErrDenied = errors.New("recipient OTP request denied")

type Request struct {
	Alias      string `json:"alias"`
	Slug       string `json:"slug"`
	Invitation string `json:"invitation"`
}

type VerifyRequest struct {
	Request
	Code string `json:"code"`
}

type Grant struct {
	InvitationHash string    `json:"invitation_sha256"`
	Epoch          string    `json:"epoch"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type state struct {
	Schema         string    `json:"schema"`
	ChannelID      string    `json:"channel_id"`
	InvitationHash string    `json:"invitation_sha256"`
	Epoch          string    `json:"epoch"`
	ActivatedAt    time.Time `json:"activated_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	LastSentAt     time.Time `json:"last_sent_at,omitempty"`
	FailedAttempts int       `json:"failed_attempts"`
}

type Service struct {
	Root        string
	Key         []byte
	Channels    *channel.Store
	Outbox      repoworker.PublicShareOutbox
	TTL         time.Duration
	Cooldown    time.Duration
	MaxAttempts int
	Now         func() time.Time
}

func (s Service) RequestCode(request Request) error {
	record, recipient, digest, err := s.recipient(request)
	if err != nil {
		return ErrDenied
	}
	if err := s.validate(); err != nil {
		return err
	}
	now := s.now()
	return repoworker.WithFileLock(filepath.Join(s.Root, ".lock"), func() error {
		current, err := s.load(record.ChannelID, digest)
		if errors.Is(err, os.ErrNotExist) || (err == nil && !now.Before(current.ExpiresAt)) {
			current = state{
				Schema: stateSchema, ChannelID: record.ChannelID, InvitationHash: digest,
				Epoch: uuid.NewString(), ActivatedAt: now, ExpiresAt: now.Add(s.ttl()),
			}
			if err := s.store(current); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if !current.LastSentAt.IsZero() && now.Sub(current.LastSentAt) < s.cooldown() {
			return nil
		}
		code := s.code(current)
		if err := s.Outbox.DeliverRecipientOTP(record, recipient.Email, request.Invitation, current.Epoch, code, current.ActivatedAt, current.ExpiresAt); err != nil {
			return err
		}
		current.LastSentAt = now
		return s.store(current)
	})
}

func (s Service) Verify(request VerifyRequest) (Grant, error) {
	record, _, digest, err := s.recipient(request.Request)
	if err != nil || len(request.Code) != 8 || strings.Trim(request.Code, "0123456789") != "" {
		return Grant{}, ErrDenied
	}
	if err := s.validate(); err != nil {
		return Grant{}, err
	}
	now := s.now()
	var grant Grant
	err = repoworker.WithFileLock(filepath.Join(s.Root, ".lock"), func() error {
		current, err := s.load(record.ChannelID, digest)
		if err != nil || !now.Before(current.ExpiresAt) || current.FailedAttempts >= s.attempts() {
			return ErrDenied
		}
		want := s.code(current)
		if subtle.ConstantTimeCompare([]byte(want), []byte(request.Code)) != 1 {
			current.FailedAttempts++
			if err := s.store(current); err != nil {
				return err
			}
			return ErrDenied
		}
		grant = Grant{InvitationHash: digest, Epoch: current.Epoch, ExpiresAt: current.ExpiresAt}
		return nil
	})
	if err != nil {
		return Grant{}, err
	}
	return grant, nil
}

func (s Service) recipient(request Request) (channel.Record, channel.RecipientCredential, string, error) {
	if s.Channels == nil || request.Invitation == "" || len(request.Invitation) > 256 || strings.ContainsAny(request.Invitation, "\x00\r\n") {
		return channel.Record{}, channel.RecipientCredential{}, "", ErrDenied
	}
	record, err := s.Channels.ResolveAddress(request.Alias, request.Slug)
	if err != nil {
		return channel.Record{}, channel.RecipientCredential{}, "", ErrDenied
	}
	digestRaw := sha256.Sum256([]byte(request.Invitation))
	digest := hex.EncodeToString(digestRaw[:])
	matched := -1
	for index := range record.Recipients {
		if len(record.Recipients[index].TokenHash) == len(digest) && subtle.ConstantTimeCompare([]byte(record.Recipients[index].TokenHash), []byte(digest)) == 1 {
			matched = index
		}
	}
	if matched < 0 {
		return channel.Record{}, channel.RecipientCredential{}, "", ErrDenied
	}
	return record, record.Recipients[matched], digest, nil
}

func (s Service) code(current state) string {
	mac := hmac.New(sha256.New, s.Key)
	fmt.Fprintf(mac, "filees public share recipient otp v1\x00%s\x00%s\x00%s", current.ChannelID, current.InvitationHash, current.Epoch)
	value := uint64(0)
	for _, octet := range mac.Sum(nil)[:8] {
		value = value<<8 | uint64(octet)
	}
	return fmt.Sprintf("%08d", value%100000000)
}

func (s Service) load(channelID, digest string) (state, error) {
	raw, err := os.ReadFile(s.path(channelID, digest))
	if err != nil {
		return state{}, err
	}
	var current state
	if json.Unmarshal(raw, &current) != nil || current.Schema != stateSchema || current.ChannelID != channelID || current.InvitationHash != digest {
		return state{}, errors.New("stored recipient OTP state is invalid")
	}
	if _, err := uuid.Parse(current.Epoch); err != nil || current.ActivatedAt.IsZero() || !current.ExpiresAt.After(current.ActivatedAt) || current.FailedAttempts < 0 || current.FailedAttempts > s.attempts() {
		return state{}, errors.New("stored recipient OTP state is invalid")
	}
	return current, nil
}

func (s Service) store(current state) error {
	dir := filepath.Dir(s.path(current.ChannelID, current.InvitationHash))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(current)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".otp-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(append(raw, '\n'))
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, s.path(current.ChannelID, current.InvitationHash)); err != nil {
		return err
	}
	return durable.SyncDirectory(dir)
}

func (s Service) path(channelID, digest string) string {
	return filepath.Join(s.Root, channelID, digest+".json")
}

func (s Service) validate() error {
	if !filepath.IsAbs(s.Root) || len(s.Key) < 32 || s.Channels == nil || !filepath.IsAbs(s.Outbox.Root) || s.ttl() <= 0 || s.cooldown() < 0 || s.attempts() < 1 {
		return errors.New("recipient OTP service is incomplete")
	}
	if err := os.MkdirAll(s.Root, 0700); err != nil {
		return err
	}
	return nil
}

func (s Service) ttl() time.Duration {
	if s.TTL > 0 {
		return s.TTL
	}
	return DefaultTTL
}
func (s Service) cooldown() time.Duration {
	if s.Cooldown > 0 {
		return s.Cooldown
	}
	return DefaultCooldown
}
func (s Service) attempts() int {
	if s.MaxAttempts > 0 {
		return s.MaxAttempts
	}
	return DefaultAttempts
}
func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
