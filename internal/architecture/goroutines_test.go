package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Запускатели горутин из библиотек, которые не перехватывают панику: тело для них
// оборачивается защитником safego. context.AfterFunc не проверяется: его тела —
// отмена контекста и закрытие соединения, паники в них нет.
var foreignLaunchers = map[string]string{
	"Go":     "Guard",
	"DoChan": "GuardValue",
}

const errgroupImport = "golang.org/x/sync/errgroup"

// unguardedGoroutines — голый go и тела errgroup/singleflight без защитника safego:
// паника в чужой горутине роняет весь процесс, а ожидающие её итога висят до срока.
func unguardedGoroutines(fset *token.FileSet, file *ast.File) []string {
	var found []string
	usesErrgroup := false
	for _, spec := range file.Imports {
		if path, _ := strconv.Unquote(spec.Path.Value); path == errgroupImport {
			usesErrgroup = true
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.GoStmt:
			found = append(found, fset.Position(n.Pos()).String()+": голый go — запуск через safego.Go или safego.Run")
		case *ast.CallExpr:
			if problem := unguardedLaunch(n, usesErrgroup); problem != "" {
				found = append(found, fset.Position(n.Pos()).String()+": "+problem)
			}
		}
		return true
	})
	return found
}

func unguardedLaunch(call *ast.CallExpr, usesErrgroup bool) string {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || len(call.Args) == 0 {
		return ""
	}
	guard, launcher := foreignLaunchers[selector.Sel.Name]
	if !launcher || (selector.Sel.Name == "Go" && !usesErrgroup) || isSafego(selector.X) {
		return ""
	}
	if isSafegoCall(call.Args[len(call.Args)-1], guard) {
		return ""
	}
	return selector.Sel.Name + " без safego." + guard + " — паника тела уронит процесс"
}

func isSafegoCall(expr ast.Expr, name string) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == name && isSafego(selector.X)
}

func isSafego(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "safego"
}

// TestGoroutinesAreGuarded — в internal и cmd ни одной горутины без перехвата паники.
func TestGoroutinesAreGuarded(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, path := range goFiles(t) {
		relative, _ := filepath.Rel(root, path)
		inProcess := strings.HasPrefix(relative, "internal/") || strings.HasPrefix(relative, "cmd/")
		if !inProcess || strings.HasSuffix(path, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, problem := range unguardedGoroutines(fset, file) {
			t.Error(problem)
		}
	}
	if checked < 50 {
		t.Fatalf("проверено %d исходников: сторож ничего бы не проверил", checked)
	}
}

func TestGoroutineGuardCatchesViolations(t *testing.T) {
	const source = `package sample

import "golang.org/x/sync/errgroup"

func f(group *errgroup.Group, flights *singleflight.Group) {
	go work()
	group.Go(func() error { return nil })
	group.Go(safego.Guard("ok", func() error { return nil }))
	flights.DoChan("k", func() (any, error) { return nil, nil })
	flights.DoChan("k", safego.GuardValue("ok", func() (any, error) { return nil, nil }))
	safego.Go("ok", work)
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sample.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := unguardedGoroutines(fset, file)
	if len(got) != 3 || !strings.Contains(got[0], "голый go") || !strings.Contains(got[1], "Go без safego.Guard") ||
		!strings.Contains(got[2], "DoChan без safego.GuardValue") {
		t.Fatalf("сторож ловит голый go и незащищённые тела чужих запускателей: %v", got)
	}
}
