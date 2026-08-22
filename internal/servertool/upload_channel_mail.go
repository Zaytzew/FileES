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

func deliverPendingUploadChannelMail(config serverconfig.Config, stderr io.Writer) error {
	if config.Repositories.ResultsRoot == "" && config.PublicShares.StateRoot == "" {
		return nil
	}
	stateRoot := config.PublicShares.EffectiveStateRoot(config.Repositories.ResultsRoot)
	outbox := repoworker.UploadChannelOutbox{Root: filepath.Join(stateRoot, "upload-outbox")}
	for delivered := 0; delivered < 128; delivered++ {
		job, ok, err := outbox.Claim(time.Now(), 5*time.Minute)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		message, err := repoworker.RenderUploadChannelMail(job, config.SMTPFrom, config.MessageIDDomain, config.PublicShares.BaseURL)
		if err == nil {
			err = smtpSubmit(context.Background(), config.SMTP, smtpsubmit.Request{EnvelopeFrom: config.SMTPFrom, Recipient: job.DeliveryAddress, Message: message})
		}
		if err != nil {
			if smtpsubmit.IsTemporary(err) {
				if markErr := outbox.MarkFailed(job.MessageID, job.AttemptID); markErr != nil {
					return fmt.Errorf("upload channel mail failed (%v), reset failed: %w", err, markErr)
				}
				fmt.Fprintf(stderr, "upload channel mail %s: %v\n", job.MessageID, err)
				return err
			}
			if markErr := outbox.MarkRejected(job.MessageID, job.AttemptID); markErr != nil {
				return fmt.Errorf("upload channel mail failed (%v), reject failed: %w", err, markErr)
			}
			fmt.Fprintf(stderr, "upload channel mail %s: %v\n", job.MessageID, err)
			continue
		}
		if err := outbox.MarkSent(job.MessageID, job.AttemptID); err != nil {
			return err
		}
	}
	return fmt.Errorf("upload channel mail batch exceeds 128 messages")
}

func deliverOneUploadChannelMail(config serverconfig.Config, stdout, stderr io.Writer) int {
	stateRoot := config.PublicShares.EffectiveStateRoot(config.Repositories.ResultsRoot)
	if stateRoot == "." || stateRoot == "" {
		_ = writeJSON(stdout, map[string]string{"schema": "filees.mail-result/v1", "status": "no_work"})
		return ExitOK
	}
	outbox := repoworker.UploadChannelOutbox{Root: filepath.Join(stateRoot, "upload-outbox")}
	job, ok, err := outbox.Claim(time.Now(), 5*time.Minute)
	if err != nil {
		report(stderr, "filees-mail upload channel claim", err)
		return ExitTempFail
	}
	if !ok {
		_ = writeJSON(stdout, map[string]string{"schema": "filees.mail-result/v1", "status": "no_work"})
		return ExitOK
	}
	message, err := repoworker.RenderUploadChannelMail(job, config.SMTPFrom, config.MessageIDDomain, config.PublicShares.BaseURL)
	if err != nil {
		_ = outbox.MarkFailed(job.MessageID, job.AttemptID)
		report(stderr, "filees-mail upload channel render", err)
		return ExitData
	}
	err = smtpSubmit(context.Background(), config.SMTP, smtpsubmit.Request{EnvelopeFrom: config.SMTPFrom, Recipient: job.DeliveryAddress, Message: message})
	if err != nil {
		temporary := smtpsubmit.IsTemporary(err)
		mark := outbox.MarkFailed
		if !temporary {
			mark = outbox.MarkRejected
		}
		if markErr := mark(job.MessageID, job.AttemptID); markErr != nil {
			report(stderr, "filees-mail upload channel record failure", markErr)
			return ExitTempFail
		}
		report(stderr, "filees-mail upload channel submit", err)
		if temporary {
			return ExitTempFail
		}
		return ExitUnavailable
	}
	if err := outbox.MarkSent(job.MessageID, job.AttemptID); err != nil {
		report(stderr, "filees-mail upload channel record acceptance", err)
		return ExitTempFail
	}
	if err := writeJSON(stdout, map[string]string{"schema": "filees.mail-result/v1", "status": "queued", "kind": "upload_channel_invitation", "message_id": job.MessageID}); err != nil {
		return ExitSoftware
	}
	return ExitOK
}
