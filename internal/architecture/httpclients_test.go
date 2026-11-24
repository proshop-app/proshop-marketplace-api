package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

const httpclientPackage = "internal/infrastructure/httpclient/"

// handmadeClients — http.Client или http.Transport собраны вручную либо взяты стандартные:
// у стандартного транспорта два простаивающих соединения на хост и нет сроков установки.
func handmadeClients(fset *token.FileSet, file *ast.File) []string {
	var found []string
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.CompositeLit:
			if name := httpName(n.Type); name == "Client" || name == "Transport" {
				message := ": http." + name + " вручную — httpclient.Internal/External"
				found = append(found, fset.Position(n.Pos()).String()+message)
			}
		case *ast.SelectorExpr:
			if name := httpName(n); name == "DefaultClient" || name == "DefaultTransport" {
				found = append(found, fset.Position(n.Pos()).String()+": http."+name+" — httpclient.Internal/External")
			}
		}
		return true
	})
	return found
}

func httpName(expr ast.Expr) string {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if ident, ok := selector.X.(*ast.Ident); ok && ident.Name == "http" {
		return selector.Sel.Name
	}
	return ""
}

// TestHTTPClientsComeFromOneAssembly — HTTP-клиенты процесса собирает только httpclient.
func TestHTTPClientsComeFromOneAssembly(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range goFiles(t) {
		relative, _ := filepath.Rel(root, path)
		inProcess := strings.HasPrefix(relative, "internal/") || strings.HasPrefix(relative, "cmd/")
		if !inProcess || strings.HasSuffix(path, "_test.go") || strings.HasPrefix(relative, httpclientPackage) {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, problem := range handmadeClients(fset, file) {
			t.Error(problem)
		}
	}
}

func TestHTTPClientGuardCatchesViolations(t *testing.T) {
	const source = `package sample

var a = &http.Client{Timeout: 1}
var b = http.Transport{}
var c = http.DefaultClient
var d = httpclient.Internal(httpclient.Config{})
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sample.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := handmadeClients(fset, file); len(got) != 3 {
		t.Fatalf("сторож ловит ручные и стандартные клиенты: %v", got)
	}
}
