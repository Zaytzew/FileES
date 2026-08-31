package servertool

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"filees/pkg/activation"
	"filees/pkg/onboarding"
	"filees/pkg/repoworker"
)

// adminVersion is overridden at link time by the packaging scripts. Without a
// version at all, the only way to tell how old a deployed filees-admin is was
// to compare its usage text against the source - which is how a stale binary
// went unnoticed on 2026-08-06 while a newly added flag appeared "not
// defined".
var adminVersion = "dev"

// adminUsage refuses a command out loud. Returning ExitUsage without printing
// anything is the command-line form of "click it and nothing happens": the
// caller sees a non-zero exit, no output, and no way to tell a typo from a
// broken binary. Measured live on 2026-08-06, where `ticket create` with its
// flags before the positional e-mail exited silently and looked like the
// feature was missing.
func adminUsage(stderr io.Writer, flags *flag.FlagSet, required string) int {
	fmt.Fprintf(stderr, "usage: filees-admin [-config path] %s %s\n", flags.Name(), required)
	flags.PrintDefaults()
	return ExitUsage
}

func RunAdmin(args []string, stdout, stderr io.Writer) int {
	// version is answered before the config path is even parsed. The usual
	// reason to ask is that a deployment looks stale, and a suspect config is
	// a common part of that suspicion - so making the answer depend on the
	// config would withhold it exactly when it is most wanted.
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version" || args[0] == "-version") {
		fmt.Fprintln(stdout, adminVersion)
		return ExitOK
	}
	path, args, err := configPath(args)
	if err != nil {
		report(stderr, "filees-admin", err)
		return ExitUsage
	}
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: filees-admin [-config path] ticket create|resend|revoke|list | operation inspect | client revoke|revoke-realm | repo transfer-owner|check-state|prune | erasure complete | version")
		return ExitUsage
	}
	switch args[0] + " " + args[1] {
	case "ticket create":
		flags := flag.NewFlagSet("ticket create", flag.ContinueOnError)
		flags.SetOutput(stderr)
		emailFlag := flags.String("email", "", "delivery e-mail (legacy spelling; prefer positional argument)")
		ttl := flags.Duration("ttl", 24*time.Hour, "ticket lifetime")
		joinRealmAlias := flags.String("join-realm-alias", "", "bind this invitation to an existing realm by its alias, instead of creating a new realm")
		const ticketCreateUsage = "usage: filees-admin [-config path] ticket create <e-mail> [--ttl 24h] [--join-realm-alias <alias>]\n" +
			"  the e-mail must come first, before any flags"
		values := args[2:]
		positionalEmail := ""
		if len(values) > 0 && !strings.HasPrefix(values[0], "-") {
			positionalEmail, values = values[0], values[1:]
		}
		if err := flags.Parse(values); err != nil || flags.NArg() != 0 || (*emailFlag != "" && positionalEmail != "") {
			fmt.Fprintln(stderr, ticketCreateUsage)
			return ExitUsage
		}
		email := *emailFlag
		if positionalEmail != "" {
			email = positionalEmail
		}
		if email == "" {
			fmt.Fprintln(stderr, ticketCreateUsage)
			return ExitUsage
		}
		files, config, err := openFiles(path, toolAccess{name: "filees-admin/ticket-create", areas: onboarding.AreaTickets | onboarding.AreaOperations | onboarding.AreaAudit, write: true, needSMTP: true, needRealmAlias: *joinRealmAlias != ""})
		if err != nil {
			report(stderr, "filees-admin config", err)
			return ExitConfig
		}
		realmID := ""
		if *joinRealmAlias != "" {
			realmID, err = repoworker.ResolveAlias(config.Activation.ServiceWorkingCopy, *joinRealmAlias)
			if err != nil {
				report(stderr, "filees-admin ticket create: resolve --join-realm-alias", err)
				return ExitConfig
			}
		}
		profile := onboarding.Invitation{ServerID: config.Invitation.ServerID, ServerAddress: config.Invitation.ServerAddress, KnownHost: config.Invitation.KnownHost}
		ticket, err := files.CreateInvitationTicket(email, *ttl, profile, realmID)
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
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 || *ticketID == "" {
			return adminUsage(stderr, flags, "--ticket-id UUID")
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
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 || *ticketID == "" {
			return adminUsage(stderr, flags, "--ticket-id UUID")
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
			fmt.Fprintln(stderr, "usage: filees-admin [-config path] ticket list")
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
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 || *operationID == "" {
			return adminUsage(stderr, flags, "--operation-id UUID")
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
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 || *clientID == "" {
			return adminUsage(stderr, flags, "--client-id UUID [--reason text]")
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
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 || *realmID == "" {
			return adminUsage(stderr, flags, "--realm-id UUID [--reason text]")
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
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 || *repoID == "" || *realmID == "" {
			return adminUsage(stderr, flags, "--repo-id UUID --realm-id UUID")
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
		if err := withServiceWorkingCopy(ctx, config.Activation, func() error {
			return publisher.TransferOwner(ctx, *repoID, *realmID)
		}); err != nil {
			report(stderr, "filees-admin repo transfer-owner", err)
			return ExitTempFail
		}
		if err := writeJSON(stdout, map[string]any{"schema": "filees.admin-client-result/v1", "status": "transferred", "repo_id": *repoID, "realm_id": *realmID}); err != nil {
			return ExitSoftware
		}
		return ExitOK
	case "repo check-state":
		flags := flag.NewFlagSet("repo check-state", flag.ContinueOnError)
		flags.SetOutput(stderr)
		realmID := flags.String("realm-id", "", "limit to one realm UUID (optional)")
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
			return adminUsage(stderr, flags, "[--realm-id UUID]")
		}
		_, config, err := openFiles(path, toolAccess{name: "filees-admin/repo-check-state", areas: onboarding.AreaOperations, needRepoResults: true, needRepositoryData: true})
		if err != nil {
			report(stderr, "filees-admin config", err)
			return ExitConfig
		}
		records, err := scanBackendRecords(config.Repositories.ResultsRoot, config.Repositories.Root, *realmID, time.Now())
		if err != nil {
			report(stderr, "filees-admin repo check-state", err)
			return ExitTempFail
		}
		if err := writeJSON(stdout, map[string]any{"schema": "filees.admin-repo-check-state/v1", "status": "ok", "records": records}); err != nil {
			return ExitSoftware
		}
		return ExitOK
	case "repo prune":
		flags := flag.NewFlagSet("repo prune", flag.ContinueOnError)
		flags.SetOutput(stderr)
		realmID := flags.String("realm-id", "", "limit to one realm UUID (optional)")
		olderThan := flags.Duration("older-than", time.Hour, "only prune records at least this old, to never race a live in-progress attempt")
		apply := flags.Bool("apply", false, "actually delete; without this flag, lists what would be pruned and changes nothing")
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
			return adminUsage(stderr, flags, "[--realm-id UUID] [--older-than 1h] [--apply]")
		}
		_, config, err := openFiles(path, toolAccess{name: "filees-admin/repo-prune", areas: onboarding.AreaOperations, write: true, needRepoResults: true, needRepositoryData: true})
		if err != nil {
			report(stderr, "filees-admin config", err)
			return ExitConfig
		}
		now := time.Now()
		records, err := scanBackendRecords(config.Repositories.ResultsRoot, config.Repositories.Root, *realmID, now)
		if err != nil {
			report(stderr, "filees-admin repo prune", err)
			return ExitTempFail
		}
		var candidates, needsAttention []backendRecordReport
		for _, record := range records {
			if !record.Prunable {
				needsAttention = append(needsAttention, record)
				continue
			}
			if time.Duration(record.AgeSeconds)*time.Second < *olderThan {
				needsAttention = append(needsAttention, record)
				continue
			}
			candidates = append(candidates, record)
		}
		if !*apply {
			if err := writeJSON(stdout, map[string]any{"schema": "filees.admin-repo-prune/v1", "status": "dry_run", "would_remove": candidates, "needs_attention": needsAttention}); err != nil {
				return ExitSoftware
			}
			return ExitOK
		}
		var removed []backendRecordReport
		for _, record := range candidates {
			if err := os.Remove(record.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				report(stderr, "filees-admin repo prune remove "+record.OperationID, err)
				return ExitTempFail
			}
			removed = append(removed, record)
		}
		if err := writeJSON(stdout, map[string]any{"schema": "filees.admin-repo-prune/v1", "status": "pruned", "removed": removed, "needs_attention": needsAttention}); err != nil {
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
