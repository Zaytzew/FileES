package errcat

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEveryKeyHasPolishAndDiagnostic(t *testing.T) {
	for _, spec := range All() {
		if spec.Code == "" || spec.Key == "" {
			t.Fatalf("empty identity: %+v", spec)
		}
		if spec.Diagnostic == "" {
			t.Errorf("%s/%s missing diagnostic English", spec.Code, spec.Key)
		}
		if spec.Polish == "" {
			t.Errorf("%s/%s missing Polish", spec.Code, spec.Key)
		}
		if spec.Polish == spec.Diagnostic {
			t.Errorf("%s/%s uses the same sentence for log and UI", spec.Code, spec.Key)
		}
	}
}

func TestPreferredLookupKeepsSpecificProtoCodes(t *testing.T) {
	missing, ok := ByKey("proto.missing_repo_id")
	if !ok || missing.Code != "PROTO-0004" {
		t.Fatalf("preferred proto.missing_repo_id = %+v", missing)
	}
	notFound, ok := ByKey("proto.repo_not_found")
	if !ok || notFound.Code != "PROTO-0005" {
		t.Fatalf("preferred proto.repo_not_found = %+v", notFound)
	}
	if _, ok := ByPair("PROTO-0001", "proto.repo_not_found"); !ok {
		t.Fatal("historical protoErr pair PROTO-0001/proto.repo_not_found must stay registered")
	}
}

func TestIPCHandlersAreInTheCatalog(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	pairs := append(errResponses(t, filepath.Join(root, "pkg", "ipcserver", "handlers.go")),
		errResponses(t, filepath.Join(root, "pkg", "ipcserver", "conn.go"))...)
	if len(pairs) < 50 {
		t.Fatalf("extracted too few IPC pairs: %d", len(pairs))
	}
	for _, pair := range pairs {
		if _, ok := ByPair(Code(pair.code), Key(pair.key)); !ok {
			t.Errorf("unregistered IPC pair %s %s", pair.code, pair.key)
		}
	}
}

func TestPolishFallbackDoesNotEchoKey(t *testing.T) {
	if got := Polish("not.a.real.key"); got != "Błąd zgłoszony przez daemon" {
		t.Fatalf("fallback = %q", got)
	}
}

func TestHeldByOtherUsesCatalogDetails(t *testing.T) {
	sentence := PolishDetailed("lock.held_by_other", map[string]string{
		"path":   "rysunek.dwg",
		"holder": "anna",
		"until":  "2026-08-11T13:41:16Z",
	})
	if !strings.Contains(sentence, "rysunek.dwg") || !strings.Contains(sentence, "anna") {
		t.Fatalf("sentence = %q", sentence)
	}
	if PolishDetailed("lock.operation_failed", map[string]string{"detail": "x"}) != "" {
		t.Fatal("unrelated key must not grow a detail sentence")
	}
}

type ipcPair struct{ code, key string }

func errResponses(t *testing.T, path string) []ipcPair {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var pairs []ipcPair
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := callName(call)
		switch name {
		case "ErrResponse":
			if len(call.Args) >= 5 {
				code, key := stringLit(call.Args[1]), stringLit(call.Args[4])
				if code != "" && key != "" {
					pairs = append(pairs, ipcPair{code, key})
				}
			}
		case "protoErr":
			if len(call.Args) >= 2 {
				if key := stringLit(call.Args[1]); key != "" {
					pairs = append(pairs, ipcPair{"PROTO-0001", key})
				}
			}
		}
		return true
	})
	return pairs
}

func callName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		return fun.Sel.Name
	default:
		return ""
	}
}

func stringLit(expr ast.Expr) string {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	return strings.Trim(lit.Value, `"`)
}
