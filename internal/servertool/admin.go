package servertool

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"filees/pkg/activation"
	"filees/pkg/onboarding"
	"filees/pkg/repoworker"
)

func RunAdmin(args []string, stdout, stderr io.Writer) int {
	path, args, err := configPath(args)
	if err != nil {
		report(stderr, "filees-admin", err)
		return ExitUsage
	}
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: filees-admin [-config path] ticket create|resend|revoke|list | operation inspect | client revoke|revoke-realm | repo transfer-owner | erasure complete")
		return ExitUsage
	}
	switch args[0] + " " + args[1] {
	case "ticket create":
		flags := flag.NewFlagSet("ticket create", flag.ContinueOnError)
		flags.SetOutput(stderr)
		emailFlag := flags.String("email", "", "delivery e-mail (legacy spelling; prefer positional argument)")
		ttl := flags.Duration("ttl", 24*time.Hour, "ticket lifetime")
		values := args[2:]
		positionalEmail := ""
		if len(values) > 0 && !strings.HasPrefix(values[0], "-") {
			positionalEmail, values = values[0], values[1:]
		}
		if err := flags.Parse(values); err != nil || flags.NArg() != 0 || (*emailFlag != "" && positionalEmail != "") {
			return ExitUsage
		}
		email := *emailFlag
		if positionalEmail != "" {
			email = positionalEmail
		}
		if email == "" {
			fmt.Fprintln(stderr, "usage: filees-admin [-config path] ticket create <e-mail> [--ttl 24h]")
			return ExitUsage
		}
		files, config, err := openFiles(path, toolAccess{name: "filees-admin/ticket-create", areas: onboarding.AreaTickets | onboarding.AreaOperations | onboarding.AreaAudit, write: true, needSMTP: true})
		if err != nil {
			report(stderr, "filees-admin config", err)
			return ExitConfig
		}
		profile := onboarding.Invitation{ServerID: config.Invitation.ServerID, ServerAddress: config.Invitation.ServerAddress, KnownHost: config.Invitation.KnownHost}
		ticket, err := files.CreateInvitationTicket(email, *ttl, profile)
		if err != nil {
			return adminError(stderr, err)
		}
		if code := deliverPendingMail(files, config, io.Discard, stderr); code != ExitOK {
			return code
		}
		// The invitation capability belongs only in the recipient's e-mail;
		// never echo it into an administrator's shell history or JSON log.
		ticket.InvitationOutbox.Invitation = ""
		ticket.InvitationOutbox.DeliveryAddress = ""
		response := onboarding.AdminResponse{Schema: onboarding.AdminProtocolSchema, RequestID: uuid.NewString(), Status: onboarding.AdminOK, Ticket: &ticket}
		if err := writeJSON(stdout, response); err != nil {
			return ExitSoftware
		}
		return ExitOK
	case "ticket resend":
		flags := flag.NewFlagSet("ticket resend", flag.ContinueOnError)
		flags.SetOutput(stderr)
		ticketID := flags.String("ticket-id", "", "ticket UUID")
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
			return ExitUsage
		}
		files, config, err := openFiles(path, toolAccess{name: "filees-admin/ticket-resend", areas: onboarding.AreaTickets | onboarding.AreaOperations | onboarding.AreaAudit, write: true, needSMTP: true})
		if err != nil {
			report(stderr, "filees-admin config", err)
			return ExitConfig
		}
		profile := onboarding.Invitation{ServerID: config.Invitation.ServerID, ServerAddress: config.Invitation.ServerAddress, KnownHost: config.Invitation.KnownHost}
		if err := files.ResendInvitation(*ticketID, profile); err != nil {
			return adminError(stderr, err)
		}
		if code := deliverPendingMail(files, config, io.Discard, stderr); code != ExitOK {
			return code
		}
		return writeAdminEmpty(stdout, uuid.NewString())
	case "ticket revoke":
		flags := flag.NewFlagSet("ticket revoke", flag.ContinueOnError)
		flags.SetOutput(stderr)
		ticketID := flags.String("ticket-id", "", "ticket UUID")
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
			return ExitUsage
		}
		files, _, err := openFiles(path, toolAccess{name: "filees-admin/ticket-revoke", areas: onboarding.AreaTickets | onboarding.AreaAudit, write: true})
		if err != nil {
			report(stderr, "filees-admin config", err)
			return ExitConfig
		}
		if err := files.RevokeTicket(*ticketID); err != nil {
			return adminError(stderr, err)
		}
		return writeAdminEmpty(stdout, uuid.NewString())
	case "ticket list":
		if len(args) != 2 {
			return ExitUsage
		}
		files, _, err := openFiles(path, toolAccess{name: "filees-admin/ticket-list", areas: onboarding.AreaTickets})
		if err != nil {
			report(stderr, "filees-admin config", err)
			return ExitConfig
		}
		tickets, err := files.ListTickets()
		if err != nil {
			return adminError(stderr, err)
		}
		response := onboarding.AdminResponse{Schema: onboarding.AdminProtocolSchema, RequestID: uuid.NewString(), Status: onboarding.AdminOK, Tickets: tickets}
		if err := writeJSON(stdout, response); err != nil {
			return ExitSoftware
		}
		return ExitOK
	case "operation inspect":
		flags := flag.NewFlagSet("operation inspect", flag.ContinueOnError)
		flags.SetOutput(stderr)
		operationID := flags.String("operation-id", "", "operation UUID")
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
			return ExitUsage
		}
		files, _, err := openFiles(path, toolAccess{name: "filees-admin/operation-inspect", areas: onboarding.AreaOperations})
		if err != nil {
			report(stderr, "filees-admin config", err)
			return ExitConfig
		}
		operation, err := files.GetOperation(*operationID)
		if err != nil {
			return adminError(stderr, err)
		}
		response := onboarding.AdminResponse{Schema: onboarding.AdminProtocolSchema, RequestID: uuid.NewString(), Status: onboarding.AdminOK, Operation: &operation}
		if err := writeJSON(stdout, response); err != nil {
			return ExitSoftware
		}
		return ExitOK
	case "client revoke":
		flags := flag.NewFlagSet("client revoke", flag.ContinueOnError)
		flags.SetOutput(stderr)
		clientID := flags.String("client-id", "", "client UUID")
		reason := flags.String("reason", "", "one-line revoke reason")
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
			return ExitUsage
		}
		_, config, err := openFiles(path, toolAccess{name: "filees-admin/client-revoke", areas: onboarding.AreaOperations, write: true, needActivation: true, needSVN: true})
		if err != nil {
			report(stderr, "filees-admin config", err)
			return ExitConfig
		}
		manager, err := activation.New(config.Activation, nil)
		if err != nil {
			report(stderr, "filees-admin activation", err)
			return ExitConfig
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		revision, err := manager.Revoke(ctx, *clientID, *reason)
		if err != nil {
			report(stderr, "filees-admin client revoke", err)
			return ExitTempFail
		}
		if err := writeJSON(stdout, map[string]any{"schema": "filees.admin-client-result/v1", "status": "revoked", "client_id": *clientID, "service_revision": revision}); err != nil {
			return ExitSoftware
		}
		return ExitOK
	case "client revoke-realm":
		flags := flag.NewFlagSet("client revoke-realm", flag.ContinueOnError)
		flags.SetOutput(stderr)
		realmID := flags.String("realm-id", "", "realm UUID")
		reason := flags.String("reason", "", "one-line revoke reason")
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
			return ExitUsage
		}
		_, config, err := openFiles(path, toolAccess{name: "filees-admin/client-revoke-realm", areas: onboarding.AreaOperations, write: true, needActivation: true, needSVN: true})
		if err != nil {
			report(stderr, "filees-admin config", err)
			return ExitConfig
		}
		manager, err := activation.New(config.Activation, nil)
		if err != nil {
			report(stderr, "filees-admin activation", err)
			return ExitConfig
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		revoked, err := manager.RevokeRealm(ctx, *realmID, *reason)
		if err != nil {
			report(stderr, "filees-admin client revoke-realm", err)
			return ExitTempFail
		}
		if err := writeJSON(stdout, map[string]any{"schema": "filees.admin-client-result/v1", "status": "revoked", "realm_id": *realmID, "client_ids": revoked}); err != nil {
			return ExitSoftware
		}
		return ExitOK
	case "repo transfer-owner":
		flags := flag.NewFlagSet("repo transfer-owner", flag.ContinueOnError)
		flags.SetOutput(stderr)
		repoID := flags.String("repo-id", "", "repository UUID")
		realmID := flags.String("realm-id", "", "new owner realm UUID")
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
			return ExitUsage
		}
		_, config, err := openFiles(path, toolAccess{name: "filees-admin/repo-transfer-owner", areas: onboarding.AreaOperations, write: true, needSVN: true})
		if err != nil {
			report(stderr, "filees-admin config", err)
			return ExitConfig
		}
		r := config.Repositories
		publisher := repoworker.ServicePublisher{ServiceWC: config.Activation.ServiceWorkingCopy, DataAuthzFile: r.DataAuthzFile, Runner: repoworker.SVNPublishRunner{SVN: config.Activation.SVNBinary, WorkingCopy: config.Activation.ServiceWorkingCopy}}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := publisher.TransferOwner(ctx, *repoID, *realmID); err != nil {
			report(stderr, "filees-admin repo transfer-owner", err)
			return ExitTempFail
		}
		if err := writeJSON(stdout, map[string]any{"schema": "filees.admin-client-result/v1", "status": "transferred", "repo_id": *repoID, "realm_id": *realmID}); err != nil {
			return ExitSoftware
		}
		return ExitOK
	case "erasure complete":
		flags := flag.NewFlagSet("erasure complete", flag.ContinueOnError)
		flags.SetOutput(stderr)
		operationID := flags.String("operation-id", "", "realm-removal operation UUID")
		partiallyRetained := flags.Bool("partially-retained", false, "retention prevents complete deletion")
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 || *operationID == "" {
			fmt.Fprintln(stderr, "usage: filees-admin [-config path] erasure complete --operation-id UUID [--partially-retained]")
			return ExitUsage
		}
		files, config, err := openFiles(path, toolAccess{
			name: "filees-admin/erasure-complete", areas: onboarding.AreaOperations | onboarding.AreaAudit,
			write: true, needSMTP: true, needRepoResults: true,
		})
		if err != nil {
			report(stderr, "filees-admin config", err)
			return ExitConfig
		}
		store := repoworker.DataErasureStore{Root: filepath.Join(config.Repositories.ResultsRoot, "data-erasure")}
		record, err := store.Complete(*operationID, *partiallyRetained)
		if err != nil {
			return adminError(stderr, err)
		}
		if code := deliverPendingMail(files, config, io.Discard, stderr); code != ExitOK {
			return code
		}
		if err := writeJSON(stdout, map[string]any{
			"schema": "filees.admin-erasure-result/v1", "operation_id": record.OperationID,
			"state": record.State, "completed_at": record.CompletedAt,
		}); err != nil {
			return ExitSoftware
		}
		return ExitOK
	default:
		fmt.Fprintln(stderr, "filees-admin: unsupported command")
		return ExitUsage
	}
}

func writeAdminEmpty(stdout io.Writer, requestID string) int {
	response := onboarding.AdminResponse{Schema: onboarding.AdminProtocolSchema, RequestID: requestID, Status: onboarding.AdminOK}
	if err := writeJSON(stdout, response); err != nil {
		return ExitSoftware
	}
	return ExitOK
}

func adminError(stderr io.Writer, err error) int {
	report(stderr, "filees-admin", err)
	if err == onboarding.ErrNotFound || err == onboarding.ErrTicketExists {
		return ExitData
	}
	return ExitSoftware
}
