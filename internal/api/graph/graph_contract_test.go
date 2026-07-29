package graph

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The payload exists to be read by one consumer, and its cost is paid four
// times over: serialised here, transferred, parsed by the browser, then held in
// memory. A field no one reads costs all four and buys nothing.
//
// That is not hypothetical. This payload carried an edge per proposed move, per
// anti-affinity relation and per PDB membership — 45% of all elements at 2,500
// pods — for the whole time between M11 replacing the node-link graph and M19
// noticing. It also carried utilization, drained, targetNode and moveStep,
// which nothing ever read.
//
// So the struct is checked against the frontend. Every json tag on Data must
// appear in the UI sources.
func TestEveryPayloadFieldIsReadByTheUI(t *testing.T) {
	fields := jsonTags(t)
	if len(fields) == 0 {
		t.Fatal("parsed no json tags off Data; the parser is broken, not the payload")
	}

	ui := uiSources(t)

	for _, f := range fields {
		// Property access (d.zone) or index (d["zone"]). A bare occurrence
		// would match the api.ts type declaration and pass for a field that is
		// declared but never used, which is the exact failure being guarded.
		pat := regexp.MustCompile(`[.\[]\s*["']?` + regexp.QuoteMeta(f) + `\b`)
		if !pat.MatchString(ui) {
			t.Errorf("Data.%s is emitted to every client but the UI never reads it; "+
				"remove it, or wire it up", f)
		}
	}
}

// The counterpart: a field the UI reads but the payload stopped sending shows
// up as undefined at runtime rather than as a compile error, because it is
// optional in the TypeScript type. This catches the api.ts type drifting ahead
// of the Go struct.
func TestUITypeDoesNotDeclareFieldsThePayloadNoLongerSends(t *testing.T) {
	sent := map[string]bool{}
	for _, f := range jsonTags(t) {
		sent[f] = true
	}
	// Fields the UI type carries for its own reasons, not from this payload.
	allowed := map[string]bool{"kind": true, "id": true}

	path := filepath.Join(repoRoot(t), "ui/src/api.ts")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read api.ts: %v", err)
	}

	// The GraphData interface, whatever it is called, is the one declaring
	// "parent" — the field unique to this payload.
	block := interfaceContaining(string(src), "parent")
	if block == "" {
		t.Fatal("could not locate the graph element interface in api.ts")
	}

	for _, m := range regexp.MustCompile(`(?m)^\s*([a-zA-Z]+)\??:`).FindAllStringSubmatch(block, -1) {
		f := m[1]
		if !sent[f] && !allowed[f] {
			t.Errorf("api.ts declares %q on the graph element, but the payload no longer sends it; "+
				"it will read as undefined at runtime", f)
		}
	}
}

func jsonTags(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "graph.go", nil, 0)
	if err != nil {
		t.Fatalf("parse graph.go: %v", err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "Data" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, fld := range st.Fields.List {
			if fld.Tag == nil {
				continue
			}
			tag := reflect.StructTag(strings.Trim(fld.Tag.Value, "`")).Get("json")
			name, _, _ := strings.Cut(tag, ",")
			if name != "" && name != "-" {
				out = append(out, name)
			}
		}
		return false
	})
	return out
}

func uiSources(t *testing.T) string {
	t.Helper()
	root := filepath.Join(repoRoot(t), "ui/src")
	var b strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".tsx") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		b.Write(src)
		return nil
	})
	if err != nil {
		t.Fatalf("walk ui/src: %v", err)
	}
	if b.Len() == 0 {
		t.Fatal("read no UI sources; the check would pass vacuously")
	}
	return b.String()
}

func interfaceContaining(src, marker string) string {
	for _, block := range regexp.MustCompile(`(?s)interface\s+\w+\s*\{.*?\n\}`).FindAllString(src, -1) {
		if regexp.MustCompile(`(?m)^\s*` + marker + `\??:`).MatchString(block) {
			return block
		}
	}
	return ""
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}
