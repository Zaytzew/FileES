package mobileworker

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	v1 "filees/pkg/mobile/v1"
)

// This file guards one specific way a defect entered this product and lived
// for three weeks on a fully green test suite.
//
// r540 (16 August) made the append worker create absent parent directories.
// It shipped with a test — TestAppendCreatesMissingMobileUploadsTree — which
// pins exactly the half of the behaviour its author had in mind. The other
// half, the scope, stayed in a comment. The consequence was that
// DESTINATION_GONE, a documented outcome of section 10.2, could not be
// produced by any code path: a directory deleted on the server was silently
// recreated under the phone instead of being reported. Nightly runs would
// have been green every single night, because nothing was regressing —
// something was simply never reachable.
//
// A declared outcome that no path produces is a defect by construction, and
// unlike "is this the right behaviour" it needs no human judgement to detect.
// So this test enumerates the outcome enum FROM ITS SOURCE and demands that
// every member is either produced by a scenario here, or declared pending
// with a reason. Adding a member to the enum breaks this test until one of
// the two is done — the failure lands on whoever adds it, not on triage three
// weeks later.
//
// What it does not do: it cannot tell whether a scenario passes for the right
// reason, and it cannot know about behaviour nobody wrote down. It raises the
// floor. It is not the ceiling.

// outcomeEnumSource is parsed, not imported: Go constants are not enumerable
// at run time, and a hand-kept list here would reintroduce exactly the
// forgetting this test exists to prevent.
func outcomeEnumSource() string {
	return filepath.Join("..", "..", "pkg", "mobile", "v1", "messages.go")
}

// outcomeScenarios maps each outcome to a scenario that actually produces it.
// Membership is not a promise: the test runs every entry and checks what
// comes back.
var outcomeScenarios = map[v1.Outcome]func(t *testing.T) v1.UploadObjectResult{
	v1.OutcomeCommitted: func(t *testing.T) v1.UploadObjectResult {
		a := newAppender(t, newSeededRepo(t), "rw")
		return a.mustUpload(t, "photos/2026", "reach.jpg", []byte("nowy obiekt"))
	},
	v1.OutcomeNameTakenSame: func(t *testing.T) v1.UploadObjectResult {
		// The seed writes exactly "hello" to photos/2026/a.jpg, so this is
		// the same name carrying the same bytes: a dedup drop.
		a := newAppender(t, newSeededRepo(t), "rw")
		return a.mustUpload(t, "photos/2026", "a.jpg", []byte("hello"))
	},
	v1.OutcomeNameTakenDiff: func(t *testing.T) v1.UploadObjectResult {
		a := newAppender(t, newSeededRepo(t), "rw")
		return a.mustUpload(t, "photos/2026", "a.jpg", []byte("inna tresc"))
	},
	v1.OutcomeDestGone: func(t *testing.T) v1.UploadObjectResult {
		// "photos" exists, "photos/gone" does not — a directory removed on
		// the server, seen by a phone holding a stale manifest.
		a := newAppender(t, newSeededRepo(t), "rw")
		return a.mustUpload(t, "photos/gone", "x.bin", []byte("dane"))
	},
	v1.OutcomeAccessRevoked: func(t *testing.T) v1.UploadObjectResult {
		a := newAppender(t, newSeededRepo(t), "r")
		return a.mustUpload(t, "photos/2026", "x.bin", []byte("dane"))
	},
}

// outcomePending declares outcomes the concept defines but the worker cannot
// yet produce. A declaration is not an excuse — it is the visible form of a
// gap, dated and attributable, as opposed to the silence that hid
// DESTINATION_GONE. Each entry must say what is missing and where it is
// specified.
//
// Both entries below were found by writing this test, on 2026-09-09. Neither
// is implemented anywhere in the worker, so neither can currently be reported
// to a phone.
var outcomePending = map[v1.Outcome]string{
	v1.OutcomeRepoInactive: "View carries RepoPath, Generation and Access only — " +
		"repository state lives in RepositoryGrant.State and never reaches the " +
		"appender, so an inactive repository is indistinguishable from an active " +
		"one here. Concept section 10.2 names the outcome; nothing derives it.",
	v1.OutcomePolicyReject: "Concept section 6.4 step 1 requires the worker to check " +
		"'rw and the mobile policy'. The rw half exists (OutcomeAccessRevoked); " +
		"the mobile policy half has no representation in this package at all — " +
		"no field, no interface, no call.",
}

type declaredOutcome struct {
	ident string // Go identifier, e.g. OutcomeDestGone
	value v1.Outcome
}

// parseOutcomeConsts reads every constant of type Outcome out of the enum's
// own source file.
func parseOutcomeConsts(t *testing.T) []declaredOutcome {
	t.Helper()
	src := outcomeEnumSource()
	file, err := parser.ParseFile(token.NewFileSet(), src, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}
	var found []declaredOutcome
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			typeName, ok := vs.Type.(*ast.Ident)
			if !ok || typeName.Name != "Outcome" || len(vs.Names) != 1 || len(vs.Values) != 1 {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok {
				continue
			}
			text, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("%s: unquote %s: %v", vs.Names[0].Name, lit.Value, err)
			}
			found = append(found, declaredOutcome{ident: vs.Names[0].Name, value: v1.Outcome(text)})
		}
	}
	if len(found) == 0 {
		t.Fatalf("no Outcome constants found in %s — the enum moved and this guard went blind", src)
	}
	return found
}

// workerUses reports whether the worker's own non-test sources actually
// reference the identifier. It is the reverse guard on outcomePending: once
// someone implements a pending outcome, the stale declaration must not
// survive it.
//
// This reads the syntax tree rather than the bytes, and the distinction is
// the whole point of today's lesson: a comment naming an outcome is prose,
// not an implementation. Matching text would raise a false alarm on the very
// habit — describing a constraint instead of enforcing it — that produced the
// bug this file guards against. A guard nobody trusts gets deleted.
func workerUses(t *testing.T, ident string) bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		used := false
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if ok && sel.Sel != nil && sel.Sel.Name == ident {
				used = true
				return false
			}
			return !used
		})
		if used {
			return true
		}
	}
	return false
}

// TestEveryDeclaredOutcomeIsAccountedFor is the static half and deliberately
// needs no SVN: on a machine without the binaries the enforcement still runs,
// because a guard that skips itself is the failure mode it exists to prevent.
func TestEveryDeclaredOutcomeIsAccountedFor(t *testing.T) {
	declared := parseOutcomeConsts(t)
	known := make(map[v1.Outcome]bool, len(declared))

	for _, d := range declared {
		known[d.value] = true
		_, hasScenario := outcomeScenarios[d.value]
		reason, isPending := outcomePending[d.value]

		switch {
		case hasScenario && isPending:
			t.Errorf("%s (%q): both a scenario and a pending declaration — decide which is true", d.ident, d.value)
		case !hasScenario && !isPending:
			t.Errorf("%s (%q): declared in the enum but no scenario produces it and no pending "+
				"declaration explains why. This is the DESTINATION_GONE shape: an outcome the "+
				"protocol promises and no code path can deliver. Add a scenario to "+
				"outcomeScenarios, or a reason to outcomePending.", d.ident, d.value)
		case isPending:
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s (%q): pending without a reason", d.ident, d.value)
			}
			if workerUses(t, d.ident) {
				t.Errorf("%s (%q): declared pending, but the worker's own sources now use it. "+
					"If it is implemented, move it to outcomeScenarios and delete the declaration.", d.ident, d.value)
			}
		}
	}

	for outcome := range outcomeScenarios {
		if !known[outcome] {
			t.Errorf("scenario registered for %q, which the enum no longer declares", outcome)
		}
	}
	for outcome := range outcomePending {
		if !known[outcome] {
			t.Errorf("pending declaration for %q, which the enum no longer declares", outcome)
		}
	}
}

// TestRegisteredOutcomesAreActuallyProduced runs every scenario. Registration
// alone would be a list of intentions; this is what makes the map evidence.
func TestRegisteredOutcomesAreActuallyProduced(t *testing.T) {
	requireSVN(t)
	for outcome, scenario := range outcomeScenarios {
		t.Run(string(outcome), func(t *testing.T) {
			got := scenario(t)
			if got.Outcome != outcome {
				t.Fatalf("scenario for %q produced %q instead (%+v)", outcome, got.Outcome, got)
			}
		})
	}
}
