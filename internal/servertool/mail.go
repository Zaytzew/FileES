package servertool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"filees/pkg/onboarding"
	"filees/pkg/serverconfig"
	"filees/pkg/smtpsubmit"
)

func RunMail(args []string, stdout, stderr io.Writer) int {
	path, args, err := configPath(args)
	if err != nil || len(args) != 1 || args[0] != "send" {
		fmt.Fprintln(stderr, "usage: filees-mail [-config path] send")
		return ExitUsage
	}
	files, config, err := openFiles(path, toolAccess{name: "filees-mail/send", areas: onboarding.AreaTickets | onboarding.AreaOperations | onboarding.AreaAudit, write: true, needSMTP: true})
	if err != nil {
		report(stderr, "filees-mail config", err)
		return ExitConfig
	}
	return deliverPendingMail(files, config, stdout, stderr)
}

func deliverPendingMail(files *onboarding.Files, config serverconfig.Config, stdout, stderr io.Writer) int {
	job, err := files.ClaimPendingMail(5 * time.Minute)
	if errors.Is(err, onboarding.ErrNotFound) {
		_ = writeJSON(stdout, map[string]string{"schema": "filees.mail-result/v1", "status": "no_work"})
		return ExitOK
	}
	if err != nil {
		report(stderr, "filees-mail claim", err)
		return ExitTempFail
	}
	message, err := onboarding.RenderMail(job, config.SMTPFrom, config.MessageIDDomain)
	if err != nil {
		_ = files.MarkOutboxFailed(job.Entry.MessageID, job.Entry.AttemptID, err.Error(), true)
		report(stderr, "filees-mail render", err)
		return ExitData
	}
	err = smtpSubmit(context.Background(), config.SMTP, smtpsubmit.Request{EnvelopeFrom: config.SMTPFrom, Recipient: job.Entry.DeliveryAddress, Message: message})
	if err != nil {
		temporary := smtpsubmit.IsTemporary(err)
		if updateErr := files.MarkOutboxFailed(job.Entry.MessageID, job.Entry.AttemptID, err.Error(), !temporary); updateErr != nil {
			report(stderr, "filees-mail record failure", updateErr)
			return ExitTempFail
		}
		report(stderr, "filees-mail submit", err)
		if temporary {
			return ExitTempFail
		}
		return ExitUnavailable
	}
	if err := files.MarkOutboxQueued(job.Entry.MessageID, job.Entry.AttemptID); err != nil {
		report(stderr, "filees-mail record acceptance", err)
		return ExitTempFail
	}
	if err := writeJSON(stdout, map[string]string{"schema": "filees.mail-result/v1", "status": "queued", "message_id": job.Entry.MessageID}); err != nil {
		return ExitSoftware
	}
	return ExitOK
}

var smtpSubmit = smtpsubmit.Submit
