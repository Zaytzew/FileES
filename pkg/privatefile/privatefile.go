// Package privatefile expresses one rule in one place: this path holds key
// material and must be reachable only by the user who owns it.
//
// Until now that rule was written as os.Chmod(path, 0o600) at a dozen call
// sites in pkg/deploy, pkg/localpin and pkg/clientprofile. On Windows those
// calls do not fail — they simply do not restrict anything, because os.Chmod
// there only toggles the read-only attribute. The guarantee was therefore
// absent on Windows rather than merely untested, and the tests that assert
// "want 0600" were reporting a real gap, not a portability nuisance.
//
// The mode bits stay the mechanism on unix. On Windows the equivalent is an
// explicit DACL naming the current user and nothing else, with inheritance
// blocked so a permissive parent directory cannot widen it back.
package privatefile

import "errors"

// ErrNotPrivate reports a path that exists but is reachable by someone other
// than its owner. Callers that treat a missing file as normal should test for
// this rather than for any error.
var ErrNotPrivate = errors.New("path is not private to the current user")
