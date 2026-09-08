package client

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"filees/pkg/errcat"
)

// nativeSchema is the receipt schema the helper stamps on every answer.
const nativeSchema = "filees.native-svn/v1"

// NativeErrorEntry is one link in the helper's cause chain: the Apache/APR
// code and the message it arrived with.
type NativeErrorEntry struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// NativeFailure is a refusal from the native SVN helper with its codes intact.
//
// They used to be flattened: both call sites composed a sentence containing the
// raw JSON, so errmap fell back to matching English needles against text that
// happened to contain the answer in machine-readable form (M46). The codes are
// kept here, and the sentence is built from them rather than instead of them.
type NativeFailure struct {
	Verb    string
	Entries []NativeErrorEntry
	// Exit is the process failure, when there was one. Truncated output or a
	// refusal with exit 0 leaves it nil.
	Exit error
	// Truncated records that the helper said more than we were willing to read.
	// Kept because a classification made from half an answer must be visible as
	// such.
	Truncated bool
	// Output is the raw stdout and stderr, already capped by nativeOutput. It
	// stays because alpha diagnostics need the whole thing, not the part we
	// knew how to name.
	Output string
}

func (f *NativeFailure) Error() string {
	parts := make([]string, 0, len(f.Entries)+1)
	for _, entry := range f.Entries {
		parts = append(parts, fmt.Sprintf("E%d: %s", entry.Code, entry.Message))
	}
	if f.Truncated {
		parts = append(parts, "output truncated")
	}
	if len(parts) == 0 {
		if f.Exit != nil {
			return fmt.Sprintf("native SVN %q failed: %v\n%s", f.Verb, f.Exit, f.Output)
		}
		return fmt.Sprintf("native SVN %q failed\n%s", f.Verb, f.Output)
	}
	return fmt.Sprintf("native SVN %q: %s", f.Verb, strings.Join(parts, "; "))
}

func (f *NativeFailure) Unwrap() error { return f.Exit }

// Codes returns the chain as reported, outermost first.
func (f *NativeFailure) Codes() []int {
	out := make([]int, 0, len(f.Entries))
	for _, entry := range f.Entries {
		out = append(out, entry.Code)
	}
	return out
}

// svnCodeKeys maps Subversion codes to the product vocabulary.
//
// Only codes this project has actually met are here, each traceable to a
// measurement in reports/ or to a comment written beside the failure it caused.
// A guessed mapping is worse than none: an unmapped code still reaches errmap's
// text heuristics with its message intact, while a wrong one is confidently
// wrong and stops anybody looking further.
var svnCodeKeys = map[int]errcat.Key{
	170001: errcat.KeyAuthFailed,        // Authorization failed
	210002: errcat.KeyConnectionDropped, // Network connection closed unexpectedly
	155004: errcat.KeyWorkingCopyBusy,   // Working copy locked; run svn cleanup
	160039: errcat.KeyLockOperation,     // does not own lock
	200009: errcat.KeyCommitNoVCS,       // is not under version control
	165001: errcat.KeyCommitFailed,      // blocked by a pre-commit hook
}

// svnWrapperCodes are true but uninformative: Subversion reports them around a
// more specific cause. They classify only when nothing deeper is recognised.
//
// This is the point of having the chain at all. M9 in the failure matrix records
// that an identity refusal arrives as E170013 and gets classified as network -
// a permanent cause reported as a transient one, which is how a client ends up
// retrying forever against a revoked key. The CLI gave us one line and no way to
// do better; the helper gives us the whole chain, so the outer code stops being
// the answer and becomes the fallback.
var svnWrapperCodes = map[int]errcat.Key{
	170013: errcat.KeyNetUnreachable, // Unable to connect to a repository at URL
}

// classifyNativeCodes picks the key for a cause chain, preferring the deepest
// specific code and falling back to a wrapper only when nothing else is known.
func classifyNativeCodes(entries []NativeErrorEntry) (errcat.Key, int, bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		if key, ok := svnCodeKeys[entries[i].Code]; ok {
			return key, entries[i].Code, true
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if key, ok := svnWrapperCodes[entries[i].Code]; ok {
			return key, entries[i].Code, true
		}
	}
	return "", 0, false
}

// parseNativeErrors reads the helper's errors[] out of its JSON receipt.
// A receipt that is absent, truncated or unparseable yields nothing, which is
// not an error here: the caller still has the raw output and the exit status.
func parseNativeErrors(stdout string) []NativeErrorEntry {
	var doc struct {
		Schema string             `json:"schema"`
		OK     bool               `json:"ok"`
		Errors []NativeErrorEntry `json:"errors"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		return nil
	}
	if doc.Schema != nativeSchema || doc.OK {
		return nil
	}
	return doc.Errors
}

// nativeFault turns a helper refusal into the typed error the rest of FileES
// already understands.
//
// errmap.Classify checks for an errcat.Fault before it reaches its text
// heuristics, so returning one here is the whole integration: no new mechanism,
// and the CLI and the helper end at the same keys. An unrecognised chain
// deliberately returns the NativeFailure alone rather than a Fault, so the
// heuristics still get their chance instead of being pre-empted by a guess.
func nativeFault(verb string, exitErr error, truncated bool, stdout, stderr string) error {
	failure := &NativeFailure{
		Verb:      verb,
		Entries:   parseNativeErrors(stdout),
		Exit:      exitErr,
		Truncated: truncated,
		Output:    strings.TrimRight(stdout+"\n"+stderr, "\n"),
	}
	key, code, ok := classifyNativeCodes(failure.Entries)
	if !ok {
		return failure
	}
	return errcat.New(key, map[string]string{
		"detail":      failure.Error(),
		"native_code": "E" + strconv.Itoa(code),
	}, failure)
}
