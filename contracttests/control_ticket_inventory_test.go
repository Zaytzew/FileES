package contracttests

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// The wire enum, both validators and repository dispatcher must evolve in
// one change. Older lists silently diverged while intentional early routes
// (passport/settings) were maintained separately from durable operations.
func TestControlTicketInventoryCoversValidatorsAndWorker(t *testing.T) {
	parse := func(path string) *ast.File {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	protocol := parse("../pkg/control/v1/messages.go")
	declared := map[string]bool{}
	ast.Inspect(protocol, func(n ast.Node) bool {
		v, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		typ, ok := v.Type.(*ast.Ident)
		if ok && typ.Name == "TicketType" {
			for _, name := range v.Names {
				declared[name.Name] = true
			}
		}
		return true
	})
	refs := func(node ast.Node) map[string]bool {
		out := map[string]bool{}
		ast.Inspect(node, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && declared[id.Name] {
				out[id.Name] = true
			}
			return true
		})
		return out
	}
	check := func(label string, got map[string]bool) {
		for name := range declared {
			if !got[name] {
				t.Errorf("%s omits %s", label, name)
			}
		}
	}
	validators := 0
	for _, decl := range protocol.Decls {
		f, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if f.Name.Name == "Validate" && f.Recv != nil {
			typ, ok := f.Recv.List[0].Type.(*ast.Ident)
			if ok && (typ.Name == "Ticket" || typ.Name == "Result") {
				check(typ.Name+".Validate", refs(f.Body))
				validators++
			}
		}
		if f.Name.Name == "validateSuccessPayload" {
			check(f.Name.Name, refs(f.Body))
			validators++
		}
	}
	if validators != 3 || len(declared) < 30 {
		t.Fatal("inventory unexpectedly empty", validators, len(declared))
	}
	workerRefs := map[string]bool{}
	for _, path := range []string{"../pkg/repoworker/worker.go", "../pkg/repoworker/upload_channels.go"} {
		for _, decl := range parse(path).Decls {
			f, ok := decl.(*ast.FuncDecl)
			if !ok || (f.Name.Name != "Handle" && f.Name.Name != "isUploadChannelTicket") {
				continue
			}
			for name := range refs(f.Body) {
				workerRefs[name] = true
			}
		}
	}
	check("Worker.Handle (including passport/settings/upload early routes)", workerRefs)
	// Enum aliases do not count as another wire type.
	for name := range declared {
		if !strings.HasPrefix(name, "Ticket") {
			t.Errorf("unexpected ticket declaration %s", name)
		}
	}
}
