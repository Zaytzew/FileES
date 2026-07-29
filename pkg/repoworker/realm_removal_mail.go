package repoworker

import (
	"errors"
	"fmt"
	"strings"

	"filees/pkg/onboarding"
)

// RenderRealmRemovalMail renders the confirmation notice from a claimed
// outbox record. The server configuration has already validated the message
// ID domain; this defensive check merely prevents an unsafe direct caller.
func RenderRealmRemovalMail(job RealmRemovalMailJob, from, messageIDDomain string) ([]byte, error) {
	if job.DeliveryState != RealmRemovalMailSending || job.DeliveryAddress == "" || job.OTP == "" {
		return nil, errors.New("realm removal mail job is not claimable")
	}
	canonicalFrom, err := onboarding.CanonicalEmail(from)
	if err != nil {
		return nil, fmt.Errorf("SMTP from: %w", err)
	}
	to, err := onboarding.CanonicalEmail(job.DeliveryAddress)
	if err != nil {
		return nil, fmt.Errorf("SMTP recipient: %w", err)
	}
	domain := strings.ToLower(strings.TrimSpace(messageIDDomain))
	if domain == "" || strings.ContainsAny(domain, "@<>\r\n \t") {
		return nil, errors.New("invalid mail message_id_domain")
	}
	if _, err := parseRealmRemovalMailID(job.MessageID); err != nil {
		return nil, err
	}
	body := fmt.Sprintf(
		"A request was made to remove your FileES participation from this server.\r\n\r\n"+
			"This will invalidate all active client activations for your FileES realm and remove its server-side repository participation.\r\n\r\n"+
			"Confirmation code:\r\n\r\n%s\r\n\r\n"+
			"The code expires at %s. If this was not you, ignore this e-mail and contact the server administrator.\r\n",
		job.OTP, job.ExpiresAt.UTC().Format("2006-01-02 15:04:05Z"),
	)
	return []byte(fmt.Sprintf(
		"From: FileES <%s>\r\n"+
			"To: <%s>\r\n"+
			"Date: %s\r\n"+
			"Message-ID: <%s@%s>\r\n"+
			"Subject: FileES participation removal confirmation\r\n"+
			"Auto-Submitted: auto-generated\r\n"+
			"MIME-Version: 1.0\r\n"+
			"Content-Type: text/plain; charset=UTF-8\r\n"+
			"Content-Transfer-Encoding: 8bit\r\n\r\n%s",
		canonicalFrom, to, job.CreatedAt.UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700"), job.MessageID, domain, body,
	)), nil
}

func parseRealmRemovalMailID(id string) (string, error) {
	if len(id) != 36 || strings.ContainsAny(id, "<>@\r\n") {
		return "", errors.New("invalid realm removal mail message_id")
	}
	return id, nil
}
