package client

import (
	"encoding/json"
	"strings"
	"testing"
)

const conversionInventoryReceipt = `{"inventory_version":1,"remote":false,"entries":[
{"path":".","item":"normal","props":"normal","kind":"dir","depth":"infinity","revision":0,"versioned":true,"copied":false,"switched":false,"file_external":false,"wc_locked":false},
{"path":"docs/a.txt","item":"normal","props":"none","kind":"file","depth":"unknown","revision":4,"versioned":true,"copied":false,"switched":false,"file_external":false,"wc_locked":false},
{"path":"private.bak","item":"ignored","props":"none","kind":"unknown","depth":"unknown","revision":null,"versioned":false,"copied":false,"switched":false,"file_external":false,"wc_locked":false}]}`

func inventoryReply(t *testing.T, reply string) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal([]byte(reply), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestConversionInventoryIncludesRootAndDoesNotPromoteMixedRevisions(t *testing.T) {
	result, err := decodeConversionInventory(inventoryReply(t, conversionInventoryReceipt))
	if err != nil || len(result.Entries) != 3 {
		t.Fatalf("inventory=%+v err=%v", result, err)
	}
	if result.Entries[0].Path != "." || *result.Entries[0].Revision != 0 || *result.Entries[1].Revision != 4 || result.Entries[2].Versioned {
		t.Fatalf("lost root, r0, mixed BASE or ignored object: %+v", result)
	}
	if err := result.CheckFullClean(); err != nil {
		t.Fatal(err)
	}
	// The ordinary adapter's historical behaviour remains unchanged.
	if entries := appendNativeStatus(nil, inventoryReply(t, conversionInventoryReceipt)); len(entries) != 2 {
		t.Fatalf("changed normal Status behaviour: %+v", entries)
	}
}

func TestConversionInventoryRefusesIncompleteNativeProof(t *testing.T) {
	for name, modify := range map[string]func(map[string]any){
		"old helper":          func(raw map[string]any) { delete(raw, "inventory_version") },
		"wrong version":       func(raw map[string]any) { raw["inventory_version"] = float64(2) },
		"remote result":       func(raw map[string]any) { raw["remote"] = true },
		"missing root":        func(raw map[string]any) { raw["entries"] = raw["entries"].([]any)[1:] },
		"duplicate root":      func(raw map[string]any) { rows := raw["entries"].([]any); raw["entries"] = append(rows, rows[0]) },
		"no entries":          func(raw map[string]any) { raw["entries"] = []any{} },
		"no switched flag":    func(raw map[string]any) { delete(raw["entries"].([]any)[0].(map[string]any), "switched") },
		"absent revision":     func(raw map[string]any) { delete(raw["entries"].([]any)[0].(map[string]any), "revision") },
		"negative revision":   func(raw map[string]any) { raw["entries"].([]any)[0].(map[string]any)["revision"] = float64(-1) },
		"fractional revision": func(raw map[string]any) { raw["entries"].([]any)[0].(map[string]any)["revision"] = float64(1.5) },
		"huge revision":       func(raw map[string]any) { raw["entries"].([]any)[0].(map[string]any)["revision"] = float64(1 << 54) },
		"traversal":           func(raw map[string]any) { raw["entries"].([]any)[1].(map[string]any)["path"] = "../other" },
		"absolute path":       func(raw map[string]any) { raw["entries"].([]any)[1].(map[string]any)["path"] = "C:/other" },
	} {
		t.Run(name, func(t *testing.T) {
			raw := inventoryReply(t, conversionInventoryReceipt)
			modify(raw)
			if _, err := decodeConversionInventory(raw); err == nil {
				t.Fatal("incomplete inventory accepted")
			}
		})
	}
}

func TestConversionInventoryRootPropertiesAndUnsupportedStatesBlockCleanProof(t *testing.T) {
	for _, change := range []struct{ old, next string }{
		{`"props":"normal"`, `"props":"modified"`},
		{`"item":"normal"`, `"item":"modified"`},
		{`"copied":false`, `"copied":true`},
		{`"switched":false`, `"switched":true`},
		{`"file_external":false`, `"file_external":true`},
		{`"wc_locked":false`, `"wc_locked":true`},
		{`"depth":"infinity"`, `"depth":"empty"`},
		{`"depth":"infinity"`, `"depth":"exclude"`},
		{`"revision":0`, `"revision":null`},
		{`"versioned":true`, `"conflicted":true,"versioned":true`},
		{`"kind":"file"`, `"kind":"symlink"`},
		{`"item":"ignored"`, `"item":"external"`},
	} {
		t.Run(change.next, func(t *testing.T) {
			result, err := decodeConversionInventory(inventoryReply(t, strings.Replace(conversionInventoryReceipt, change.old, change.next, 1)))
			if err != nil {
				t.Fatal(err)
			}
			if err := result.CheckFullClean(); err == nil {
				t.Fatal("unsupported or dirty WC accepted")
			}
		})
	}
}

func TestConversionInventoryCannotFallBackToCLI(t *testing.T) {
	c := New(Options{SvnPath: "must-not-run"}).(*execClient)
	if _, err := c.ConversionInventory(t.Context(), t.TempDir()); err == nil {
		t.Fatal("non-native conversion inspection accepted")
	}
}
