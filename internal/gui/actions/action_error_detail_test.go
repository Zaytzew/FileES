package actions

import (
	"strings"
	"testing"
)

type structuredErr struct {
	key     string
	details map[string]string
}

func (e structuredErr) Error() string { return "structured: " + e.key }
func (e structuredErr) PresentationError() (string, string, string, string) {
	return "RECOVERY-1001", "ERROR", "REQUIRE_ACTION", e.key
}
func (e structuredErr) PresentationDetails() map[string]string { return e.details }

// The catalog sentence names a class of failure, not an instance. Three very
// different causes - the dispatcher could not be executed, the output file
// already existed, the archive was gone - all rendered as the same sentence,
// which is what made them undiagnosable from the user's side.
func TestActionErrorBodyKeepsTheDaemonSuppliedDetail(t *testing.T) {
	body := actionErrorBody(structuredErr{
		key:     "recovery.download_failed",
		details: map[string]string{"detail": "recovery output already exists"},
	})
	if !strings.Contains(body, "recovery output already exists") {
		t.Fatalf("body = %q; the instance must survive alongside the class", body)
	}
	if !strings.Contains(body, "Pobranie archiwum") {
		t.Fatalf("body = %q; the catalog sentence must still lead", body)
	}
}

// A key with no detail must read exactly as before.
func TestActionErrorBodyUnchangedWithoutDetail(t *testing.T) {
	if body := actionErrorBody(structuredErr{key: "recovery.download_failed"}); strings.Contains(body, "\n") {
		t.Fatalf("body = %q; nothing should be appended when there is no detail", body)
	}
}
