package main

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// The existing extracted-renderer tests use the real catalogues/formatter,
// not a second translation implementation or a stub returning message keys.
func frontendI18NTestPrelude(t *testing.T) string {
	t.Helper()
	source := embeddedFrontendFile(t, "frontend/i18n.js")
	imports := regexp.MustCompile(`import (\w+) from "\./locales/([A-Za-z0-9-]+)\.js";`)
	for _, match := range imports.FindAllStringSubmatch(source, -1) {
		catalogue := embeddedFrontendFile(t, "frontend/locales/"+match[2]+".js")
		catalogue = strings.Replace(catalogue, "export default", "const "+match[1]+" =", 1)
		source = strings.Replace(source, match[0], catalogue, 1)
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
