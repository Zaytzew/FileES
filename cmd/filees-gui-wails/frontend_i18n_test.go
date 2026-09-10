package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The existing extracted-renderer tests use the real catalogues/formatter,
// not a second translation implementation or a stub returning message keys.
func frontendI18NTestPrelude(t *testing.T) string {
	t.Helper()
	source := embeddedFrontendFile(t, "frontend/i18n.js")
	for _, locale := range []string{"pl", "en"} {
		catalogue := embeddedFrontendFile(t, "frontend/locales/"+locale+".js")
		catalogue = strings.Replace(catalogue, "export default", "const "+locale+" =", 1)
		source = strings.Replace(source, `import `+locale+` from "./locales/`+locale+`.js";`, catalogue, 1)
	}
	source = strings.ReplaceAll(source, "export function", "function")
	source = strings.ReplaceAll(source, "export const", "const")
	return strings.Replace(source, `let locale = "en"`, `let locale = "pl"`, 1)
}

func TestFrontendI18NCataloguesAndPreference(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), node, "--test", "frontend-tests/i18n.test.mjs")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("i18n: %v\n%s", err, output)
	}
}
