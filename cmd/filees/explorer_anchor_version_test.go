//go:build !nocfapi

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filees/pkg/ipcserver"
)

// A real subprocess on Windows too; it handles only the harmless version verb.
func init() {
	mode := os.Getenv("FILEES_TEST_CFAPI_VERSION_PROCESS")
	if mode == "" || len(os.Args) != 2 || os.Args[1] != "version" {
		return
	}
	switch mode {
	case "timeout":
		time.Sleep(30 * time.Second)
	case "oversize":
		fmt.Print(strings.Repeat("x", 2<<20))
	case "stderr":
		fmt.Fprint(os.Stderr, strings.Repeat("x", 2<<20))
		fmt.Print(os.Getenv("FILEES_TEST_CFAPI_VERSION_JSON"))
	default:
		fmt.Print(os.Getenv("FILEES_TEST_CFAPI_VERSION_JSON"))
	}
	if mode == "exit" {
		os.Exit(1)
	}
	os.Exit(0)
}

func anchorVersionJSON(t *testing.T, version string, features []string) string {
	t.Helper()
	raw, err := json.Marshal(cfapiVersion{Schema: "filees.cfapi/v1", OK: true, Version: version, Features: features})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestAnchorHelperVersionValidation(t *testing.T) {
	current := anchorVersionJSON(t, "0.1.18", requiredAnchorFeatures)
	for _, tc := range []struct {
		name, raw, daemon string
		ok                bool
	}{
		{"same release", current, "0.1.18+r1711", true},
		{"different revision not a compatibility marker", current, "0.1.18+r9999", true},
		{"dev still validates", current, "dev", true},
		{"old release", current, "0.1.19+r1711", false},
		{"missing version", anchorVersionJSON(t, "", requiredAnchorFeatures), "dev", false},
		{"invalid version", anchorVersionJSON(t, "x", requiredAnchorFeatures), "dev", false},
		{"missing features", anchorVersionJSON(t, "0.1.18", nil), "dev", false},
		{"bad schema", strings.Replace(current, "filees.cfapi/v1", "filees.cfapi/v2", 1), "dev", false},
		{"failed", strings.Replace(current, `"ok":true`, `"ok":false`, 1), "dev", false},
		{"truncated", current[:len(current)-1], "dev", false},
		{"two objects", current + current, "dev", false},
		{"null", "null", "dev", false},
		{"empty daemon", current, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateAnchorVersion([]byte(tc.raw), tc.daemon); (err == nil) != tc.ok {
				t.Fatalf("ok=%v: %v", tc.ok, err)
			}
		})
	}
	for i, missing := range requiredAnchorFeatures {
		features := append([]string{}, requiredAnchorFeatures[:i]...)
		features = append(features, requiredAnchorFeatures[i+1:]...)
		if err := validateAnchorVersion([]byte(anchorVersionJSON(t, "0.1.18", features)), "dev"); err == nil || !strings.Contains(err.Error(), missing) {
			t.Fatalf("missing %s: %v", missing, err)
		}
	}
}

func TestAnchorHelperVersionProcess(t *testing.T) {
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FILEES_TEST_CFAPI_VERSION_JSON", anchorVersionJSON(t, "0.1.18", requiredAnchorFeatures))
	for _, mode := range []string{"ok", "stderr", "exit", "oversize", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("FILEES_TEST_CFAPI_VERSION_PROCESS", mode)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			start := time.Now()
			err := anchorHelperCompatible(ctx, helper, "0.1.18+r1711")
			if (err == nil) != (mode == "ok" || mode == "stderr") {
				t.Fatalf("%s: %v", mode, err)
			}
			if time.Since(start) > 6*time.Second {
				t.Fatal("query exceeded its deadline")
			}
		})
	}
	var out anchorVersionOutput
	if _, err := io.Copy(&out, strings.NewReader(strings.Repeat("x", 128<<10))); err == nil || out.buffer.Len() > 64<<10 {
		t.Fatal("io.Copy bypassed output limit")
	}
}

func TestAnchorHelperOverrideDoesNotBypassWiringCheck(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("helper discovery is Windows-only")
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FILEES_CFAPI", helper)
	t.Setenv("FILEES_TEST_CFAPI_VERSION_PROCESS", "ok")
	previous := version
	version = "0.1.18+r1711"
	defer func() { version = previous }()
	for _, good := range []bool{false, true} {
		answer := `{"ok":true}`
		if good {
			answer = anchorVersionJSON(t, "0.1.18", requiredAnchorFeatures)
		}
		t.Setenv("FILEES_TEST_CFAPI_VERSION_JSON", answer)
		server := ipcserver.New("unused")
		p := &daemonProvisioner{}
		loop := explorerAnchors(server, nil, p)
		if (loop != nil) != good || (p.detachAnchor != nil) != good {
			t.Fatalf("incorrect wiring, compatible=%v", good)
		}
	}
}

func TestAnchorHelperNativeVersion(t *testing.T) {
	helper := os.Getenv("FILEES_TEST_CFAPI_NATIVE")
	if helper == "" {
		t.Skip("provide an isolated freshly built helper for native version acceptance")
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	if err := anchorHelperCompatible(t.Context(), helper, strings.TrimSpace(string(raw))+"+r1711"); err != nil {
		t.Fatal(err)
	}
	if err := anchorHelperCompatible(t.Context(), helper, "999.0.0+r1"); err == nil {
		t.Fatal("mismatched native helper accepted")
	}
}
