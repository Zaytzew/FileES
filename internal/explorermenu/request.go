// Package explorermenu carries only the two explicit Explorer folder gestures.
// It has no registry, network, repository or process-execution authority.
package explorermenu

import (
	"errors"
	"path/filepath"
	"strings"
)

const (
	Add    = "--explorer-add"
	Detach = "--explorer-detach"
)

type Request struct{ Verb, Path string }

// Parse accepts exactly one verb and one absolute path, never shell syntax.
// Existence and repository eligibility belong to the GUI/controller and daemon.
func Parse(args []string) (Request, error) {
	if len(args) != 2 || (args[0] != Add && args[0] != Detach) || !filepath.IsAbs(args[1]) || strings.ContainsAny(args[1], "\x00\r\n") {
		return Request{}, errors.New("invalid Explorer folder request")
	}
	return Request{Verb: args[0], Path: filepath.Clean(args[1])}, nil
}

// WithoutRequest prevents a display recovery/restart from replaying a gesture.
func WithoutRequest(argv []string) []string {
	result := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		if i > 0 && (argv[i] == Add || argv[i] == Detach) {
			i++
			continue
		}
		result = append(result, argv[i])
	}
	return result
}
