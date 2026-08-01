package servertool

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"filees/pkg/smtpsubmit"
)

func deliverPendingPublicShareMail(config serverconfig.Config, stderr io.Writer) error {
	if !config.PublicShares.Enabled {
		return nil
	}
	stateRoot := config.PublicShares.EffectiveStateRoot(config.Repositories.ResultsRoot)
	outbox := repoworker.PublicShareOutbox{Root: filepath.Join(stateRoot, "outbox")}
	for delivered := 0; delivered < 128; delivered++ {
		job, ok, err := outbox.Claim(time.Now(), 5*time.Minute)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		message, err := repoworker.RenderPublicShareMail(job, config.SMTPFrom, config.MessageIDDomain, config.PublicShares.BaseURL)
		if err == nil {
			err = smtpSubmit(context.Background(), config.SMTP, smtpsubmit.Request{EnvelopeFrom: config.SMTPFrom, Recipient: job.DeliveryAddress, Message: message})
		}
		if err != nil {
			if markErr := outbox.MarkFailed(job.MessageID, job.AttemptID); markErr != nil {
				return fmt.Errorf("public share mail failed (%v), reset failed: %w", err, markErr)
			}
			fmt.Fprintf(stderr, "public share mail %s: %v\n", job.MessageID, err)
			return err
		}
		if err := outbox.MarkSent(job.MessageID, job.AttemptID); err != nil {
			return err
		}
	}
	return fmt.Errorf("public share mail batch exceeds 128 messages")
}
