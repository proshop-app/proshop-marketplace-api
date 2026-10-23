package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goFiles — все исходники модуля, кроме testdata.
func goFiles(t *testing.T) []string {
	t.Helper()

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	var files []string

	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && (entry.Name() == "testdata" || strings.HasPrefix(entry.Name(), ".")) {
			return filepath.SkipDir
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return files
}

// declarationsFirst — объявление верхнего уровня после функции.
func declarationsFirst(fset *token.FileSet, file *ast.File) []string {
	var (
		seenFunc bool
		found    []string
	)

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			seenFunc = true
		case *ast.GenDecl:
			if d.Tok != token.IMPORT && seenFunc {
				found = append(found, fset.Position(d.Pos()).String()+": "+d.Tok.String()+" после функции")
			}
		}
	}

	return found
}

// mainOnly — в main.go команды только функция main.
func mainOnly(fset *token.FileSet, file *ast.File) []string {
	var found []string

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv != nil || d.Name.Name != "main" {
				found = append(found, fset.Position(d.Pos()).String()+": "+d.Name.Name+" — в main.go только main")
			}
		case *ast.GenDecl:
			if d.Tok != token.IMPORT {
				found = append(found, fset.Position(d.Pos()).String()+": "+d.Tok.String()+" — в main.go только main")
			}
		}
	}

	return found
}

// onePerLine — многострочный литерал или вызов, где элементы делят строку.
// Допустимо либо всё на строке открытия скобки — f(ctx, T{ … }), — либо
// каждый элемент на своей строке.
func onePerLine(fset *token.FileSet, file *ast.File) []string {
	var found []string

	line := func(pos token.Pos) int { return fset.Position(pos).Line }
	check := func(open, closing token.Pos, elems []ast.Expr, kind string) {
		if line(open) == line(closing) || len(elems) < 2 {
			return
		}

		if line(elems[len(elems)-1].Pos()) == line(open) {
			return
		}

		lines := map[int]bool{line(open): true}

		for _, elem := range elems {
			at := line(elem.Pos())
			if lines[at] {
				found = append(found, fset.Position(elem.Pos()).String()+": "+kind+" — по элементу на строку")

				return
			}

			lines[at] = true
		}
	}

	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.CompositeLit:
			check(n.Lbrace, n.Rbrace, n.Elts, "литерал")
		case *ast.CallExpr:
			check(n.Lparen, n.Rparen, n.Args, "вызов")
		}

		return true
	})

	return found
}

// TestLayout — константы, переменные и типы объявлены в начале файла, до
// функций; main.go команд содержит только main; в многострочных литералах и
// вызовах — по элементу на строку.
func TestLayout(t *testing.T) {
	files := goFiles(t)
	if len(files) < 50 {
		t.Fatalf("найдено %d исходников: сторож ничего бы не проверил", len(files))
	}

	for _, path := range files {
		fset := token.NewFileSet()

		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}

		for _, problem := range declarationsFirst(fset, file) {
			t.Error(problem)
		}

		for _, problem := range onePerLine(fset, file) {
			t.Error(problem)
		}

		if isCommandMain(path) {
			for _, problem := range mainOnly(fset, file) {
				t.Error(problem)
			}
		}
	}
}

func TestLayoutGuardCatchesViolations(t *testing.T) {
	const source = `package main

import "os"

func main() { os.Exit(run()) }

type late struct{}

func run() int { return 0 }
`

	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, "main.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}

	if got := declarationsFirst(fset, file); len(got) != 1 || !strings.Contains(got[0], "type после функции") {
		t.Fatalf("тип после функции: %v", got)
	}
	if got := mainOnly(fset, file); len(got) != 2 {
		t.Fatalf("в main.go лишние тип и run: %v", got)
	}
}

func TestOnePerLineGuard(t *testing.T) {
	const source = `package sample

var hugged = f(ctx, T{
	A: 1,
	B: 2,
})

var packed = T{
	A: 1, B: 2,
}

var split = f(
	ctx, name,
	arg,
)

var tail = f(T{
	A: 1,
}, labels)
`

	fset := token.NewFileSet()

	file, err := parser.ParseFile(fset, "sample.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}

	got := onePerLine(fset, file)
	if len(got) != 3 {
		t.Fatalf("три нарушения — packed, split и tail: %v", got)
	}

	for i, want := range []string{"sample.go:9:", "sample.go:13:", "sample.go:17:"} {
		if !strings.HasPrefix(got[i], want) {
			t.Fatalf("нарушение %d: ожидалось %s, получено %s", i, want, got[i])
		}
	}
}

// isCommandMain — main.go одной из команд в cmd/.
func isCommandMain(path string) bool {
	return filepath.Base(path) == "main.go" && filepath.Base(filepath.Dir(filepath.Dir(path))) == "cmd"
}
