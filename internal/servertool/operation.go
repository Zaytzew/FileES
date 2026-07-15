package servertool

import (
	"flag"
	"fmt"
	"io"

	"filees/pkg/onboarding"
)

func RunOperation(args []string, stdout, stderr io.Writer) int {
	path, args, err := configPath(args)
	if err != nil || len(args) == 0 {
		fmt.Fprintln(stderr, "usage: filees-operation [-config path] recover | inspect --operation-id UUID")
		return ExitUsage
	}
	switch args[0] {
	case "recover":
		if len(args) != 1 {
			return ExitUsage
		}
		files, _, err := openFiles(path, toolAccess{name: "filees-operation/recover", areas: onboarding.AreaAll, write: true, needOTP: true})
		if err != nil {
			report(stderr, "filees-operation config", err)
			return ExitConfig
		}
		if err := files.Recover(); err != nil {
			report(stderr, "filees-operation recover", err)
			return ExitTempFail
		}
		if err := writeJSON(stdout, map[string]string{"schema": "filees.operation-result/v1", "status": "ok"}); err != nil {
			return ExitSoftware
		}
		return ExitOK
	case "inspect":
		flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
		flags.SetOutput(stderr)
		operationID := flags.String("operation-id", "", "operation UUID")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			return ExitUsage
		}
		files, _, err := openFiles(path, toolAccess{name: "filees-operation/inspect", areas: onboarding.AreaOperations})
		if err != nil {
			report(stderr, "filees-operation config", err)
			return ExitConfig
		}
		operation, err := files.GetOperation(*operationID)
		if err != nil {
			report(stderr, "filees-operation", err)
			return ExitData
		}
		if err := writeJSON(stdout, operation); err != nil {
			return ExitSoftware
		}
		return ExitOK
	default:
		return ExitUsage
	}
}
