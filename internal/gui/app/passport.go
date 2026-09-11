package app

import (
	contract "filees/pkg/contract/v1"
	"filees/pkg/errcat"
)

// passportIssuePresentation splits a reservation issue into the key that names
// it and the text to show if nothing renders that key.
//
// This layer projects state; it does not author sentences. The daemon owns the
// wording and serves it per language, so turning the key into a sentence here
// would compile one language into the projection and leave every other reader
// with whatever this process happened to be built with.
func passportIssuePresentation(issue contract.PassportIssue) (key, fallback string) {
	if _, ok := errcat.ByPair(errcat.Code(issue.Code), errcat.Key(issue.Message)); ok {
		// The daemon sent a dictionary key rather than prose.
		return issue.Message, ""
	}
	fallback = issue.Message
	if issue.Path != "" {
		fallback += " — " + issue.Path
	}
	return "", fallback
}
