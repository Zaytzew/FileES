package app

import (
	contract "filees/pkg/contract/v1"
	"filees/pkg/errcat"
)

func passportIssueMessage(issue contract.PassportIssue) string {
	message := issue.Message
	if spec, ok := errcat.ByPair(errcat.Code(issue.Code), errcat.Key(issue.Message)); ok {
		message = spec.Polish
	}
	if issue.Path != "" {
		message += " — " + issue.Path
	}
	return message
}
