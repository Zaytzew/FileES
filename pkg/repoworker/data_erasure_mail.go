package repoworker

import (
	"errors"
	"fmt"
	"strings"

	"filees/pkg/onboarding"
)

func RenderDataErasureCompletionMail(job DataErasureMailJob, from, messageIDDomain string) ([]byte, error) {
	if job.DeliveryState != RealmRemovalMailSending || job.DeliveryAddress == "" {
		return nil, errors.New("data erasure mail job is not claimable")
	}
	if job.FinalState != DataErasureCompleted && job.FinalState != DataErasurePartiallyRetained {
		return nil, errors.New("data erasure mail has invalid final state")
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
	status := "The data-erasure request has been completed."
	if job.FinalState == DataErasurePartiallyRetained {
		status = "The active-data erasure is complete. Some records remain retained under the server retention policy."
	}
	body := status + "\r\n\r\nOperation: " + job.OperationID + "\r\n"
	return []byte(fmt.Sprintf(
		"From: FileES <%s>\r\n"+
			"To: <%s>\r\n"+
			"Date: %s\r\n"+
			"Message-ID: <%s@%s>\r\n"+
			"Subject: FileES data-erasure request completed\r\n"+
			"Auto-Submitted: auto-generated\r\n"+
			"MIME-Version: 1.0\r\n"+
			"Content-Type: text/plain; charset=UTF-8\r\n"+
			"Content-Transfer-Encoding: 8bit\r\n\r\n%s",
		canonicalFrom, to, job.CreatedAt.UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700"),
		job.MessageID, domain, body,
	)), nil
}
