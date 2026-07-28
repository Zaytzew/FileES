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
	if entry.DeliveryState != DeliverySending || entry.DeliveryAddress == "" {
		return nil, errors.New("mail job does not contain a claimed onboarding message")
	}
	to, err := canonicalEmail(entry.DeliveryAddress)
	if err != nil {
		return nil, fmt.Errorf("SMTP recipient: %w", err)
	}
	if strings.ContainsAny(entry.MessageID, "<>@\r\n") {
		return nil, errors.New("invalid mail message_id")
	}
	var subject, body string
	switch entry.Template {
	case OTPMailTemplate, "":
		if entry.OTP == "" {
			return nil, errors.New("OTP mail job has no OTP")
		}
		subject = "FileES onboarding code"
		body = fmt.Sprintf("Your one-time FileES tunnel code is:\r\n\r\n%s\r\n\r\nIt expires at %s. It authorizes one reverse tunnel only.\r\n", entry.OTP, job.ExpiresAt.UTC().Format("2006-01-02 15:04:05Z"))
	case InvitationMailTemplate:
		if entry.Invitation == "" {
			return nil, errors.New("invitation mail job has no invitation")
		}
		subject = "FileES activation invitation"
		body = fmt.Sprintf("Open FileES and paste this activation invitation:\r\n\r\n%s\r\n\r\nThe invitation expires at %s. It does not activate anything until you confirm the separate one-time code sent later.\r\n", entry.Invitation, job.ExpiresAt.UTC().Format("2006-01-02 15:04:05Z"))
	default:
		return nil, errors.New("unsupported FileES mail template")
	}
	message := fmt.Sprintf(
		"From: FileES <%s>\r\n"+
			"To: <%s>\r\n"+
			"Date: %s\r\n"+
			"Message-ID: <%s@%s>\r\n"+
			"Subject: %s\r\n"+
			"Auto-Submitted: auto-generated\r\n"+
			"MIME-Version: 1.0\r\n"+
			"Content-Type: text/plain; charset=UTF-8\r\n"+
			"Content-Transfer-Encoding: 8bit\r\n"+
			"\r\n%s",
		canonicalFrom,
		to,
		entry.CreatedAt.UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700"),
		entry.MessageID,
		strings.ToLower(messageIDDomain),
		subject,
		body,
	)
	return []byte(message), nil
}

func CanonicalEmail(value string) (string, error) { return canonicalEmail(value) }
