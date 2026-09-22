package main

import (
	"hash/fnv"
	"regexp"
	"strconv"
	"testing"
)

// Every hand-kept binding module must call the IDs the released binary
// answers to. Wails derives them from FNV-1a("<package path>.<Type>.<Method>"),
// and in a built application this package's path is "main"; a test binary
// sees the import path instead. IDs copied from a test context therefore fail
// only in the real application ("unknown bound method id"), as the unattached
// browser did in r1450.
func TestHandKeptBindingModulesUseTheReleasedBinarysIDs(t *testing.T) {
	modules := map[string]string{
		"guiservice.js":         "GUIService",
		"settingsservice.js":    "SettingsService",
		"repositoryservice.js":  "RepositoryService",
		"pairingservice.js":     "PairingBridge",
		"promptservice.js":      "PromptBridge",
		"timemachineservice.js": "TimeMachineService",
		"headbrowserservice.js": "HeadBrowserService",
	}
	call := regexp.MustCompile(`(?s)export function (\w+)\([^)]*\)\s*\{[^}]*?ByID\((\d+)`)
	for file, typeName := range modules {
		raw, err := frontend.ReadFile("frontend/bindings/filees/cmd/filees-gui-wails/" + file)
		if err != nil {
			t.Fatal(err)
		}
		calls := call.FindAllStringSubmatch(string(raw), -1)
		if len(calls) == 0 {
			t.Fatalf("%s has no ID calls; the pattern or the module changed", file)
		}
		for _, match := range calls {
			sum := fnv.New32a()
			_, _ = sum.Write([]byte("main." + typeName + "." + match[1]))
			if want := strconv.FormatUint(uint64(sum.Sum32()), 10); match[2] != want {
				t.Errorf("%s: %s calls ID %s, the released binary answers to %s", file, match[1], match[2], want)
			}
		}
	}
}
