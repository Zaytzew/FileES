package repoworker

import (
	"crypto/sha256"
	"encoding/hex"
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
	"filees/public-shares/manifest"
	"github.com/google/uuid"
)

func (o UploadChannelOutbox) Claim(now time.Time, lease time.Duration) (UploadChannelMailJob, bool, error) {
	if !filepath.IsAbs(o.Root) || lease <= 0 {
		return UploadChannelMailJob{}, false, errors.New("upload channel outbox claim is incomplete")
	}
	if _, err := os.Stat(o.Root); errors.Is(err, os.ErrNotExist) {
		return UploadChannelMailJob{}, false, nil
	} else if err != nil {
		return UploadChannelMailJob{}, false, err
	}
	var claimed UploadChannelMailJob
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
			if job.ResendOperationID != "" {
				if digest, err := o.sentDigest(job.MessageID); err == nil {
					if digest != uploadMailDigest(job) {
						return errors.New("upload channel resend conflicts with completed mail")
					}
					if err := os.Remove(filepath.Join(o.Root, entry.Name())); err != nil {
						return err
					}
					continue
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			if job.State == "failed" {
				continue
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

func (o UploadChannelOutbox) MarkFailed(messageID, attemptID string) error {
	return o.finish(messageID, attemptID, "pending")
}

func (o UploadChannelOutbox) MarkRejected(messageID, attemptID string) error {
	return o.finish(messageID, attemptID, "failed")
}

func (o UploadChannelOutbox) MarkSent(messageID, attemptID string) error {
	return o.finish(messageID, attemptID, "sent")
}

func (o UploadChannelOutbox) finish(messageID, attemptID, outcome string) error {
	if _, err := uuid.Parse(messageID); err != nil || attemptID == "" {
		return errors.New("upload channel mail completion is invalid")
	}
	return WithFileLock(filepath.Join(o.Root, ".lock"), func() error {
		path := filepath.Join(o.Root, messageID+".json")
		job, err := o.load(path)
		if err != nil {
			return err
		}
		if job.State != "sending" || job.AttemptID != attemptID {
			return errors.New("upload channel mail attempt is stale")
		}
		switch outcome {
		case "sent":
			if job.ResendOperationID != "" {
				if err := os.MkdirAll(filepath.Join(o.Root, "sent"), 0700); err != nil {
					return err
				}
				if err := atomicJSON(filepath.Join(o.Root, "sent", messageID+".json"), map[string]string{"digest": uploadMailDigest(job)}); err != nil {
					return err
				}
			}
			return os.Remove(path)
		case "failed":
			job.State, job.AttemptID, job.LeaseUntil = "failed", "", time.Time{}
			return atomicJSON(path, job)
		case "pending":
			job.State, job.AttemptID, job.LeaseUntil = "pending", "", time.Time{}
			return atomicJSON(path, job)
		default:
			return errors.New("upload channel mail completion is invalid")
		}
	})
}

func RenderUploadChannelMail(job UploadChannelMailJob, from, domain, baseURL string) ([]byte, error) {
	if err := validateUploadChannelMail(job); err != nil || job.State != "sending" {
		return nil, errors.New("upload channel mail job is invalid")
	}
	canonicalFrom, err := onboarding.CanonicalEmail(from)
	if err != nil {
		return nil, errors.New("upload channel SMTP from is invalid")
	}
	if domain == "" || strings.ContainsAny(domain, "@<>\r\n \t") {
		return nil, errors.New("upload channel message ID domain is invalid")
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, errors.New("upload channel base URL is invalid")
	}
	base.Path = "/" + job.Alias + "/" + job.Slug
	query := base.Query()
	query.Set("invite", job.Invitation)
	base.RawQuery = query.Encode()
	subject := "FileES - półka na pliki"
	body := fmt.Sprintf("Na wskazanej półce FileES możesz położyć plik:\r\n\r\n%s\r\n\r\nLink jest przeznaczony tylko dla Ciebie.\r\n", base.String())
	if job.InvitationMessage != "" {
		text := strings.ReplaceAll(strings.ReplaceAll(job.InvitationMessage, "\r\n", "\n"), "\r", "\n")
		body += "\r\nWiadomość od właściciela półki:\r\n" + strings.ReplaceAll(text, "\n", "\r\n") + "\r\n"
	}
	message := fmt.Sprintf("From: FileES <%s>\r\nTo: <%s>\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\nSubject: %s\r\nAuto-Submitted: auto-generated\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s", canonicalFrom, job.DeliveryAddress, job.CreatedAt.UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700"), job.MessageID, strings.ToLower(domain), subject, body)
	return []byte(message), nil
}

func (o UploadChannelOutbox) queue(job UploadChannelMailJob) error {
	if err := validateUploadChannelMail(job); err != nil {
		return err
	}
	if err := os.MkdirAll(o.Root, 0700); err != nil {
		return err
	}
	return WithFileLock(filepath.Join(o.Root, ".lock"), func() error {
		path := filepath.Join(o.Root, job.MessageID+".json")
		if job.ResendOperationID != "" {
			if digest, err := o.sentDigest(job.MessageID); err == nil {
				if digest == uploadMailDigest(job) {
					return nil
				}
				return errors.New("upload channel resend conflicts with completed mail")
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if raw, err := os.ReadFile(path); err == nil {
			var old UploadChannelMailJob
			if json.Unmarshal(raw, &old) != nil {
				return errors.New("stored upload channel mail job is invalid")
			}
			if sameUploadMail(old, job) {
				return nil
			}
			return errors.New("upload channel mail job conflicts")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return atomicJSON(path, job)
	})
}

func sameUploadMail(a, b UploadChannelMailJob) bool {
	return uploadMailDigest(a) == uploadMailDigest(b)
}

func uploadMailDigest(a UploadChannelMailJob) string {
	// Lease/state/time change during delivery, but never the requested content.
	a.State, a.AttemptID, a.LeaseUntil, a.CreatedAt = "", "", time.Time{}, time.Time{}
	x, _ := json.Marshal(a)
	digest := sha256.Sum256(x)
	return hex.EncodeToString(digest[:])
}

func (o UploadChannelOutbox) sentDigest(messageID string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(o.Root, "sent", messageID+".json"))
	if err != nil {
		return "", err
	}
	var receipt struct {
		Digest string `json:"digest"`
	}
	if json.Unmarshal(raw, &receipt) != nil || len(receipt.Digest) != 64 {
		return "", errors.New("upload channel mail receipt is invalid")
	}
	return receipt.Digest, nil
}

func (o UploadChannelOutbox) load(path string) (UploadChannelMailJob, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return UploadChannelMailJob{}, err
	}
	var job UploadChannelMailJob
	if json.Unmarshal(raw, &job) != nil || validateUploadChannelMail(job) != nil {
		return UploadChannelMailJob{}, errors.New("stored upload channel mail job is invalid")
	}
	return job, nil
}

func validateUploadChannelMail(job UploadChannelMailJob) error {
	if err := manifest.ValidateInvitationMessage(job.InvitationMessage); err != nil {
		return err
	}
	if job.ResendOperationID != "" {
		if _, err := uuid.Parse(job.ResendOperationID); err != nil {
			return err
		}
	}
	if job.Schema != uploadChannelMailSchema {
		return errors.New("upload channel mail schema is invalid")
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
	if job.Alias == "" || job.Slug == "" || job.Invitation == "" || strings.ContainsAny(job.Alias+job.Slug+job.Invitation, "\x00\r\n/\\") {
		return errors.New("upload channel mail address is invalid")
	}
	if job.State != "pending" && job.State != "sending" && job.State != "failed" {
		return errors.New("upload channel mail state is invalid")
	}
	if job.State == "sending" && (job.AttemptID == "" || job.LeaseUntil.IsZero()) {
		return errors.New("upload channel mail lease is invalid")
	}
	if (job.State == "pending" || job.State == "failed") && (job.AttemptID != "" || !job.LeaseUntil.IsZero()) {
		return errors.New("pending upload channel mail has a lease")
	}
	return nil
}
