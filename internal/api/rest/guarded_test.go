package rest_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// openRoutes are the only endpoints permitted to bypass the guard.
//
// /api/v1/authinfo has to be open: a client cannot present a credential until
// it knows which issuer to get one from, and the answer is public by
// construction — an issuer URL and a public OIDC client ID.
var openRoutes = map[string]bool{
	"GET /api/v1/authinfo": true,
}

// Every route must go through the guard.
//
// Checked against the source rather than by probing, because probing can only
// find routes a test already knows about — and the failure that matters is
// somebody adding a route and not thinking about auth at all.
//
// Routes registers every guarded endpoint through the route() helper, which
// takes an auth.Resource and so cannot be called without choosing a
// permission. Its mux.Handle call receives the pattern as a variable, never a
// literal — so any mux.Handle or mux.HandleFunc here holding a plain string
// literal went straight onto the mux and skipped the guard entirely.
//
// From M10 this file is what stops an execute route shipping unauthenticated,
// which is the single worst mistake available in this codebase.
func TestEveryRouteIsGuarded(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "rest.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	routes := findRoutesMethod(t, file)

	var direct []string
	ast.Inspect(routes, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok || recv.Name != "mux" {
			return true
		}
		if sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc" {
			return true
		}
		// A plain string literal means the pattern was registered inline. The
		// read() helper concatenates, so its calls are BinaryExprs and are not
		// collected here.
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		pattern, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Fatalf("unparseable route pattern %s", lit.Value)
		}
		direct = append(direct, pattern)
		return true
	})

	for _, pattern := range direct {
		if !openRoutes[pattern] {
			t.Errorf("route %q is registered directly on the mux and is therefore unauthenticated.\n"+
				"Register it through the route() helper with the permission it requires "+
				"(read() for plain reads), or add it to openRoutes with a comment "+
				"explaining why it is safe to serve without credentials.", pattern)
		}
	}

	// The converse: if authinfo stops being registered we would never notice,
	// because the loop above only rejects. A UI that cannot discover the issuer
	// cannot sign in.
	found := false
	for _, p := range direct {
		if p == "GET /api/v1/authinfo" {
			found = true
		}
	}
	if !found {
		t.Error("GET /api/v1/authinfo is no longer served openly; clients cannot discover how to sign in")
	}
}

func findRoutesMethod(t *testing.T, file *ast.File) *ast.FuncDecl {
	t.Helper()
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "Routes" && fn.Recv != nil {
			return fn
		}
	}
	t.Fatal("no Routes method found in rest.go; this test needs updating")
	return nil
}

// authinfo must stay free of anything an unauthenticated caller should not
// have. It exists to bootstrap a login, not to describe the deployment.
func TestAuthInfoLeaksNothingSensitive(t *testing.T) {
	srv := testServer(t)

	res, err := http.Get(srv.URL + "/api/v1/authinfo")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("authinfo must be reachable without credentials, got %d", res.StatusCode)
	}

	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, forbidden := range []string{"secret", "Secret", "password", "Password", "bearer", "Bearer"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("authinfo mentions %q, which suggests a credential leaked in:\n%s", forbidden, body)
		}
	}
}
