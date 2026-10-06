// Package explorermenu carries only explicit, closed Explorer folder gestures.
// It has no registry, network, repository or process-execution authority.
package explorermenu

import (
	"errors"
	"path/filepath"
	"strings"
)

const (
	Add      = "--explorer-add"
	Detach   = "--explorer-detach"
	Download = "--explorer-download"
	Release  = "--explorer-release"
)

func knownVerb(verb string) bool {
	return verb == Add || verb == Detach || verb == Download || verb == Release
}

type Request struct{ Verb, Path string }

// Parse accepts exactly one verb and one absolute path, never shell syntax.
// Existence and repository eligibility belong to the GUI/controller and daemon.
func Parse(args []string) (Request, error) {
	if len(args) != 2 || !knownVerb(args[0]) || !filepath.IsAbs(args[1]) || strings.ContainsAny(args[1], "\x00\r\n") {
		return Request{}, errors.New("invalid Explorer folder request")
	}
	return Request{Verb: args[0], Path: filepath.Clean(args[1])}, nil
}

// WithoutRequest prevents a display recovery/restart from replaying a gesture.
func WithoutRequest(argv []string) []string {
	result := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		if i > 0 && knownVerb(argv[i]) {
			i++
			continue
		}
		result = append(result, argv[i])
	}
	return result
}
