package servertool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"filees/internal/obsandbox"
	"filees/pkg/onboarding"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"filees/pkg/smtpsubmit"
)

func RunMail(args []string, stdout, stderr io.Writer) int {
	path, args, err := configPath(args)
	if err != nil || len(args) != 1 || (args[0] != "send" && args[0] != "public-loop") {
		fmt.Fprintln(stderr, "usage: filees-mail [-config path] send|public-loop")
		return ExitUsage
	}
	if args[0] == "public-loop" {
		return runPublicShareMailLoop(path, stderr)
	}
	files, config, err := openFiles(path, toolAccess{name: "filees-mail/send", areas: onboarding.AreaTickets | onboarding.AreaOperations | onboarding.AreaAudit, write: true, needSMTP: true, needRepoResults: true})
	if err != nil {
		report(stderr, "filees-mail config", err)
		return ExitConfig
	}
	return deliverPendingMail(files, config, stdout, stderr)
}

func runPublicShareMailLoop(path string, stderr io.Writer) int {
	if err := sandboxBegin(mailPromises); err != nil {
		report(stderr, "filees-mail public sandbox bootstrap", err)
		return ExitSoftware
	}
	config, err := serverconfig.LoadFor(path, serverconfig.SecretSMTP)
	if err != nil {
		report(stderr, "filees-mail public config", err)
		return ExitConfig
	}
	if !config.PublicShares.Enabled {
		report(stderr, "filees-mail public config", errors.New("public shares are disabled"))
		return ExitConfig
	}
	stateRoot := config.PublicShares.EffectiveStateRoot(config.Repositories.ResultsRoot)
	if err := os.MkdirAll(stateRoot, 0700); err != nil {
		report(stderr, "filees-mail public state", err)
		return ExitConfig
	}
	profile := obsandbox.Profile{Name: "filees-mail/public-loop", Promises: mailPromises, Paths: []obsandbox.Path{
		{Label: "public-share-state", Name: stateRoot, Perms: "rwc"},
		{Label: "resolver", Name: "/etc/resolv.conf", Perms: "r"},
		{Label: "hosts", Name: "/etc/hosts", Perms: "r"},
	}}
	if err := sandboxApply(profile); err != nil {
		report(stderr, "filees-mail public sandbox", err)
		return ExitSoftware
	}
	for {
		wait := time.Second
		if err := deliverPendingPublicShareMail(config, stderr); err != nil {
			report(stderr, "filees-mail public delivery", err)
			wait = 10 * time.Second
		}
		if err := deliverPendingUploadChannelMail(config, stderr); err != nil {
			report(stderr, "filees-mail upload channel delivery", err)
			wait = 10 * time.Second
		}
		time.Sleep(wait)
	}
}

func deliverPendingMail(files *onboarding.Files, config serverconfig.Config, stdout, stderr io.Writer) int {
	job, err := files.ClaimPendingMail(5 * time.Minute)
	if errors.Is(err, onboarding.ErrNotFound) {
		return deliverPendingRealmRemovalMail(config, stdout, stderr)
	}
	if err != nil {
		report(stderr, "filees-mail claim", err)
		return ExitTempFail
	}
	message, err := onboarding.RenderMail(job, config.SMTPFrom, config.MessageIDDomain, config.OperatorBranding)
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

func deliverPendingRealmRemovalMail(config serverconfig.Config, stdout, stderr io.Writer) int {
	if config.Repositories.ResultsRoot == "" {
		_ = writeJSON(stdout, map[string]string{"schema": "filees.mail-result/v1", "status": "no_work"})
		return ExitOK
	}
	store := repoworker.RealmRemovalStore{Root: filepath.Join(config.Repositories.ResultsRoot, "realm-removals")}
	job, err := store.ClaimPendingMail(5 * time.Minute)
	if errors.Is(err, os.ErrNotExist) {
		return deliverPendingDataErasureMail(config, stdout, stderr)
	}
	if err != nil {
		report(stderr, "filees-mail realm removal claim", err)
		return ExitTempFail
	}
	message, err := repoworker.RenderRealmRemovalMail(job, config.SMTPFrom, config.MessageIDDomain)
	if err != nil {
		_ = store.MarkMailFailed(job.OperationID, job.AttemptID, err.Error(), true)
		report(stderr, "filees-mail realm removal render", err)
		return ExitData
	}
	err = smtpSubmit(context.Background(), config.SMTP, smtpsubmit.Request{EnvelopeFrom: config.SMTPFrom, Recipient: job.DeliveryAddress, Message: message})
	if err != nil {
		temporary := smtpsubmit.IsTemporary(err)
		if updateErr := store.MarkMailFailed(job.OperationID, job.AttemptID, err.Error(), !temporary); updateErr != nil {
			report(stderr, "filees-mail realm removal record failure", updateErr)
			return ExitTempFail
		}
		report(stderr, "filees-mail realm removal submit", err)
		if temporary {
			return ExitTempFail
		}
		return ExitUnavailable
	}
	if err := store.MarkMailQueued(job.OperationID, job.AttemptID); err != nil {
		report(stderr, "filees-mail realm removal record acceptance", err)
		return ExitTempFail
	}
	if err := writeJSON(stdout, map[string]string{"schema": "filees.mail-result/v1", "status": "queued", "kind": "realm_removal_confirmation", "message_id": job.MessageID}); err != nil {
		return ExitSoftware
	}
	return ExitOK
}

func deliverPendingDataErasureMail(config serverconfig.Config, stdout, stderr io.Writer) int {
	store := repoworker.DataErasureStore{Root: filepath.Join(config.Repositories.ResultsRoot, "data-erasure")}
	job, err := store.ClaimPendingMail(5 * time.Minute)
	if errors.Is(err, os.ErrNotExist) {
		return deliverOneUploadChannelMail(config, stdout, stderr)
	}
	if err != nil {
		report(stderr, "filees-mail data erasure claim", err)
		return ExitTempFail
	}
	message, err := repoworker.RenderDataErasureCompletionMail(job, config.SMTPFrom, config.MessageIDDomain)
	if err != nil {
		_ = store.MarkMailFailed(job.OperationID, job.AttemptID, err.Error(), true)
		report(stderr, "filees-mail data erasure render", err)
		return ExitData
	}
	err = smtpSubmit(context.Background(), config.SMTP, smtpsubmit.Request{
		EnvelopeFrom: config.SMTPFrom, Recipient: job.DeliveryAddress, Message: message,
	})
	if err != nil {
		temporary := smtpsubmit.IsTemporary(err)
		if updateErr := store.MarkMailFailed(job.OperationID, job.AttemptID, err.Error(), !temporary); updateErr != nil {
			report(stderr, "filees-mail data erasure record failure", updateErr)
			return ExitTempFail
		}
		report(stderr, "filees-mail data erasure submit", err)
		if temporary {
			return ExitTempFail
		}
		return ExitUnavailable
	}
	if err := store.MarkMailQueued(job.OperationID, job.AttemptID); err != nil {
		report(stderr, "filees-mail data erasure record acceptance", err)
		return ExitTempFail
	}
	if err := writeJSON(stdout, map[string]string{
		"schema": "filees.mail-result/v1", "status": "queued",
		"kind": "data_erasure_completion", "message_id": job.MessageID,
	}); err != nil {
		return ExitSoftware
	}
	return ExitOK
}

var smtpSubmit = smtpsubmit.Submit
