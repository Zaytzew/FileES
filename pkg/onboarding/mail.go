package onboarding

import (
	"errors"
	"fmt"
	"strings"
)

func RenderMail(job MailJob, from, messageIDDomain string) ([]byte, error) {
	canonicalFrom, err := canonicalEmail(from)
	if err != nil {
		return nil, fmt.Errorf("SMTP from: %w", err)
	}
	if !validDomain(messageIDDomain) {
		return nil, errors.New("message_id_domain must be a DNS name")
	}
	entry := job.Entry
	if entry.DeliveryState != DeliverySending || entry.DeliveryAddress == "" || entry.OTP == "" {
		return nil, errors.New("mail job does not contain a claimed onboarding message")
	}
	to, err := canonicalEmail(entry.DeliveryAddress)
	if err != nil {
		return nil, fmt.Errorf("SMTP recipient: %w", err)
	}
	if strings.ContainsAny(entry.MessageID, "<>@\r\n") {
		return nil, errors.New("invalid mail message_id")
	}
	message := fmt.Sprintf(
		"From: FileES <%s>\r\n"+
			"To: <%s>\r\n"+
			"Date: %s\r\n"+
			"Message-ID: <%s@%s>\r\n"+
			"Subject: FileES onboarding code\r\n"+
			"Auto-Submitted: auto-generated\r\n"+
			"MIME-Version: 1.0\r\n"+
			"Content-Type: text/plain; charset=UTF-8\r\n"+
			"Content-Transfer-Encoding: 8bit\r\n"+
			"\r\n"+
			"Your one-time FileES tunnel code is:\r\n\r\n%s\r\n\r\n"+
			"It expires at %s. It authorizes one reverse tunnel only.\r\n",
		canonicalFrom,
		to,
		entry.CreatedAt.UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700"),
		entry.MessageID,
		strings.ToLower(messageIDDomain),
		entry.OTP,
		job.ExpiresAt.UTC().Format("2006-01-02 15:04:05Z"),
	)
	return []byte(message), nil
}

func CanonicalEmail(value string) (string, error) { return canonicalEmail(value) }
