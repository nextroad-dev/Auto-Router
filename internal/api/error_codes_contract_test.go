package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The two error envelopes the service can answer with. Every code a handler writes must be a
// member of the enum the corresponding response documents, because the console maps each code
// to operator-facing copy and this test is what keeps that mapping honest.
const (
	adminEnvelope     = "AdminErrorCode"
	inferenceEnvelope = "InferenceErrorCode"
)

// trackedHelpers maps a response helper to the index of the argument that carries the error code,
// together with the envelope that helper answers on.
//
// The diagnostic helper answers on the OpenAI envelope: writeDebugError delegates to
// proxy.WriteError, and /debug/* is mounted outside /admin/, so it is not a management response.
var trackedHelpers = map[string]struct {
	codeIndex int
	envelope  string
}{
	"writeAdminError": {2, adminEnvelope},     // (w, status, code, message, field)
	"unknownPathID":   {1, adminEnvelope},     // (w, code, what)
	"writeDebugError": {2, inferenceEnvelope}, // (w, status, code, message)
	"writeError":      {3, inferenceEnvelope}, // (w, status, errorType, code, message)
}

// openAPIEnums reads docs/openapi.yaml and returns the members of the named enums. The contract
// is the single source of truth for the vocabulary, so this test reads the file rather than
// duplicating the list: a code added to a handler but not to the contract fails here.
func openAPIEnums(t *testing.T) map[string]map[string]struct{} {
	t.Helper()
	root := filepath.Join("..", "..", "docs", "openapi.yaml")
	raw, err := os.ReadFile(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	// Normalize line endings: .gitattributes pins this file to LF, but a Windows working tree
	// can still materialize it as CRLF, and the patterns below are written against LF.
	source := strings.ReplaceAll(string(raw), "\r\n", "\n")
	schemasAt := strings.Index(source, "\n  schemas:\n")
	if schemasAt < 0 {
		t.Fatal("docs/openapi.yaml has no schemas section")
	}
	body := source[schemasAt:]

	enumOf := func(name string) map[string]struct{} {
		start := regexp.MustCompile(`(?m)^    ` + name + `:$`).FindStringIndex(body)
		if start == nil {
			t.Fatalf("enum %s is not declared in docs/openapi.yaml", name)
		}
		rest := body[start[0]:]
		// The enum ends at the next sibling schema key at the same indentation.
		next := regexp.MustCompile(`(?m)^    [A-Za-z]+:$`).FindStringIndex(rest[1:])
		if next == nil {
			t.Fatalf("could not delimit enum %s", name)
		}
		members := map[string]struct{}{}
		for _, m := range regexp.MustCompile(`(?m)^        - ([a-z_]+)$`).FindAllStringSubmatch(rest[:next[0]+1], -1) {
			members[m[1]] = struct{}{}
		}
		if len(members) == 0 {
			t.Fatalf("enum %s has no members", name)
		}
		return members
	}
	return map[string]map[string]struct{}{
		adminEnvelope:     enumOf(adminEnvelope),
		inferenceEnvelope: enumOf(inferenceEnvelope),
	}
}

type emittedCode struct {
	code     string
	file     string
	line     int
	helper   string
	envelope string
}

// passThroughFuncs are functions whose call sites forward a caller-supplied code rather than
// choosing one. Their own bodies carry no vocabulary decision, so they are not inspected:
//   - the tracked helpers themselves forward to the next writer (writeDebugError and
//     unknownPathID both delegate), and
//   - AuthFailure.Write is the envelope dispatcher: it reads the code out of the failure value
//     and selects the envelope from the request path. That dispatch is modelled explicitly in
//     TestEmittedErrorCodesAreDeclaredInTheContract instead.
var passThroughFuncs = map[string]struct{}{
	"writeAdminError": {},
	"unknownPathID":   {},
	"writeDebugError": {},
	"writeError":      {},
	"Write":           {}, // (*AuthFailure).Write
}

// emittedCodes parses every non-test Go file under internal/ and cmd/ and collects the error code
// each response helper is called with.
//
// The code argument is always a string literal today, which is what makes a source-level check
// exact rather than heuristic. A non-literal call site is reported so this test cannot silently
// stop covering it.
func emittedCodes(t *testing.T) []emittedCode {
	t.Helper()
	var found []emittedCode
	fset := token.NewFileSet()

	for _, root := range []string{".", filepath.Join("..", "..", "cmd")} {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, parseErr := parser.ParseFile(fset, path, nil, 0)
			if parseErr != nil {
				return parseErr
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				// A pass-through body forwards whatever code its caller supplied (for example
				// unknownPathID forwards to writeAdminError) rather than choosing one.
				if _, isPassThrough := passThroughFuncs[fn.Name.Name]; isPassThrough {
					continue
				}
				ast.Inspect(fn.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					ident, ok := call.Fun.(*ast.Ident)
					if !ok {
						return true
					}
					helper, tracked := trackedHelpers[ident.Name]
					if !tracked || len(call.Args) <= helper.codeIndex {
						return true
					}
					literal, ok := call.Args[helper.codeIndex].(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						t.Errorf("%s:%d: %s is called with a non-literal code; this test must be extended to keep covering it",
							filepath.ToSlash(path), fset.Position(call.Pos()).Line, ident.Name)
						return true
					}
					value, unquoteErr := strconv.Unquote(literal.Value)
					if unquoteErr != nil {
						t.Errorf("%s: unquote %s: %v", filepath.ToSlash(path), literal.Value, unquoteErr)
						return true
					}
					found = append(found, emittedCode{
						code: value, file: filepath.ToSlash(path), helper: ident.Name,
						line: fset.Position(call.Pos()).Line, envelope: helper.envelope,
					})
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	if len(found) == 0 {
		t.Fatal("found no error-code call sites; the walk or the tracked-helper list is wrong")
	}
	return found
}

// TestEmittedErrorCodesAreDeclaredInTheContract fails when a handler can answer with a code the
// OpenAPI enum for its envelope does not declare. That mismatch is invisible at runtime — the
// console just falls back to generic copy — so it is caught here instead.
func TestEmittedErrorCodesAreDeclaredInTheContract(t *testing.T) {
	enums := openAPIEnums(t)

	var lines []string
	for _, call := range emittedCodes(t) {
		if _, ok := enums[call.envelope][call.code]; !ok {
			lines = append(lines, call.code+" emitted by "+call.helper+" at "+
				call.file+":"+strconv.Itoa(call.line)+" is not declared in "+call.envelope)
		}
	}

	// AuthFailure.Write routes by path: /admin/* answers on the management envelope, every other
	// guarded path answers on the OpenAI envelope. The credential codes below are produced by
	// Authenticate, which both branches call, so they must be declared in both vocabularies.
	for _, code := range []string{"invalid_api_key", "insufficient_scope"} {
		for _, envelope := range []string{adminEnvelope, inferenceEnvelope} {
			if _, ok := enums[envelope][code]; !ok {
				lines = append(lines, code+" is returned by Authenticate on the "+envelope+
					" path but is not declared in it")
			}
		}
	}
	// invalid_session and cross_site_request are constructed only inside the /admin/ branch of
	// WithManagementSurface, so they never reach a non-management path and are admin-only.
	for _, code := range []string{"invalid_session", "cross_site_request"} {
		if _, ok := enums[adminEnvelope][code]; !ok {
			lines = append(lines, code+" is returned by the management surface but is not declared in "+adminEnvelope)
		}
	}

	if len(lines) > 0 {
		sort.Strings(lines)
		t.Errorf("error codes are emitted that docs/openapi.yaml does not declare:\n  %s", strings.Join(lines, "\n  "))
	}
}

// TestDebugEndpointsAnswerOnTheInferenceEnvelope pins the fact that the diagnostic endpoints
// share the OpenAI envelope. If they ever move to the management envelope their codes must move
// with them, and this test is where that decision becomes visible.
func TestDebugEndpointsAnswerOnTheInferenceEnvelope(t *testing.T) {
	enums := openAPIEnums(t)
	inference, admin := enums[inferenceEnvelope], enums[adminEnvelope]

	for _, code := range []string{"invalid_request", "request_too_large", "debug_route_unavailable"} {
		if _, ok := inference[code]; !ok {
			t.Errorf("%s is emitted on the OpenAI envelope by the diagnostic endpoints but is not in %s", code, inferenceEnvelope)
		}
	}
	// The diagnostic endpoints reuse the management spelling for a malformed body.
	if _, ok := admin["invalid_request"]; !ok {
		t.Errorf("invalid_request is no longer declared in %s; the diagnostic endpoints share this spelling", adminEnvelope)
	}
}
