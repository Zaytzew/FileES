package repoworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filees/pkg/onboarding"
	"filees/public-shares/channel"
	"github.com/google/uuid"
)

const (
	publicShareMailSchema       = "filees.public-share-mail/v2"
	publicShareMailLegacySchema = "filees.public-share-mail/v1"
	PublicShareMailInvitation   = "invitation"
	PublicShareMailOTP          = "otp"
)

type PublicShareMailJob struct {
	Schema          string    `json:"schema"`
	MessageID       string    `json:"message_id"`
	ChannelID       string    `json:"channel_id"`
	Alias           string    `json:"alias"`
	Slug            string    `json:"slug"`
	DeliveryAddress string    `json:"delivery_address"`
	Kind            string    `json:"kind,omitempty"`
	Invitation      string    `json:"invitation,omitempty"`
	Code            string    `json:"code,omitempty"`
	ExpiresAt       time.Time `json:"expires_at,omitempty"`
	Token           string    `json:"token,omitempty"` // v1 compatibility
	State           string    `json:"state"`
	AttemptID       string    `json:"attempt_id,omitempty"`
	LeaseUntil      time.Time `json:"lease_until,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

type PublicShareOutbox struct {
	Root string
	Now  func() time.Time
}

func (o PublicShareOutbox) DeliverPublicShareTokens(_ context.Context, record channel.Record, deliveries []channel.Delivery) error {
	if !filepath.IsAbs(o.Root) {
		return errors.New("public share outbox root must be absolute")
	}
	for _, delivery := range deliveries {
		email, err := onboarding.CanonicalEmail(delivery.Email)
		if err != nil || delivery.Token == "" {
			return errors.New("public share delivery is invalid")
		}
		digest := sha256.Sum256([]byte(record.ChannelID + "\x00" + email + "\x00" + delivery.Token))
		messageID := uuid.NewSHA1(uuid.NameSpaceOID, digest[:]).String()
		job := PublicShareMailJob{Schema: publicShareMailSchema, MessageID: messageID, ChannelID: record.ChannelID, Alias: record.Alias, Slug: record.Slug, DeliveryAddress: email, Kind: PublicShareMailInvitation, Invitation: delivery.Token, State: "pending", CreatedAt: o.now()}
		if err := o.queue(job); err != nil {
			return err
		}
	}
	return nil
}

func (o PublicShareOutbox) DeliverRecipientOTP(record channel.Record, email, invitation, epoch, code string, activatedAt, expiresAt time.Time) error {
	canonical, err := onboarding.CanonicalEmail(email)
	if err != nil || invitation == "" || len(code) != 8 || strings.Trim(code, "0123456789") != "" || activatedAt.IsZero() || !expiresAt.After(activatedAt) {
		return errors.New("public share OTP delivery is invalid")
	}
	if _, err := uuid.Parse(epoch); err != nil {
		return errors.New("public share OTP epoch is invalid")
	}
	digest := sha256.Sum256([]byte(record.ChannelID + "\x00otp\x00" + epoch + "\x00" + canonical))
	job := PublicShareMailJob{
		Schema: publicShareMailSchema, MessageID: uuid.NewSHA1(uuid.NameSpaceOID, digest[:]).String(),
		ChannelID: record.ChannelID, Alias: record.Alias, Slug: record.Slug,
		DeliveryAddress: canonical, Kind: PublicShareMailOTP, Invitation: invitation,
		Code: code, ExpiresAt: expiresAt.UTC(), State: "pending", CreatedAt: activatedAt.UTC(),
	}
	return o.queue(job)
}

func (o PublicShareOutbox) Claim(now time.Time, lease time.Duration) (PublicShareMailJob, bool, error) {
	if !filepath.IsAbs(o.Root) || lease <= 0 {
		return PublicShareMailJob{}, false, errors.New("public share outbox claim is incomplete")
	}
	var claimed PublicShareMailJob
	found := false
	err := WithFileLock(filepath.Join(o.Root, ".lock"), func() error {
		entries, err := os.ReadDir(o.Root)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			job, err := o.load(filepath.Join(o.Root, entry.Name()))
			if err != nil {
				return err
			}
			if job.State == "sending" && now.UTC().Before(job.LeaseUntil) {
				continue
			}
			job.State, job.AttemptID, job.LeaseUntil = "sending", uuid.NewString(), now.UTC().Add(lease)
			if err := atomicJSON(filepath.Join(o.Root, entry.Name()), job); err != nil {
				return err
			}
			claimed, found = job, true
			return nil
		}
		return nil
	})
	return claimed, found, err
}

func (o PublicShareOutbox) MarkFailed(messageID, attemptID string) error {
	return o.finish(messageID, attemptID, false)
}
func (o PublicShareOutbox) MarkSent(messageID, attemptID string) error {
	return o.finish(messageID, attemptID, true)
}

func (o PublicShareOutbox) finish(messageID, attemptID string, sent bool) error {
	if _, err := uuid.Parse(messageID); err != nil || attemptID == "" {
		return errors.New("public share mail completion is invalid")
	}
	return WithFileLock(filepath.Join(o.Root, ".lock"), func() error {
		path := filepath.Join(o.Root, messageID+".json")
		job, err := o.load(path)
		if err != nil {
			return err
		}
		if job.State != "sending" || job.AttemptID != attemptID {
			return errors.New("public share mail attempt is stale")
		}
		if sent {
			return os.Remove(path)
		}
		job.State, job.AttemptID, job.LeaseUntil = "pending", "", time.Time{}
		return atomicJSON(path, job)
	})
}

func RenderPublicShareMail(job PublicShareMailJob, from, domain, baseURL string) ([]byte, error) {
	if err := validatePublicShareMail(job); err != nil || job.State != "sending" {
		return nil, errors.New("public share mail job is invalid")
	}
	canonicalFrom, err := onboarding.CanonicalEmail(from)
	if err != nil {
		return nil, errors.New("public share SMTP from is invalid")
	}
	if domain == "" || strings.ContainsAny(domain, "@<>\r\n \t") {
		return nil, errors.New("public share message ID domain is invalid")
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, errors.New("public share base URL is invalid")
	}
	invitation := job.Invitation
	kind := job.Kind
	if job.Schema == publicShareMailLegacySchema {
		invitation, kind = job.Token, PublicShareMailInvitation
	}
	base.Path = "/" + job.Alias + "/" + job.Slug
	query := base.Query()
	query.Set("invite", invitation)
	base.RawQuery = query.Encode()
	subject := "FileES - pliki do pobrania"
	body := fmt.Sprintf("Pliki udostępnione w FileES są dostępne pod adresem:\r\n\r\n%s\r\n\r\nNa stronie poproś o jednorazowy kod dostępu.\r\n", base.String())
	if kind == PublicShareMailOTP {
		subject = "FileES - kod dostępu"
		body = fmt.Sprintf("Kod dostępu do udostępnionych plików:\r\n\r\n%s\r\n\r\nKod wygasa o %s.\r\n", job.Code, job.ExpiresAt.Local().Format("15:04:05 MST"))
	}
	message := fmt.Sprintf("From: FileES <%s>\r\nTo: <%s>\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\nSubject: %s\r\nAuto-Submitted: auto-generated\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s", canonicalFrom, job.DeliveryAddress, job.CreatedAt.UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700"), job.MessageID, strings.ToLower(domain), subject, body)
	return []byte(message), nil
}

func (o PublicShareOutbox) queue(job PublicShareMailJob) error {
	if err := validatePublicShareMail(job); err != nil {
		return err
	}
	if err := os.MkdirAll(o.Root, 0700); err != nil {
		return err
	}
	return WithFileLock(filepath.Join(o.Root, ".lock"), func() error {
		path := filepath.Join(o.Root, job.MessageID+".json")
		if raw, err := os.ReadFile(path); err == nil {
			var old PublicShareMailJob
			if json.Unmarshal(raw, &old) != nil {
				return errors.New("stored public share mail job is invalid")
			}
			oldRaw, _ := json.Marshal(old)
			newRaw, _ := json.Marshal(job)
			if bytes.Equal(oldRaw, newRaw) {
				return nil
			}
			return errors.New("public share mail job conflicts")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return atomicJSON(path, job)
	})
}

func (o PublicShareOutbox) load(path string) (PublicShareMailJob, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return PublicShareMailJob{}, err
	}
	var job PublicShareMailJob
	if json.Unmarshal(raw, &job) != nil || validatePublicShareMail(job) != nil {
		return PublicShareMailJob{}, errors.New("stored public share mail job is invalid")
	}
	return job, nil
}

func validatePublicShareMail(job PublicShareMailJob) error {
	if job.Schema != publicShareMailSchema && job.Schema != publicShareMailLegacySchema {
		return errors.New("public share mail schema is invalid")
	}
	if _, err := uuid.Parse(job.MessageID); err != nil {
		return err
	}
	if _, err := uuid.Parse(job.ChannelID); err != nil {
		return err
	}
	if _, err := onboarding.CanonicalEmail(job.DeliveryAddress); err != nil {
		return err
	}
	invitation, kind := job.Invitation, job.Kind
	if job.Schema == publicShareMailLegacySchema {
		invitation, kind = job.Token, PublicShareMailInvitation
	}
	if job.Alias == "" || job.Slug == "" || invitation == "" || strings.ContainsAny(job.Alias+job.Slug+invitation, "\x00\r\n/\\") {
		return errors.New("public share mail address is invalid")
	}
	if kind != PublicShareMailInvitation && kind != PublicShareMailOTP {
		return errors.New("public share mail kind is invalid")
	}
	if kind == PublicShareMailOTP && (len(job.Code) != 8 || strings.Trim(job.Code, "0123456789") != "" || job.ExpiresAt.IsZero()) {
		return errors.New("public share OTP mail is invalid")
	}
	if job.State != "pending" && job.State != "sending" {
		return errors.New("public share mail state is invalid")
	}
	if job.State == "sending" && (job.AttemptID == "" || job.LeaseUntil.IsZero()) {
		return errors.New("public share mail lease is invalid")
	}
	if job.State == "pending" && (job.AttemptID != "" || !job.LeaseUntil.IsZero()) {
		return errors.New("pending public share mail has a lease")
	}
	return nil
}

func (o PublicShareOutbox) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}
