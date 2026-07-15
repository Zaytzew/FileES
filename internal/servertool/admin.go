package servertool

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"filees/pkg/onboarding"
)

func RunAdmin(args []string, stdout, stderr io.Writer) int {
	path, args, err := configPath(args)
	if err != nil {
		report(stderr, "filees-admin", err)
		return ExitUsage
	}
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: filees-admin [-config path] ticket create|revoke|list | operation inspect")
		return ExitUsage
	}
	switch args[0] + " " + args[1] {
	case "ticket create":
		flags := flag.NewFlagSet("ticket create", flag.ContinueOnError)
		flags.SetOutput(stderr)
		email := flags.String("email", "", "delivery e-mail")
		realmID := flags.String("realm-id", "", "approved realm UUID")
		ttl := flags.Duration("ttl", 24*time.Hour, "ticket lifetime")
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
			return ExitUsage
		}
		files, _, err := openFiles(path, toolAccess{name: "filees-admin/ticket-create", areas: onboarding.AreaTickets | onboarding.AreaAudit, write: true})
		if err != nil {
			report(stderr, "filees-admin config", err)
			return ExitConfig
		}
		ticket, err := files.CreateTicket(*email, onboarding.Policy{RealmID: *realmID}, *ttl)
		if err != nil {
			return adminError(stderr, err)
		}
		response := onboarding.AdminResponse{Schema: onboarding.AdminProtocolSchema, RequestID: uuid.NewString(), Status: onboarding.AdminOK, Ticket: &ticket}
		if err := writeJSON(stdout, response); err != nil {
			return ExitSoftware
		}
		return ExitOK
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
